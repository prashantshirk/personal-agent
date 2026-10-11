package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"secondbrain/internal/config"
)

const uploadPartBytes = 8 << 20

type R2 struct {
	client     *s3.Client
	bucket     string
	recipient  *age.X25519Recipient
	identities []age.Identity
}

func NewR2(ctx context.Context, cfg config.Config) (*R2, error) {
	if cfg.R2Bucket == "" {
		return nil, fmt.Errorf("R2 storage is not configured")
	}
	recipient, err := age.ParseX25519Recipient(strings.TrimSpace(cfg.AgeRecipient))
	if err != nil {
		return nil, fmt.Errorf("parse AGE_RECIPIENT: %w", err)
	}
	keyFile, err := os.Open(cfg.AgeIdentityFile)
	if err != nil {
		return nil, fmt.Errorf("open age identity file: %w", err)
	}
	identities, parseErr := age.ParseIdentities(keyFile)
	closeErr := keyFile.Close()
	if parseErr != nil {
		return nil, fmt.Errorf("parse age identity file: %w", parseErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("auto"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.R2AccessKeyID, cfg.R2SecretAccessKey, "")))
	if err != nil {
		return nil, fmt.Errorf("load S3 configuration: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(cfg.R2Endpoint); o.UsePathStyle = true })
	return &R2{client: client, bucket: cfg.R2Bucket, recipient: recipient, identities: identities}, nil
}

func RandomKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "objects/" + hex.EncodeToString(b[:]), nil
}

func (r *R2) UploadEncrypted(ctx context.Context, key, source, contentType string) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	created, err := r.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(r.bucket), Key: aws.String(key), ContentType: aws.String(contentType), Metadata: map[string]string{"encrypted": "age"}})
	if err != nil {
		return fmt.Errorf("start R2 multipart upload: %w", err)
	}
	reader, writer := io.Pipe()
	encryptedDone := make(chan error, 1)
	go func() {
		enc, e := age.Encrypt(writer, r.recipient)
		if e == nil {
			_, e = io.Copy(enc, file)
			closeErr := enc.Close()
			if e == nil {
				e = closeErr
			}
		}
		if e != nil {
			_ = writer.CloseWithError(e)
		} else {
			_ = writer.Close()
		}
		encryptedDone <- e
	}()
	var completed []types.CompletedPart
	partNumber := int32(1)
	buffer := make([]byte, uploadPartBytes)
	for {
		n, readErr := io.ReadFull(reader, buffer)
		if n > 0 {
			uploaded, uploadErr := r.client.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(r.bucket), Key: aws.String(key), UploadId: created.UploadId, PartNumber: aws.Int32(partNumber), Body: bytes.NewReader(buffer[:n]), ContentLength: aws.Int64(int64(n))})
			if uploadErr != nil {
				_ = reader.CloseWithError(uploadErr)
				<-encryptedDone
				r.abort(ctx, key, created.UploadId)
				return fmt.Errorf("upload R2 part %d: %w", partNumber, uploadErr)
			}
			completed = append(completed, types.CompletedPart{ETag: uploaded.ETag, PartNumber: aws.Int32(partNumber)})
			partNumber++
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			<-encryptedDone
			r.abort(ctx, key, created.UploadId)
			return fmt.Errorf("encrypt file stream: %w", readErr)
		}
	}
	if err := <-encryptedDone; err != nil {
		r.abort(ctx, key, created.UploadId)
		return fmt.Errorf("encrypt file stream: %w", err)
	}
	_, err = r.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(r.bucket), Key: aws.String(key), UploadId: created.UploadId, MultipartUpload: &types.CompletedMultipartUpload{Parts: completed}})
	if err != nil {
		r.abort(ctx, key, created.UploadId)
		return fmt.Errorf("complete R2 multipart upload: %w", err)
	}
	return nil
}
func (r *R2) abort(ctx context.Context, key string, uploadID *string) {
	_, _ = r.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(r.bucket), Key: aws.String(key), UploadId: uploadID})
}

func (r *R2) DownloadDecrypted(ctx context.Context, key, dest string) error {
	obj, err := r.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("download R2 object: %w", err)
	}
	defer obj.Body.Close()
	plain, err := age.Decrypt(obj.Body, r.identities...)
	if err != nil {
		return fmt.Errorf("decrypt age object: %w", err)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, plain)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("write decrypted file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return closeErr
	}
	return nil
}
func (r *R2) Delete(ctx context.Context, key string) error {
	_, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(key)})
	return err
}

func EncryptStream(src io.Reader, dst io.Writer, recipient string) error {
	r, err := age.ParseX25519Recipient(strings.TrimSpace(recipient))
	if err != nil {
		return err
	}
	enc, err := age.Encrypt(dst, r)
	if err != nil {
		return err
	}
	if _, err = io.Copy(enc, src); err != nil {
		_ = enc.Close()
		return err
	}
	return enc.Close()
}
func DecryptStream(src io.Reader, dst io.Writer, identity string) error {
	ids, err := age.ParseIdentities(strings.NewReader(identity))
	if err != nil {
		return err
	}
	plain, err := age.Decrypt(src, ids...)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, plain)
	return err
}
