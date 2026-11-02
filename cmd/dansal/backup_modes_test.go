package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/ademant/dansal/internal/backuparchive"
)

// modeTestEnv points the backup globals at a temp instance: a calendar DB,
// an images directory and a config.yaml in etc/. Returns the paths.
func modeTestEnv(t *testing.T) (root, backupDir, imagesDir string) {
	t.Helper()
	setupDedupTestDB(t)
	root = t.TempDir()
	imagesDir = filepath.Join(root, "images")
	backupDir = filepath.Join(root, "backups")
	etc := filepath.Join(root, "etc")
	if err := os.MkdirAll(etc, 0750); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(etc, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldCfg, oldPath := config, configFilePath
	t.Cleanup(func() { config, configFilePath = oldCfg, oldPath })
	configFilePath = cfgPath
	config = &Config{}
	config.Server.ImagesDir = imagesDir
	config.Server.BackupDir = backupDir
	config.Server.BackupMode = "full"
	return root, backupDir, imagesDir
}

func writeImage(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0640); err != nil {
		t.Fatal(err)
	}
}

func mustBackup(t *testing.T, o modeBackupOpts) backupResult {
	t.Helper()
	resp := createModeBackup(o)
	if !resp.OK {
		t.Fatalf("backup %+v: %s", o, resp.Error)
	}
	return resp.Data.(backupResult)
}

// imageNames lists images under dir, relative and sorted.
func imageNames(t *testing.T, dir string) []string {
	t.Helper()
	state, err := imageState(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rel := range state {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

func TestModeChainFullIncrementalCumulative(t *testing.T) {
	_, backupDir, imagesDir := modeTestEnv(t)
	writeImage(t, imagesDir, "a.avif", "a1")
	writeImage(t, imagesDir, "b.avif", "b1")

	full := mustBackup(t, modeBackupOpts{Mode: "full"})
	if full.Kind != "full" {
		t.Fatalf("kind = %q", full.Kind)
	}

	// Change a, delete b, add c.
	writeImage(t, imagesDir, "a.avif", "a2")
	os.Remove(filepath.Join(imagesDir, "b.avif"))
	writeImage(t, imagesDir, "c.avif", "c1")

	inc := mustBackup(t, modeBackupOpts{Mode: "incremental"})
	incM, err := backuparchive.ReadSidecar(inc.Path)
	if err != nil {
		t.Fatal(err)
	}
	if incM.Kind != backuparchive.KindIncremental || incM.Base != filepath.Base(full.Path) {
		t.Fatalf("incremental base = %q kind=%s", incM.Base, incM.Kind)
	}
	if strings.Join(incM.Deleted, ",") != "b.avif" {
		t.Fatalf("deleted = %v", incM.Deleted)
	}
	if !hasRecord(incM, "images/a.avif") || !hasRecord(incM, "images/c.avif") || hasRecord(incM, "images/b.avif") {
		t.Fatalf("incremental stores the wrong images: %+v", incM.Files)
	}

	// Cumulative: based on the full, so it stores a and c, and deletes b.
	cum := mustBackup(t, modeBackupOpts{Mode: "cumulative"})
	cumM, err := backuparchive.ReadSidecar(cum.Path)
	if err != nil {
		t.Fatal(err)
	}
	if cumM.Base != filepath.Base(full.Path) {
		t.Fatalf("cumulative base = %q, want the full %q", cumM.Base, filepath.Base(full.Path))
	}
	if cumM.ChainID != incM.ChainID || cumM.Sequence != 1 {
		t.Fatalf("cumulative chain=%s seq=%d, want chain=%s seq=1 (one hop from the full)", cumM.ChainID, cumM.Sequence, incM.ChainID)
	}
	if !hasRecord(cumM, "images/a.avif") || !hasRecord(cumM, "images/c.avif") {
		t.Fatalf("cumulative misses changed images: %+v", cumM.Files)
	}

	// Restoring the incremental chain reproduces the images exactly.
	chain, err := backuparchive.ResolveChain(backupDir, inc.Path)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, a := range chain {
		paths = append(paths, a.Path)
	}
	// Wipe the live images and restore from the chain.
	os.RemoveAll(imagesDir)
	res, err := restoreChain(paths, true, "")
	if err != nil {
		t.Fatalf("restoreChain: %v", err)
	}
	if res.Archives != 2 {
		t.Fatalf("archives = %d", res.Archives)
	}
	got := imageNames(t, imagesDir)
	if strings.Join(got, ",") != "a.avif,c.avif" {
		t.Fatalf("restored images = %v", got)
	}
	body, _ := os.ReadFile(filepath.Join(imagesDir, "a.avif"))
	if string(body) != "a2" {
		t.Fatalf("a.avif = %q, want the incremental's version", body)
	}
}

func hasRecord(m *backuparchive.Manifest, name string) bool {
	for _, f := range m.Files {
		if f.Name == name {
			return true
		}
	}
	return false
}

func TestModeIncrementalWithoutFullRefused(t *testing.T) {
	modeTestEnv(t)
	resp := createModeBackup(modeBackupOpts{Mode: "incremental"})
	if resp.OK || !strings.Contains(resp.Error, "full") {
		t.Fatalf("expected refusal naming the missing full, got ok=%v err=%q", resp.OK, resp.Error)
	}
	resp = createModeBackup(modeBackupOpts{Mode: "cumulative"})
	if resp.OK {
		t.Fatal("cumulative without a full was accepted")
	}
}

func TestModeConfigSelectsDefaultMode(t *testing.T) {
	_, _, imagesDir := modeTestEnv(t)
	writeImage(t, imagesDir, "a.avif", "a")
	config.Server.BackupMode = "incremental"
	resp := createModeBackup(modeBackupOpts{})
	if resp.OK {
		t.Fatal("incremental from config accepted without a full")
	}
	config.Server.BackupMode = "full"
	if got := mustBackup(t, modeBackupOpts{}); got.Kind != "full" {
		t.Fatalf("kind from config = %q", got.Kind)
	}
}

func TestModePublicKeyEncryptionAndPassphraseGuard(t *testing.T) {
	_, _, imagesDir := modeTestEnv(t)
	writeImage(t, imagesDir, "a.avif", "a")
	id, _ := age.GenerateX25519Identity()
	config.Server.BackupEncryptionPublicKey = StringList{id.Recipient().String()}

	res := mustBackup(t, modeBackupOpts{Mode: "full"})
	if !strings.HasSuffix(res.Path, ".tar.zst.age") {
		t.Fatalf("encrypted archive name = %s", res.Path)
	}
	plain := filepath.Join(t.TempDir(), "plain.tar.zst")
	if err := backuparchive.DecryptFile(res.Path, plain, id); err != nil {
		t.Fatalf("decrypt with the matching key: %v", err)
	}
	m, err := backuparchive.ReadManifestFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	if m.Credentials {
		t.Fatal("credentials kept without backup_include_credentials")
	}

	// A passphrase run is refused with a public key configured unless the
	// passphrase came from a file.
	resp := createModeBackup(modeBackupOpts{Mode: "full", Passphrase: "pw"})
	if resp.OK {
		t.Fatal("passphrase accepted alongside a public key without --passphrase-file")
	}
	ok := mustBackup(t, modeBackupOpts{Mode: "full", Passphrase: "pw", PassphraseFromFile: true})
	wrong, _ := backuparchive.PassphraseIdentity("other")
	if err := backuparchive.DecryptFile(ok.Path, filepath.Join(t.TempDir(), "x"), wrong); err == nil {
		t.Fatal("wrong passphrase decrypted the archive")
	}
}

func TestPruneKeepsWholeChains(t *testing.T) {
	_, backupDir, imagesDir := modeTestEnv(t)
	config.Server.BackupKeep = 1
	writeImage(t, imagesDir, "a.avif", "a")

	first := mustBackup(t, modeBackupOpts{Mode: "full"})
	mustBackup(t, modeBackupOpts{Mode: "incremental"})
	second := mustBackup(t, modeBackupOpts{Mode: "full"})
	mustBackup(t, modeBackupOpts{Mode: "incremental"})

	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Fatal("the old chain's full survived pruning")
	}
	all, err := backuparchive.ListArchives(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("kept %d archives, want the newest chain (full + incremental)", len(all))
	}
	for _, a := range all {
		if a.M.ChainID == "" || filepath.Base(second.Path) == "" {
			t.Fatal("bad chain state")
		}
	}
}

// A single incremental archive sent to the restore command must be expanded
// to its chain on the server; restoring it alone would drop images that are
// only in the full.
func TestRestoreSingleIncrementalResolvesChain(t *testing.T) {
	_, backupDir, imagesDir := modeTestEnv(t)
	writeImage(t, imagesDir, "a.avif", "a1")
	writeImage(t, imagesDir, "b.avif", "b1")
	mustBackup(t, modeBackupOpts{Mode: "full"})
	writeImage(t, imagesDir, "c.avif", "c1") // b and a stay in the full only
	inc := mustBackup(t, modeBackupOpts{Mode: "incremental"})

	resp := adminRestore(adminRequest{Path: inc.Path})
	if !resp.OK {
		t.Fatalf("restore: %s", resp.Error)
	}
	got := imageNames(t, imagesDir)
	if strings.Join(got, ",") != "a.avif,b.avif,c.avif" {
		t.Fatalf("images after restoring one incremental = %v (the full's images must survive)", got)
	}
	_ = backupDir
}

// An encrypted archive sent to the server restore is refused with a pointer
// to dansal_admin restore.
func TestRestoreRefusesEncryptedOnServer(t *testing.T) {
	_, backupDir, _ := modeTestEnv(t)
	p := filepath.Join(backupDir, "dansal-full-x.tar.zst.age")
	os.MkdirAll(backupDir, 0750)
	os.WriteFile(p, []byte("x"), 0600)
	resp := adminRestore(adminRequest{Path: p})
	if resp.OK || !strings.Contains(resp.Error, "dansal_admin restore") {
		t.Fatalf("encrypted restore not refused clearly: ok=%v err=%q", resp.OK, resp.Error)
	}
}

// A decrypted symmetric archive keeps no .tar.zst name, so the server must
// recognise it by content; and an incremental without its full is refused
// rather than applied.
func TestRestoreRecognisesZstdByContentAndRefusesBareIncremental(t *testing.T) {
	_, _, imagesDir := modeTestEnv(t)
	writeImage(t, imagesDir, "a.avif", "a1")
	full := mustBackup(t, modeBackupOpts{Mode: "full"})
	writeImage(t, imagesDir, "b.avif", "b1")
	inc := mustBackup(t, modeBackupOpts{Mode: "incremental"})

	renamed := filepath.Join(t.TempDir(), "decrypted-restore.tmp")
	data, _ := os.ReadFile(inc.Path)
	os.WriteFile(renamed, data, 0600)
	if !isZstdFile(renamed) {
		t.Fatal("zstd content not recognised")
	}
	resp := adminRestore(adminRequest{Paths: []string{renamed}})
	if resp.OK {
		t.Fatal("bare incremental restored without its full")
	}
	_ = full
}
