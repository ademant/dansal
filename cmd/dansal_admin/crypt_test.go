package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/scrypt"
)

func TestEncryptDecryptFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "backup.tar.gz")
	dst := filepath.Join(dir, "backup.tar.gz.enc")
	if err := os.WriteFile(src, []byte("plaintext backup contents"), 0o600); err != nil {
		t.Fatal(err)
	}

	password := []byte("hunter2")
	if err := encryptFile(src, dst, password); err != nil {
		t.Fatalf("encryptFile: %v", err)
	}

	plain, err := decryptFile(dst, password)
	if err != nil {
		t.Fatalf("decryptFile: %v", err)
	}
	if string(plain) != "plaintext backup contents" {
		t.Errorf("decrypted = %q, want original content", plain)
	}

	if _, err := decryptFile(dst, []byte("wrong password")); err == nil {
		t.Error("expected decryption to fail with wrong password")
	}
}

// writeLegacyScryptFile builds a version 0x01 (scrypt) encrypted backup file
// by hand, matching the format that shipped before #803 switched new
// backups to PBKDF2. This lets us verify decryptFile still reads backups
// created by the old dansal_admin binary.
func writeLegacyScryptFile(t *testing.T, dst string, password, data []byte) {
	t.Helper()

	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		t.Fatal(err)
	}
	key, err := scrypt.Key(password, salt, scryptN, scryptR, scryptP, scryptKey)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	ciphertext := gcm.Seal(nil, nonce, data, nil)

	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var nBuf [4]byte
	binary.BigEndian.PutUint32(nBuf[:], scryptN)

	for _, chunk := range [][]byte{
		[]byte(encMagic),
		{0x01},
		nBuf[:],
		{scryptR, scryptP},
		salt,
		nonce,
		ciphertext,
	} {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDecryptFileLegacyScryptFormat(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "legacy.enc")
	password := []byte("hunter2")
	writeLegacyScryptFile(t, dst, password, []byte("old backup content"))

	plain, err := decryptFile(dst, password)
	if err != nil {
		t.Fatalf("decryptFile on legacy scrypt file: %v", err)
	}
	if string(plain) != "old backup content" {
		t.Errorf("decrypted = %q, want old backup content", plain)
	}
}

func TestDecryptFileRejectsUnsupportedVersion(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "bogus.enc")
	if err := os.WriteFile(dst, append([]byte(encMagic), 0x99), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := decryptFile(dst, []byte("x")); err == nil {
		t.Error("expected error for unsupported version byte")
	}
}
