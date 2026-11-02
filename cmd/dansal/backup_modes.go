package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"gopkg.in/yaml.v2"

	"github.com/ademant/dansal/internal/backuparchive"
	"github.com/ademant/dansal/internal/backupcrypt"
	"github.com/ademant/dansal/internal/logging"
)

// Backup modes (#1765). A mode archive stores the calendar and web databases
// (always complete), the instance's config, nginx and systemd files, and the
// images: all of them for a full archive, only the changed ones for an
// incremental or cumulative archive. The manifest records the complete image
// state, so the next run can tell what changed without decrypting anything.

// modeBackupOpts are the caller's choices for one backup run. Empty fields
// fall back to the config.
type modeBackupOpts struct {
	Mode               string // full | incremental | cumulative; "" = backup_mode
	Dir                string // output directory; "" = backup_dir
	Passphrase         string // manual passphrase encryption (age scrypt)
	PassphraseFromFile bool   // the passphrase came from an explicit file
}

type encMode int

const (
	encNone       encMode = iota
	encAge                // public-key recipients
	encPassphrase         // manual passphrase (age scrypt)
	encSymmetric          // backup_encryption_key_file (AES-GCM, legacy format)
)

// adminModeBackup is the "backup" admin command.
func adminModeBackup(req adminRequest) adminResponse {
	return createModeBackup(modeBackupOpts{
		Mode:               req.Mode,
		Dir:                req.Path,
		Passphrase:         req.Passphrase,
		PassphraseFromFile: req.PassphraseFile,
	})
}

// backupDirPath is the configured backup directory, defaulting below
// /var/lib/dansal as the other backup commands do.
func backupDirPath() string {
	if config != nil && config.Server.BackupDir != "" {
		return config.Server.BackupDir
	}
	return "/var/lib/dansal/backups"
}

func failBackup(format string, args ...any) adminResponse {
	return adminResponse{OK: false, Error: fmt.Sprintf(format, args...)}
}

// createModeBackup writes one full, incremental or cumulative archive.
func createModeBackup(o modeBackupOpts) adminResponse {
	modeName := o.Mode
	if modeName == "" {
		modeName = config.Server.BackupMode
	}
	kind, err := backuparchive.ParseKind(modeName)
	if err != nil {
		return failBackup("%v", err)
	}
	dir := o.Dir
	if dir == "" {
		dir = backupDirPath()
	} else if strings.Contains(filepath.Base(dir), ".tar") {
		// A file name (the webmin form sends one): archives are named by mode
		// and time, so only its directory is used.
		logging.Warnf("backup: output %s is a file name; using its directory, the archive is named by mode and time", dir)
		dir = filepath.Dir(dir)
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return failBackup("mkdir: %v", err)
	}

	// The base archive for incremental (any kind) or cumulative (last full).
	var base *backuparchive.Archive
	switch kind {
	case backuparchive.KindIncremental:
		base, err = backuparchive.Latest(dir, "")
	case backuparchive.KindCumulative:
		base, err = backuparchive.Latest(dir, backuparchive.KindFull)
	}
	if err != nil {
		return failBackup("find base: %v", err)
	}
	if kind != backuparchive.KindFull && base == nil {
		return failBackup("no full backup in %s to base a %s backup on: run backup --mode full first", dir, kind)
	}

	// Encryption: passphrase (manual) > public key > symmetric key file > none.
	lines, err := publicRecipientLines(config)
	if err != nil {
		return failBackup("recipients: %v", err)
	}
	recips, infos, err := backuparchive.ParseRecipients(lines)
	if err != nil {
		return failBackup("recipients: %v", err)
	}
	enc := encNone
	switch {
	case o.Passphrase != "":
		if len(recips) > 0 && !o.PassphraseFromFile {
			return failBackup("a public key is configured: a passphrase backup needs an explicit --passphrase-file")
		}
		enc = encPassphrase
	case len(recips) > 0:
		enc = encAge
	case config.Server.BackupEncryptionKeyFile != "":
		enc = encSymmetric
	}
	includeCreds := enc == encAge && config.Server.BackupIncludeCredentials

	now := time.Now()
	stamp := now.Format("20060102-150405")
	name := fmt.Sprintf("dansal-%s-%s.tar.zst", kind, stamp)
	switch enc {
	case encAge, encPassphrase:
		name += ".age"
	case encSymmetric:
		name += ".enc"
	}
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("dansal-%s-%s-%d.tar.zst%s", kind, stamp, n, encSuffix(enc))
	}
	final := filepath.Join(dir, name)

	stage, err := os.MkdirTemp(dir, ".staging-*")
	if err != nil {
		return failBackup("staging dir: %v", err)
	}
	defer os.RemoveAll(stage)

	var files []backuparchive.FileRecord
	var entries []backuparchive.Entry
	var unreadable []string
	add := func(archiveName, src string) {
		sum, size, err := backuparchive.HashFile(src)
		if err != nil {
			unreadable = append(unreadable, src)
			logging.Warnf("backup: cannot read %s: %v", src, err)
			return
		}
		files = append(files, backuparchive.FileRecord{Name: archiveName, Size: size, SHA256: sum})
		entries = append(entries, backuparchive.Entry{Name: archiveName, Src: src})
	}

	// Databases: consistent VACUUM INTO snapshots, always complete.
	calSnap := filepath.Join(stage, "calendar.db")
	if err := snapshotCalendarDB(calSnap, !includeCreds); err != nil {
		return failBackup("calendar.db snapshot: %v", err)
	}
	add("calendar.db", calSnap)
	webPath, err := webDBPath()
	if err != nil {
		return failBackup("web.db: %v", err)
	}
	if webPath != "" {
		webSnap := filepath.Join(stage, "web.db")
		if err := snapshotSQLiteFile(webPath, webSnap); err != nil {
			return failBackup("web.db snapshot: %v", err)
		}
		add("web.db", webSnap)
	}

	// Config, nginx and systemd files.
	for _, f := range instanceConfigFiles() {
		add(f.name, f.src)
	}

	// Images: full state for the manifest, and the changed subset to store.
	state, err := imageState(config.Server.ImagesDir)
	if err != nil {
		return failBackup("images: %v", err)
	}
	current := make(map[string]string, len(state))
	var deleted []string
	for rel, img := range state {
		current[rel] = img.sum
		if kind == backuparchive.KindFull || base == nil || base.M.Images[rel] != img.sum {
			add("images/"+rel, filepath.Join(config.Server.ImagesDir, filepath.FromSlash(rel)))
		}
	}
	if kind != backuparchive.KindFull {
		for rel := range base.M.Images {
			if _, ok := state[rel]; !ok {
				deleted = append(deleted, rel)
			}
		}
	}

	m := &backuparchive.Manifest{
		Format:      backuparchive.FormatVersion,
		Kind:        kind,
		ChainID:     newChainID(),
		Archive:     name,
		Created:     now.UTC(),
		Credentials: includeCreds,
		Files:       files,
		Images:      current,
		Deleted:     deleted,
		Unreadable:  unreadable,
	}
	if base != nil {
		m.Base = filepath.Base(base.Path)
		m.ChainID = base.M.ChainID
		m.Sequence = base.M.Sequence + 1
	}

	partial := final + ".partial"
	if err := writeModeArchive(partial, stage, enc, o, recips, m, entries); err != nil {
		os.Remove(partial)
		return failBackup("write archive: %v", err)
	}
	if err := os.Rename(partial, final); err != nil {
		os.Remove(partial)
		return failBackup("finish archive: %v", err)
	}
	if err := backuparchive.WriteSidecar(final, m); err != nil {
		os.Remove(final)
		return failBackup("manifest copy: %v", err)
	}

	for _, info := range infos {
		logging.Debugf("backup: recipient %s fingerprint %s", info.Type, info.Fingerprint)
	}
	if enc == encNone {
		logging.Warnf("backup: %s written unencrypted; set server.backup_encryption_public_key", name)
	}
	if config.Server.BackupKeep > 0 {
		pruneChains(dir, config.Server.BackupKeep, filepath.Base(final))
	}

	info, _ := os.Stat(final)
	var size int64
	if info != nil {
		size = info.Size()
	}
	return adminResponse{OK: true, Data: backupResult{
		Path:        final,
		Size:        size,
		Kind:        string(kind),
		Incremental: kind != backuparchive.KindFull,
	}}
}

func encSuffix(enc encMode) string {
	switch enc {
	case encAge, encPassphrase:
		return ".age"
	case encSymmetric:
		return ".enc"
	}
	return ""
}

// writeModeArchive writes the archive to path with the chosen encryption.
func writeModeArchive(path, stage string, enc encMode, o modeBackupOpts, recips []age.Recipient, m *backuparchive.Manifest, entries []backuparchive.Entry) error {
	switch enc {
	case encAge:
		return writeAgeArchive(path, recips, m, entries)
	case encPassphrase:
		r, err := backuparchive.PassphraseRecipient(o.Passphrase)
		if err != nil {
			return err
		}
		return writeAgeArchive(path, []age.Recipient{r}, m, entries)
	case encSymmetric:
		plain := filepath.Join(stage, "archive.tar.zst")
		if err := writePlainArchive(plain, m, entries); err != nil {
			return err
		}
		key, err := os.ReadFile(config.Server.BackupEncryptionKeyFile)
		if err != nil {
			return fmt.Errorf("read backup_encryption_key_file: %w", err)
		}
		key = []byte(strings.TrimSpace(string(key)))
		if len(key) == 0 {
			return fmt.Errorf("backup_encryption_key_file is empty")
		}
		return backupcrypt.EncryptFile(plain, path, key)
	}
	return writePlainArchive(path, m, entries)
}

func writePlainArchive(path string, m *backuparchive.Manifest, entries []backuparchive.Entry) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
	if err != nil {
		return err
	}
	if err := backuparchive.WriteTar(f, m, entries); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeAgeArchive(path string, recips []age.Recipient, m *backuparchive.Manifest, entries []backuparchive.Entry) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
	if err != nil {
		return err
	}
	w, err := backuparchive.EncryptTo(f, recips...)
	if err != nil {
		f.Close()
		return err
	}
	if err := backuparchive.WriteTar(w, m, entries); err != nil {
		w.Close()
		f.Close()
		return err
	}
	if err := w.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// snapshotCalendarDB writes a consistent copy of the live calendar database,
// stripping password hashes and TOTP secrets unless keepCreds is set.
func snapshotCalendarDB(dst string, strip bool) error {
	if _, err := db.Exec("VACUUM INTO ?", dst); err != nil {
		return err
	}
	if !strip {
		return nil
	}
	snap, err := sql.Open("sqlite3", dst)
	if err != nil {
		return err
	}
	defer snap.Close()
	_, err = snap.Exec("UPDATE users SET password_hash = '', totp_secret = NULL")
	return err
}

// snapshotSQLiteFile writes a consistent copy of another SQLite database
// (web.db) without modifying it.
func snapshotSQLiteFile(src, dst string) error {
	ro, err := sql.Open("sqlite3", "file:"+src+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return err
	}
	defer ro.Close()
	_, err = ro.Exec("VACUUM INTO ?", dst)
	return err
}

// webDBPath reads db_path from web.yaml next to config.yaml. A missing
// web.yaml means this instance runs no web frontend: web.db is skipped (with a
// warning). A web.yaml without a readable db_path is an error.
func webDBPath() (string, error) {
	if configFilePath == "" {
		return "", fmt.Errorf("no config file path known")
	}
	webYAML := filepath.Join(filepath.Dir(configFilePath), "web.yaml")
	data, err := os.ReadFile(webYAML)
	if err != nil {
		if os.IsNotExist(err) {
			logging.Warnf("backup: no %s; web.db is not included", webYAML)
			return "", nil
		}
		return "", err
	}
	var w struct {
		DBPath string `yaml:"db_path"`
	}
	if err := yaml.Unmarshal(data, &w); err != nil {
		return "", fmt.Errorf("%s: %w", webYAML, err)
	}
	if w.DBPath == "" {
		return "", fmt.Errorf("%s has no db_path", webYAML)
	}
	if _, err := os.Stat(w.DBPath); err != nil {
		return "", fmt.Errorf("web.db: %w", err)
	}
	return w.DBPath, nil
}

type archiveFile struct{ name, src string }

// instanceConfigFiles lists the config, nginx and systemd files of this
// instance. The encryption key file and private keys are never included.
func instanceConfigFiles() []archiveFile {
	var out []archiveFile
	confDir := filepath.Dir(configFilePath)
	instance := filepath.Base(confDir)
	keyFile := ""
	if config != nil {
		keyFile = config.Server.BackupEncryptionKeyFile
	}
	for _, n := range []string{"config.yaml", "web.yaml", "webmin.yaml"} {
		src := filepath.Join(confDir, n)
		if src == keyFile || strings.HasSuffix(n, ".key") {
			continue
		}
		out = append(out, archiveFile{"config/" + n, src})
	}
	for _, src := range []string{
		"/etc/nginx/conf.d/dansal-" + instance + ".conf",
		"/etc/nginx/conf.d/dansal-webmin-" + instance + ".conf",
		"/etc/nginx/conf.d/dansal-doc-" + instance + ".conf",
		"/etc/nginx/conf.d/00-dansal-log-formats.conf",
		"/etc/nginx/brotli/http.d/brotli.conf",
	} {
		out = append(out, archiveFile{"nginx/" + filepath.Base(src), src})
	}
	units, _ := filepath.Glob("/etc/systemd/system/dansal*.service")
	timers, _ := filepath.Glob("/etc/systemd/system/dansal*.timer")
	dropins, _ := filepath.Glob("/etc/systemd/system/dansal*@" + instance + ".service.d/*.conf")
	for _, src := range append(append(units, timers...), dropins...) {
		rel := filepath.Base(src)
		if filepath.Base(filepath.Dir(src)) != "system" {
			rel = filepath.Base(filepath.Dir(src)) + "/" + rel
		}
		out = append(out, archiveFile{"systemd/" + rel, src})
	}
	// Optional files that are not there are skipped; add() reports the ones
	// that exist but cannot be read.
	var present []archiveFile
	for _, f := range out {
		if _, err := os.Stat(f.src); err == nil {
			present = append(present, f)
		}
	}
	return present
}

// imageInfo is the checksum and size of one stored image.
type imageInfo struct {
	sum  string
	size int64
}

// imageState maps every image under dir (relative, slash-separated path) to
// its checksum and size.
func imageState(dir string) (map[string]imageInfo, error) {
	state := map[string]imageInfo{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		sum, size, err := backuparchive.HashFile(path)
		if err != nil {
			return err
		}
		state[filepath.ToSlash(rel)] = imageInfo{sum: sum, size: size}
		return nil
	})
	return state, err
}

// newChainID returns a random identifier for a full backup and its dependents.
func newChainID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// pruneChains keeps the newest keep chains (a full backup plus every archive
// that depends on it) and deletes the rest, archives and manifest copies.
// The chain of justWritten is always kept.
func pruneChains(dir string, keep int, justWritten string) {
	all, err := backuparchive.ListArchives(dir)
	if err != nil {
		logging.Errorf("backup: list for prune: %v", err)
		return
	}
	ids := backuparchive.ChainIDs(all)
	if len(ids) <= keep {
		return
	}
	protected := map[string]bool{}
	for i := 0; i < keep && i < len(ids); i++ {
		protected[ids[i]] = true
	}
	for _, a := range all {
		if filepath.Base(a.Path) == justWritten {
			protected[a.M.ChainID] = true
		}
	}
	removed := 0
	for _, a := range all {
		if protected[a.M.ChainID] {
			continue
		}
		if os.Remove(a.Path) == nil {
			removed++
		}
		os.Remove(backuparchive.SidecarPath(a.Path))
	}
	if removed > 0 {
		logging.Debugf("backup: pruned %d archive(s) of %d old chain(s)", removed, len(ids)-keep)
	}
}

// manifestFromTar decodes a manifest.json stream.
func manifestFromTar(r io.Reader) (*backuparchive.Manifest, error) {
	var m backuparchive.Manifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}
