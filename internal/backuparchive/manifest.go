// Package backuparchive builds, reads and encrypts dansal backup archives
// for #1765: a full, incremental or cumulative archive is a zstd-compressed
// tar stream whose first entry is manifest.json. The manifest records the
// complete image state at backup time, so the next incremental or cumulative
// run can decide what changed without decrypting anything.
package backuparchive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"
)

// FormatVersion is the manifest format written by this package.
const FormatVersion = 1

// ManifestName is the archive entry holding the manifest.
const ManifestName = "manifest.json"

// Kind is what a backup archive covers relative to its base.
type Kind string

const (
	KindFull        Kind = "full"        // everything; starts a new chain
	KindIncremental Kind = "incremental" // changes since the previous archive of any kind
	KindCumulative  Kind = "cumulative"  // changes since the last full
)

// ParseKind accepts the three mode names used by the CLI and config.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case KindFull, KindIncremental, KindCumulative:
		return Kind(s), nil
	}
	return "", fmt.Errorf("unknown backup mode %q (want full, incremental or cumulative)", s)
}

// FileRecord describes one file stored in an archive.
type FileRecord struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest is stored as manifest.json inside every archive and, unencrypted,
// next to it as <archive>.manifest.json so chains can be resolved without
// the decryption key.
type Manifest struct {
	Format   int       `json:"format"`
	Kind     Kind      `json:"kind"`
	ChainID  string    `json:"chain_id"`
	Sequence int       `json:"sequence"`       // 0 for the full, +1 per archive in the chain
	Base     string    `json:"base,omitempty"` // file name of the base archive; empty for a full
	Archive  string    `json:"archive"`        // file name of this archive (set when written)
	Created  time.Time `json:"created"`
	Version  string    `json:"version,omitempty"`
	// Credentials is true when calendar.db keeps password hashes and TOTP
	// secrets (public-key encrypted archives with backup_include_credentials).
	Credentials bool `json:"credentials"`
	// Files lists what this archive stores: DBs, config, nginx, systemd, and
	// only the images that changed since the base (all images for a full).
	Files []FileRecord `json:"files"`
	// Images is the complete image state at backup time: image path relative
	// to images/ → sha256. It is the diff base for the next run.
	Images map[string]string `json:"images"`
	// Deleted lists images present in the base but gone now.
	Deleted []string `json:"deleted,omitempty"`
	// Unreadable lists optional files that exist but could not be read.
	// They are reported rather than silently dropped.
	Unreadable []string `json:"unreadable,omitempty"`
}

// HashFile returns the sha256 (hex) and size of a file on disk.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
