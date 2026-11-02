package backuparchive

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Entry is one file to store: Name is its path inside the archive, Src the
// file on disk it is read from.
type Entry struct {
	Name string
	Src  string
}

// WriteTar writes a zstd-compressed tar stream to w: manifest.json first,
// then every entry. Entry sources are read here, so a file that vanished or
// changed since its hash was recorded is reported as an error by the caller's
// re-hash, not silently stored under a stale checksum.
func WriteTar(w io.Writer, m *Manifest, entries []Entry) error {
	zw, err := zstd.NewWriter(w)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)

	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := writeBytes(tw, ManifestName, body, m.Created); err != nil {
		return err
	}
	for _, e := range entries {
		if err := addFile(tw, e.Src, e.Name); err != nil {
			return fmt.Errorf("archive %s: %w", e.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

func writeBytes(tw *tar.Writer, name string, body []byte, mod time.Time) error {
	hdr := &tar.Header{Name: name, Mode: 0640, Size: int64(len(body)), ModTime: mod, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func addFile(tw *tar.Writer, src, name string) error {
	f, err := os.Open(src)
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
