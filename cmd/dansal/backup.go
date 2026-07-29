package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

type backupResult struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	Incremental bool   `json:"incremental"`
}

// adminBackup and adminIncrementalBackup always strip credentials — they
// never honor a caller-supplied flag. Only adminBackupWithCredentials
// (reachable exclusively via the dedicated "backup-with-credentials" admin
// command) passes keepCredentials=true, so a plaintext credentials-included
// backup can never be produced by requesting "backup" with some field set.
func adminBackup(req adminRequest) adminResponse {
	return createBackup(req.Path, time.Time{}, false)
}

func adminIncrementalBackup(req adminRequest) adminResponse {
	if req.Since == "" {
		return adminResponse{OK: false, Error: "--since is required"}
	}
	since, err := time.Parse(time.RFC3339, req.Since)
	if err != nil {
		return adminResponse{OK: false, Error: "invalid since time: " + err.Error()}
	}
	return createBackup(req.Path, since, false)
}

// adminBackupWithCredentials is used only by cmdPasswordBackup, which
// immediately encrypts the resulting plaintext archive client-side and
// deletes the unencrypted temp file — see cmdPasswordBackup for the full
// handoff. This is the only path allowed to include password_hash/totp_secret.
func adminBackupWithCredentials(req adminRequest) adminResponse {
	return createBackup(req.Path, time.Time{}, true)
}

// resolveBackupPath returns a full file path for the backup archive.
// If outputPath is empty or a directory, a timestamped filename is generated
// inside the configured backup_dir (empty) or the given directory.
func resolveBackupPath(outputPath string, incremental bool) string {
	kind := "backup"
	if incremental {
		kind = "incremental"
	}
	filename := fmt.Sprintf("dansal-%s-%s.tar.gz", kind, time.Now().Format("20060102-150405"))

	if outputPath == "" {
		dir := "/var/lib/dansal/backups"
		if config != nil && config.Server.BackupDir != "" {
			dir = config.Server.BackupDir
		}
		return filepath.Join(dir, filename)
	}

	// If it looks like a directory (trailing slash or existing dir), put file inside.
	if strings.HasSuffix(outputPath, "/") || strings.HasSuffix(outputPath, string(os.PathSeparator)) {
		return filepath.Join(outputPath, filename)
	}
	if info, err := os.Stat(outputPath); err == nil && info.IsDir() {
		return filepath.Join(outputPath, filename)
	}
	return outputPath
}

func createBackup(outputPath string, since time.Time, keepCredentials bool) adminResponse {
	incremental := !since.IsZero()
	outputPath = resolveBackupPath(outputPath, incremental)

	// Consistent DB snapshot via VACUUM INTO a temp file.
	tmpDB, err := os.CreateTemp("", "dansal-db-*.db")
	if err != nil {
		return adminResponse{OK: false, Error: "temp file: " + err.Error()}
	}
	tmpDB.Close()
	defer os.Remove(tmpDB.Name())

	if _, err := db.Exec("VACUUM INTO ?", tmpDB.Name()); err != nil {
		return adminResponse{OK: false, Error: "db snapshot: " + err.Error()}
	}

	// Remove credential secrets (password hash, TOTP seed) from the
	// snapshot so plaintext backups never contain them. Fail closed: if we
	// can't open the snapshot to strip them, abort rather than silently
	// shipping an archive with credentials still in it.
	if !keepCredentials {
		snapDB, err := sql.Open("sqlite3", tmpDB.Name())
		if err != nil {
			return adminResponse{OK: false, Error: "credential strip: " + err.Error()}
		}
		_, err = snapDB.Exec("UPDATE users SET password_hash = '', totp_secret = NULL")
		snapDB.Close()
		if err != nil {
			return adminResponse{OK: false, Error: "credential strip: " + err.Error()}
		}
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0750); err != nil {
		return adminResponse{OK: false, Error: "mkdir: " + err.Error()}
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return adminResponse{OK: false, Error: "create archive: " + err.Error()}
	}

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	// Database snapshot is always included.
	archiveErr := addFileToTar(tw, tmpDB.Name(), "calendar.db")

	// Images — all for full backup, only changed files for incremental.
	if archiveErr == nil {
		archiveErr = addDirToTar(tw, config.Server.ImagesDir, "images", since)
	}

	tw.Close()
	gz.Close()
	f.Close()

	if archiveErr != nil {
		os.Remove(outputPath)
		return adminResponse{OK: false, Error: archiveErr.Error()}
	}

	info, _ := os.Stat(outputPath)
	var size int64
	if info != nil {
		size = info.Size()
	}

	return adminResponse{OK: true, Data: backupResult{
		Path:        outputPath,
		Size:        size,
		Incremental: incremental,
	}}
}

type configBackupResult struct {
	Path  string   `json:"path"`
	Size  int64    `json:"size"`
	Files []string `json:"files"`
}

func adminConfigBackup(req adminRequest) adminResponse {
	return createConfigBackup(req.Path)
}

// resolveConfigBackupPath mirrors resolveBackupPath but with its own
// filename prefix, so config-backup archives never collide with business
// data ones in the same directory.
func resolveConfigBackupPath(outputPath string) string {
	filename := fmt.Sprintf("dansal-config-backup-%s.tar.gz", time.Now().Format("20060102-150405"))
	if outputPath == "" {
		dir := "/var/lib/dansal/backups"
		if config != nil && config.Server.BackupDir != "" {
			dir = config.Server.BackupDir
		}
		return filepath.Join(dir, filename)
	}
	if strings.HasSuffix(outputPath, "/") || strings.HasSuffix(outputPath, string(os.PathSeparator)) {
		return filepath.Join(outputPath, filename)
	}
	if info, err := os.Stat(outputPath); err == nil && info.IsDir() {
		return filepath.Join(outputPath, filename)
	}
	return outputPath
}

// createConfigBackup packages deployment/reproducibility data — config files
// and this instance's nginx vhost configs — so a crashed server can be
// rebuilt on fresh hardware/OS. It deliberately excludes business data
// (calendar.db, images) and the Let's Encrypt certificate, which is
// re-issued against the restored nginx config rather than carried in the
// backup. Restoring one produces a working but empty instance: no users, no
// events — those come from a separate business-data restore.
func createConfigBackup(outputPath string) adminResponse {
	if configFilePath == "" {
		return adminResponse{OK: false, Error: "no config file path known"}
	}
	outputPath = resolveConfigBackupPath(outputPath)

	if err := os.MkdirAll(filepath.Dir(outputPath), 0750); err != nil {
		return adminResponse{OK: false, Error: "mkdir: " + err.Error()}
	}
	f, err := os.Create(outputPath)
	if err != nil {
		return adminResponse{OK: false, Error: "create archive: " + err.Error()}
	}

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	confDir := filepath.Dir(configFilePath)
	instance := filepath.Base(confDir)

	candidates := []struct{ src, name string }{
		{configFilePath, "config.yaml"},
		{filepath.Join(confDir, "web.yaml"), "web.yaml"},
		{filepath.Join(confDir, "webmin.yaml"), "webmin.yaml"},
		{"/etc/nginx/conf.d/dansal-" + instance + ".conf", "nginx/dansal.conf"},
		{"/etc/nginx/conf.d/dansal-webmin-" + instance + ".conf", "nginx/dansal-webmin.conf"},
		{"/etc/nginx/conf.d/dansal-doc-" + instance + ".conf", "nginx/dansal-doc.conf"},
	}

	var included []string
	var archiveErr error
	for _, c := range candidates {
		if _, statErr := os.Stat(c.src); statErr != nil {
			continue // optional file, not present for this instance
		}
		if archiveErr = addFileToTar(tw, c.src, c.name); archiveErr != nil {
			break
		}
		included = append(included, c.name)
	}

	tw.Close()
	gz.Close()
	f.Close()

	if archiveErr != nil {
		os.Remove(outputPath)
		return adminResponse{OK: false, Error: archiveErr.Error()}
	}

	info, _ := os.Stat(outputPath)
	var size int64
	if info != nil {
		size = info.Size()
	}
	return adminResponse{OK: true, Data: configBackupResult{Path: outputPath, Size: size, Files: included}}
}

func addFileToTar(tw *tar.Writer, srcPath, name string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = name

	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func adminRestore(req adminRequest) adminResponse {
	if req.Path == "" {
		return adminResponse{OK: false, Error: "path is required"}
	}
	path := req.Path
	// If only a filename was given (no directory component), resolve it against backup_dir.
	if !strings.ContainsAny(path, "/\\") {
		dir := "/var/lib/dansal/backups"
		if config != nil && config.Server.BackupDir != "" {
			dir = config.Server.BackupDir
		}
		path = filepath.Join(dir, path)
	}
	restored, err := restoreFromTar(path, req.WipeCredentials)
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	if restored.Config && configFilePath != "" {
		reloadConfig(configFilePath)
	}
	return adminResponse{OK: true, Data: restored}
}

type restoreResult struct {
	Config         bool `json:"config"`
	DB             bool `json:"db"`
	Images         int  `json:"images"`
	PreservedUsers int  `json:"preserved_users"`
}

// userSnapshotRow holds one pre-restore live users row, generically — every
// column is captured so restoreLiveUserCredentials can restore the whole
// row, not just password_hash, without needing to track the users schema
// here as it evolves.
type userSnapshotRow struct {
	id      int64
	email   sql.NullString
	columns []string
	values  []any
}

// snapshotLiveUsers captures the live users table before a restore swaps
// the database file out from under it, so existing accounts can be
// preserved afterward unless the caller opted into wiping credentials.
func snapshotLiveUsers() ([]userSnapshotRow, error) {
	rows, err := db.Query("SELECT * FROM users")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	idIdx, emailIdx := -1, -1
	for i, c := range cols {
		switch c {
		case "id":
			idIdx = i
		case "email":
			emailIdx = i
		}
	}
	if idIdx < 0 {
		return nil, fmt.Errorf("users table has no id column")
	}

	var snapshot []userSnapshotRow
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}

		id, _ := vals[idIdx].(int64)
		var email sql.NullString
		if emailIdx >= 0 {
			if s, ok := vals[emailIdx].(string); ok {
				email = sql.NullString{String: s, Valid: true}
			}
		}

		var ucols []string
		var uvals []any
		for i, c := range cols {
			if i == idIdx {
				continue
			}
			ucols = append(ucols, c)
			uvals = append(uvals, vals[i])
		}
		snapshot = append(snapshot, userSnapshotRow{id: id, email: email, columns: ucols, values: uvals})
	}
	return snapshot, rows.Err()
}

// restoreLiveUserCredentials writes each pre-restore live user row back over
// the just-restored data, so a routine restore never silently locks
// existing accounts out. Matched by id first, falling back to email in case
// ids shifted between the live and restored databases. Users present live
// before the restore but absent afterward (deleted in the backup) are left
// alone — only genuinely new users from the backup are ever inserted, and
// insertion itself is just "don't touch what restoreDB already added".
func restoreLiveUserCredentials(snapshot []userSnapshotRow) (int, error) {
	preserved := 0
	for _, u := range snapshot {
		targetID := u.id
		var exists int
		db.QueryRow("SELECT COUNT(*) FROM users WHERE id=?", targetID).Scan(&exists)
		if exists == 0 && u.email.Valid && u.email.String != "" {
			var altID int64
			if err := db.QueryRow("SELECT id FROM users WHERE email=?", u.email.String).Scan(&altID); err == nil {
				targetID = altID
				exists = 1
			}
		}
		if exists == 0 {
			continue
		}

		setClause := make([]string, len(u.columns))
		args := make([]any, 0, len(u.values)+1)
		for i, c := range u.columns {
			setClause[i] = c + " = ?"
			args = append(args, u.values[i])
		}
		args = append(args, targetID)

		query := "UPDATE users SET " + strings.Join(setClause, ", ") + " WHERE id = ?"
		if _, err := db.Exec(query, args...); err != nil {
			return preserved, err
		}
		preserved++
	}
	return preserved, nil
}

func restoreFromTar(tarPath string, wipeCredentials bool) (restoreResult, error) {
	var result restoreResult

	var liveUsers []userSnapshotRow
	if !wipeCredentials {
		var err error
		liveUsers, err = snapshotLiveUsers()
		if err != nil {
			return result, fmt.Errorf("snapshot live users: %w", err)
		}
	}

	f, err := os.Open(tarPath)
	if err != nil {
		return result, err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return result, err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	var dbRestorePath string

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, err
		}

		switch {
		case hdr.Name == "config.yaml":
			if configFilePath == "" {
				continue
			}
			if err := extractToFile(tr, configFilePath, hdr.FileInfo().Mode()); err != nil {
				return result, fmt.Errorf("restore config: %w", err)
			}
			result.Config = true

		case hdr.Name == "calendar.db":
			tmp, err := os.CreateTemp("", "dansal-restore-*.db")
			if err != nil {
				return result, fmt.Errorf("temp db: %w", err)
			}
			if _, err := io.Copy(tmp, tr); err != nil {
				tmp.Close()
				os.Remove(tmp.Name())
				return result, fmt.Errorf("extract db: %w", err)
			}
			tmp.Close()
			dbRestorePath = tmp.Name()

		case strings.HasPrefix(hdr.Name, "images/"):
			rel := strings.TrimPrefix(hdr.Name, "images/")
			if rel == "" || hdr.Typeflag == tar.TypeDir {
				continue
			}
			dest := filepath.Join(config.Server.ImagesDir, rel)
			if err := os.MkdirAll(filepath.Dir(dest), 0750); err != nil {
				return result, fmt.Errorf("mkdir for image: %w", err)
			}
			if err := extractToFile(tr, dest, hdr.FileInfo().Mode()); err != nil {
				return result, fmt.Errorf("restore image %s: %w", rel, err)
			}
			result.Images++
		}
	}

	if dbRestorePath != "" {
		defer os.Remove(dbRestorePath)
		if err := restoreDB(dbRestorePath); err != nil {
			return result, fmt.Errorf("restore db: %w", err)
		}
		result.DB = true

		if !wipeCredentials {
			preserved, err := restoreLiveUserCredentials(liveUsers)
			if err != nil {
				return result, fmt.Errorf("preserve live user credentials: %w", err)
			}
			result.PreservedUsers = preserved
		}
	}

	return result, nil
}

func extractToFile(r io.Reader, destPath string, mode os.FileMode) error {
	f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

// restoreDB uses SQLite's online backup API to replace the live database
// contents with those from srcPath without interrupting other connections.
func restoreDB(srcPath string) error {
	srcDB, err := sql.Open("sqlite3", srcPath)
	if err != nil {
		return err
	}
	defer srcDB.Close()

	srcConn, err := srcDB.Conn(context.Background())
	if err != nil {
		return err
	}
	defer srcConn.Close()

	destConn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer destConn.Close()

	return srcConn.Raw(func(srcRaw any) error {
		src, ok := srcRaw.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("unexpected driver type")
		}
		return destConn.Raw(func(destRaw any) error {
			dst, ok := destRaw.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("unexpected driver type")
			}
			bk, err := dst.Backup("main", src, "main")
			if err != nil {
				return err
			}
			if _, err = bk.Step(-1); err != nil {
				return err
			}
			return bk.Finish()
		})
	})
}

type backupFileInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

func listBackups() ([]backupFileInfo, error) {
	dir := "/var/lib/dansal/backups"
	if config != nil && config.Server.BackupDir != "" {
		dir = config.Server.BackupDir
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []backupFileInfo{}, nil
		}
		return nil, err
	}
	var files []backupFileInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, backupFileInfo{
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})
	return files, nil
}

func adminListBackups(_ adminRequest) adminResponse {
	files, err := listBackups()
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	return adminResponse{OK: true, Data: files}
}

func startScheduledBackup() {
	if config == nil || config.Server.BackupIntervalHours <= 0 {
		return
	}
	interval := time.Duration(config.Server.BackupIntervalHours) * time.Hour
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			resp := createBackup("", time.Time{}, false)
			if resp.OK {
				if r, ok := resp.Data.(backupResult); ok {
					log.Printf("scheduled backup: %s (%s)", r.Path, fmtSize(r.Size))
				}
			} else {
				log.Printf("scheduled backup failed: %s", resp.Error)
			}
		}
	}()
}

func fmtSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func addDirToTar(tw *tar.Writer, srcDir, prefix string, since time.Time) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !since.IsZero() && !info.ModTime().After(since) {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		return addFileToTar(tw, path, filepath.Join(prefix, rel))
	})
}
