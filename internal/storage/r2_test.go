package storage

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"filippo.io/age"
)

func TestAgeStreamingRoundTrip(t *testing.T){
	identity,err:=age.GenerateX25519Identity();if err!=nil{t.Fatal(err)}
	plain:=bytes.Repeat([]byte("certificate private content\n"),4096);var encrypted bytes.Buffer
	if err:=EncryptStream(bytes.NewReader(plain),&encrypted,identity.Recipient().String());err!=nil{t.Fatal(err)}
	var decoded bytes.Buffer;if err:=DecryptStream(bytes.NewReader(encrypted.Bytes()),&decoded,identity.String());err!=nil{t.Fatal(err)}
	if got,want:=sha256.Sum256(decoded.Bytes()),sha256.Sum256(plain);got!=want{t.Fatal("decrypted content hash did not match source")}
}
