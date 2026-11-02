package main

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ademant/dansal/internal/backuparchive"
	"github.com/ademant/dansal/internal/logging"
)

// isChainArchive reports whether path is a decrypted #1765 archive (zstd tar),
// as opposed to a legacy tar.gz.
func isChainArchive(path string) bool {
	return strings.HasSuffix(path, ".tar.zst")
}

// isZstdFile reports whether the file starts with the zstd frame magic. A
// decrypted symmetric (.enc) archive has no .tar.zst name but is still zstd.
func isZstdFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return false
	}
	return magic[0] == 0x28 && magic[1] == 0xB5 && magic[2] == 0x2F && magic[3] == 0xFD
}

// restoreChain applies a backup chain: paths are the decrypted archives,
// oldest first (full, then its incrementals or the cumulative). Images are
// extracted from every archive in order, then removed unless the final
// manifest lists them, so the image directory ends up exactly as it was at
// the last backup. The databases and config files come from the last archive
// only, because every archive carries complete copies of them.
//
// Config, nginx and systemd files are never written into /etc here: they are
// extracted into stageDir when one is given, and listed as skipped otherwise.
func restoreChain(paths []string, wipeCredentials bool, stageDir string) (restoreResult, error) {
	var result restoreResult
	result.Archives = len(paths)

	var liveUsers []userSnapshotRow
	if !wipeCredentials {
		var err error
		liveUsers, err = snapshotLiveUsers()
		if err != nil {
			return result, fmt.Errorf("snapshot live users: %w", err)
		}
	}

	// The chain must start with a full: an incremental or cumulative applied
	// without its base would remove every image that is not in it.
	first, err := backuparchive.ReadManifestFile(paths[0])
	if err != nil {
		return result, fmt.Errorf("%s: %w", filepath.Base(paths[0]), err)
	}
	if first.Kind != backuparchive.KindFull {
		return result, fmt.Errorf("%s is a %s backup: restore its full backup first (or restore the directory so the chain is resolved)", filepath.Base(paths[0]), first.Kind)
	}

	var finalManifest *backuparchive.Manifest
	var dbPath string
	defer func() {
		if dbPath != "" {
			os.Remove(dbPath)
		}
	}()

	for i, p := range paths {
		last := i == len(paths)-1
		err := backuparchive.ReadPlain(p, func(hdr *tar.Header, tr *tar.Reader) error {
			name := hdr.Name
			switch {
			case name == backuparchive.ManifestName:
				if last {
					m, err := manifestFromTar(tr)
					if err != nil {
						return fmt.Errorf("manifest: %w", err)
					}
					finalManifest = m
				}
				return nil

			case strings.HasPrefix(name, "images/"):
				rel := strings.TrimPrefix(name, "images/")
				if rel == "" || !filepath.IsLocal(filepath.FromSlash(rel)) || hdr.Typeflag == tar.TypeDir {
					return nil
				}
				dest := filepath.Join(config.Server.ImagesDir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(dest), 0750); err != nil {
					return fmt.Errorf("mkdir for image: %w", err)
				}
				if err := extractToFile(tr, dest, hdr.FileInfo().Mode()); err != nil {
					return fmt.Errorf("restore image %s: %w", rel, err)
				}
				result.Images++
				return nil

			case !last:
				return nil // DBs and config come from the last archive only

			case name == "calendar.db":
				tmp, err := os.CreateTemp("", "dansal-restore-*.db")
				if err != nil {
					return fmt.Errorf("temp db: %w", err)
				}
				if _, err := io.Copy(tmp, tr); err != nil {
					tmp.Close()
					os.Remove(tmp.Name())
					return fmt.Errorf("extract db: %w", err)
				}
				tmp.Close()
				dbPath = tmp.Name()
				return nil

			default:
				if stageDir == "" || !filepath.IsLocal(filepath.FromSlash(name)) {
					result.Skipped = append(result.Skipped, name)
					return nil
				}
				dest := filepath.Join(stageDir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(dest), 0750); err != nil {
					return fmt.Errorf("mkdir for %s: %w", name, err)
				}
				if err := extractToFile(tr, dest, hdr.FileInfo().Mode()); err != nil {
					return fmt.Errorf("stage %s: %w", name, err)
				}
				result.Staged = append(result.Staged, name)
				return nil
			}
		})
		if err != nil {
			return result, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
	}

	if finalManifest == nil {
		return result, fmt.Errorf("last archive has no manifest")
	}
	if finalManifest.Images == nil {
		return result, fmt.Errorf("last archive has no image state; refusing to guess what to delete")
	}
	deleted, err := removeImagesNotIn(config.Server.ImagesDir, finalManifest.Images)
	if err != nil {
		return result, fmt.Errorf("remove images: %w", err)
	}
	result.Deleted = deleted

	if dbPath != "" {
		if err := restoreDB(dbPath); err != nil {
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
	logging.Infof("restore: %d archive(s), %d image(s) written, %d removed", len(paths), result.Images, result.Deleted)
	return result, nil
}

// removeImagesNotIn deletes every image under dir whose relative path is not
// a key of keep, and returns how many were removed.
func removeImagesNotIn(dir string, keep map[string]string) (int, error) {
	var stale []string
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
		if _, ok := keep[filepath.ToSlash(rel)]; !ok {
			stale = append(stale, path)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, p := range stale {
		if err := os.Remove(p); err != nil {
			return 0, err
		}
	}
	return len(stale), nil
}
