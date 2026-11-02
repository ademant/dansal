package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

// #1765: a bad backup key stops the service at load time, with the key named.
func TestValidateBackupConfig(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	ok := &Config{}
	ok.Server.BackupEncryptionPublicKey = StringList{id.Recipient().String()}
	if err := validateBackupConfig(ok); err != nil {
		t.Fatalf("valid config refused: %v", err)
	}
	if ok.Server.BackupMode != "full" {
		t.Fatalf("default mode = %q, want full", ok.Server.BackupMode)
	}

	badMode := &Config{}
	badMode.Server.BackupMode = "differential"
	if err := validateBackupConfig(badMode); err == nil || !strings.Contains(err.Error(), "backup_mode") {
		t.Fatalf("bad mode: %v", err)
	}

	badKey := &Config{}
	badKey.Server.BackupEncryptionPublicKey = StringList{"age1pq1qqqqqqqqqqqqqqqqqqqq"}
	if err := validateBackupConfig(badKey); err == nil || !strings.Contains(err.Error(), "backup_encryption_public_key") {
		t.Fatalf("bad recipient: %v", err)
	}
}

// The YAML key accepts one string or a list.
func TestStringListYAML(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "server:\n  backup_encryption_public_key: \"" + id.Recipient().String() + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Server.BackupEncryptionPublicKey) != 1 {
		t.Fatalf("single string: %v", cfg.Server.BackupEncryptionPublicKey)
	}
}
