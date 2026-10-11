package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"secondbrain/internal/config"
)

type Local struct {
	root       string
	recipient  *age.X25519Recipient
	identities []age.Identity
}

func NewLocal(cfg config.Config) (*Local, error) {
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
	if err := os.MkdirAll(cfg.LocalStorageDir, 0700); err != nil {
		return nil, fmt.Errorf("create local storage directory: %w", err)
	}
	return &Local{root: cfg.LocalStorageDir, recipient: recipient, identities: identities}, nil
}

func (s *Local) UploadEncrypted(ctx context.Context, key, source, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dest, err := s.objectPath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".secondbrain-object-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	enc, err := age.Encrypt(tmp, s.recipient)
	if err == nil {
		_, err = io.Copy(enc, in)
		closeErr := enc.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("encrypt local object: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("install local object: %w", err)
	}
	return nil
}

func (s *Local) DownloadDecrypted(ctx context.Context, key, dest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := s.objectPath(key)
	if err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	plain, err := age.Decrypt(in, s.identities...)
	if err != nil {
		return fmt.Errorf("decrypt local object: %w", err)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, plain)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("write decrypted local object: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return closeErr
	}
	return ctx.Err()
}

func (s *Local) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := s.objectPath(key)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

func (s *Local) objectPath(key string) (string, error) {
	clean := path.Clean(key)
	if clean == "." || path.IsAbs(clean) || clean != key || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid object key")
	}
	return filepath.Join(s.root, filepath.FromSlash(clean)), nil
}
