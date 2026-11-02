package backuparchive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sidecarSuffix names the unencrypted manifest copy next to an archive.
const sidecarSuffix = ".manifest.json"

// Archive is an archive found in a backup directory together with its
// manifest copy. Path is the full path of the archive file.
type Archive struct {
	Path string
	M    *Manifest
}

// SidecarPath returns where the manifest copy for archivePath lives.
func SidecarPath(archivePath string) string { return archivePath + sidecarSuffix }

// WriteSidecar stores the manifest copy next to archivePath, atomically.
func WriteSidecar(archivePath string, m *Manifest) error {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	dst := SidecarPath(archivePath)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, body, 0640); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// ReadSidecar loads the manifest copy for archivePath.
func ReadSidecar(archivePath string) (*Manifest, error) {
	body, err := os.ReadFile(SidecarPath(archivePath))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("manifest copy %s: %w", SidecarPath(archivePath), err)
	}
	return &m, nil
}

// ListArchives returns every archive in dir that has a manifest copy and
// whose archive file still exists, newest first. Archives without a copy
// (legacy or unrelated files) are not part of any chain and are ignored.
func ListArchives(dir string) ([]Archive, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Archive
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, sidecarSuffix) {
			continue
		}
		archivePath := filepath.Join(dir, strings.TrimSuffix(name, sidecarSuffix))
		if _, err := os.Stat(archivePath); err != nil {
			continue
		}
		m, err := ReadSidecar(archivePath)
		if err != nil {
			continue
		}
		out = append(out, Archive{Path: archivePath, M: m})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].M.Created.After(out[j].M.Created) })
	return out, nil
}

// Latest returns the newest archive in dir matching kind, or nil if there is
// none. kind "" matches any kind.
func Latest(dir string, kind Kind) (*Archive, error) {
	all, err := ListArchives(dir)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if kind == "" || all[i].M.Kind == kind {
			return &all[i], nil
		}
	}
	return nil, nil
}

// ResolveChain returns the archives needed to restore archivePath, oldest
// first: its full, then each dependent archive up to archivePath. Every base
// must be present in dir; a missing link is an error naming the file.
func ResolveChain(dir, archivePath string) ([]Archive, error) {
	var chain []Archive
	cur := filepath.Join(dir, filepath.Base(archivePath))
	for hops := 0; ; hops++ {
		if hops > 10000 {
			return nil, fmt.Errorf("backup chain of %s is too long", archivePath)
		}
		m, err := ReadSidecar(cur)
		if err != nil {
			return nil, fmt.Errorf("manifest copy for %s: %w", filepath.Base(cur), err)
		}
		chain = append([]Archive{{Path: cur, M: m}}, chain...)
		if m.Kind == KindFull {
			return chain, nil
		}
		if m.Base == "" {
			return nil, fmt.Errorf("%s is a %s without a base", filepath.Base(cur), m.Kind)
		}
		cur = filepath.Join(dir, m.Base)
		if _, err := os.Stat(cur); err != nil {
			return nil, fmt.Errorf("base %s of %s is missing: %w", m.Base, filepath.Base(archivePath), err)
		}
	}
}

// ChainIDs groups archives by chain and returns the chain IDs newest first,
// by the newest archive of each chain.
func ChainIDs(all []Archive) []string {
	seen := map[string]bool{}
	var ids []string
	for _, a := range all {
		if !seen[a.M.ChainID] {
			seen[a.M.ChainID] = true
			ids = append(ids, a.M.ChainID)
		}
	}
	return ids
}
