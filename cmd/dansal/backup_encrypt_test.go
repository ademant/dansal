package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ademant/dansal/internal/backupcrypt"
)

// TestEncryptBackupInPlace covers #1492 (compliance G13): a plaintext
// archive encrypted with a key file must be removed (never left behind
// alongside its encrypted copy) and must decrypt back to the original
// bytes via the same backupcrypt.DecryptFile password-restore uses.
func TestEncryptBackupInPlace(t *testing.T) {
	dir := t.TempDir()
	plainPath := filepath.Join(dir, "dansal-backup-20260101-030000.tar.gz")
	if err := os.WriteFile(plainPath, []byte("archive contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "backup.key")
	if err := os.WriteFile(keyFile, []byte("super-secret-key\n"), 0o400); err != nil {
		t.Fatal(err)
	}

	encPath, err := encryptBackupInPlace(plainPath, keyFile)
	if err != nil {
		t.Fatalf("encryptBackupInPlace: %v", err)
	}
	if encPath != plainPath+".enc" {
		t.Errorf("encPath = %q, want %q", encPath, plainPath+".enc")
	}
	if _, err := os.Stat(plainPath); !os.IsNotExist(err) {
		t.Errorf("plaintext %s should have been removed, stat err = %v", plainPath, err)
	}

	plain, err := backupcrypt.DecryptFile(encPath, []byte("super-secret-key"))
	if err != nil {
		t.Fatalf("DecryptFile: %v", err)
	}
	if string(plain) != "archive contents" {
		t.Errorf("decrypted = %q, want %q", plain, "archive contents")
	}
}

func TestEncryptBackupInPlaceRejectsEmptyKeyFile(t *testing.T) {
	dir := t.TempDir()
	plainPath := filepath.Join(dir, "dansal-backup-x.tar.gz")
	os.WriteFile(plainPath, []byte("x"), 0o600)
	keyFile := filepath.Join(dir, "empty.key")
	os.WriteFile(keyFile, []byte("   \n"), 0o400)

	if _, err := encryptBackupInPlace(plainPath, keyFile); err == nil {
		t.Error("expected error for an empty (whitespace-only) key file")
	}
}

func TestIsBackupArchiveName(t *testing.T) {
	cases := map[string]bool{
		"dansal-backup-20260101-030000.tar.gz":     true,
		"dansal-backup-20260101-030000.tar.gz.enc": true,
		"dansal-backup-20260101-030000.tar":        false,
		"readme.txt":                               false,
	}
	for name, want := range cases {
		if got := isBackupArchiveName(name); got != want {
			t.Errorf("isBackupArchiveName(%q) = %v, want %v", name, got, want)
		}
	}
}
