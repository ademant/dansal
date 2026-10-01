package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// touchBackupFile creates an empty file at dir/name and sets its mtime to
// make retention ordering deterministic (files created in quick succession
// in a test can otherwise land on the same mtime).
func touchBackupFile(t *testing.T, dir, name string, age time.Duration) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Now().Add(-age)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// TestPruneBackups covers #1407: retention is counted per kind (so a burst
// of incremental backups can't evict full backups or vice versa), only
// files matching the three generated prefixes are ever touched, and the
// newest `keep` of each kind survive.
func TestPruneBackups(t *testing.T) {
	dir := t.TempDir()

	// 5 full backups, ages 0..4 days (oldest last).
	for i := 0; i < 5; i++ {
		touchBackupFile(t, dir, "dansal-backup-"+string(rune('a'+i))+".tar.gz", time.Duration(i)*24*time.Hour)
	}
	// 4 incrementals, ages 0..3 days.
	for i := 0; i < 4; i++ {
		touchBackupFile(t, dir, "dansal-incremental-"+string(rune('a'+i))+".tar.gz", time.Duration(i)*24*time.Hour)
	}
	// 3 config backups, ages 0..2 days.
	for i := 0; i < 3; i++ {
		touchBackupFile(t, dir, "dansal-config-backup-"+string(rune('a'+i))+".tar.gz", time.Duration(i)*24*time.Hour)
	}
	// Foreign files: never touched regardless of name/extension.
	touchBackupFile(t, dir, "pre-deploy-20260101.tar.gz", 365*24*time.Hour)
	touchBackupFile(t, dir, "copy-to-test-20260101.db", 365*24*time.Hour)

	pruneBackups(dir, 2, "")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	remaining := map[string]bool{}
	for _, e := range entries {
		remaining[e.Name()] = true
	}

	// Full backups: newest 2 of 5 survive (ages 0, 1 day -> index a, b).
	for _, name := range []string{"dansal-backup-a.tar.gz", "dansal-backup-b.tar.gz"} {
		if !remaining[name] {
			t.Errorf("expected %s to survive (newest 2 kept), it was removed", name)
		}
	}
	for _, name := range []string{"dansal-backup-c.tar.gz", "dansal-backup-d.tar.gz", "dansal-backup-e.tar.gz"} {
		if remaining[name] {
			t.Errorf("expected %s to be pruned (beyond the newest 2), it survived", name)
		}
	}

	// Incrementals: newest 2 of 4 survive.
	for _, name := range []string{"dansal-incremental-a.tar.gz", "dansal-incremental-b.tar.gz"} {
		if !remaining[name] {
			t.Errorf("expected %s to survive, it was removed", name)
		}
	}
	for _, name := range []string{"dansal-incremental-c.tar.gz", "dansal-incremental-d.tar.gz"} {
		if remaining[name] {
			t.Errorf("expected %s to be pruned, it survived", name)
		}
	}

	// Config backups: only 3 existed, keep=2 -> 1 pruned, 2 survive.
	for _, name := range []string{"dansal-config-backup-a.tar.gz", "dansal-config-backup-b.tar.gz"} {
		if !remaining[name] {
			t.Errorf("expected %s to survive, it was removed", name)
		}
	}
	if remaining["dansal-config-backup-c.tar.gz"] {
		t.Error("expected dansal-config-backup-c.tar.gz to be pruned, it survived")
	}

	// Foreign files are never candidates for deletion, regardless of age.
	if !remaining["pre-deploy-20260101.tar.gz"] {
		t.Error("foreign .tar.gz file must never be pruned")
	}
	if !remaining["copy-to-test-20260101.db"] {
		t.Error("foreign non-.tar.gz file must never be pruned")
	}
}

// TestPruneBackupsKeepZeroRemovesNothing covers the explicit backup_keep: 0
// = keep-everything default from #1407.
func TestPruneBackupsKeepZeroRemovesNothing(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		touchBackupFile(t, dir, "dansal-backup-"+string(rune('a'+i))+".tar.gz", time.Duration(i)*24*time.Hour)
	}

	pruneBackups(dir, 0, "")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Errorf("keep=0 must remove nothing, got %d files remaining, want 5", len(entries))
	}
}

// TestPruneBackupsNeverDeletesJustWritten covers the "never delete the
// archive that was just written" guarantee, even in the pathological case
// where it isn't the one with the newest mtime (e.g. a clock oddity).
func TestPruneBackupsNeverDeletesJustWritten(t *testing.T) {
	dir := t.TempDir()
	// Two existing, older archives plus the "just written" one, backdated as
	// if its mtime were somehow not the newest -- keep=1 would otherwise
	// prune it.
	touchBackupFile(t, dir, "dansal-backup-old1.tar.gz", 2*24*time.Hour)
	touchBackupFile(t, dir, "dansal-backup-old2.tar.gz", 1*24*time.Hour)
	touchBackupFile(t, dir, "dansal-backup-just-written.tar.gz", 3*24*time.Hour)

	pruneBackups(dir, 1, "dansal-backup-just-written.tar.gz")

	if _, err := os.Stat(filepath.Join(dir, "dansal-backup-just-written.tar.gz")); err != nil {
		t.Error("the just-written archive must never be pruned, regardless of its mtime")
	}
}

// TestPruneBackupsIfConfiguredSkipsOutsideBackupDir covers the "explicit
// --output PATH outside backup_dir -> no pruning" guard from #1407.
func TestPruneBackupsIfConfiguredSkipsOutsideBackupDir(t *testing.T) {
	old := config
	defer func() { config = old }()

	backupDir := t.TempDir()
	elsewhere := t.TempDir()
	config = &Config{Server: ServerConfig{BackupDir: backupDir, BackupKeep: 1}}

	for i := 0; i < 3; i++ {
		touchBackupFile(t, elsewhere, "dansal-backup-"+string(rune('a'+i))+".tar.gz", time.Duration(i)*24*time.Hour)
	}

	pruneBackupsIfConfigured(filepath.Join(elsewhere, "dansal-backup-a.tar.gz"))

	entries, err := os.ReadDir(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("a write outside backup_dir must never be pruned, got %d files remaining, want 3", len(entries))
	}
}
