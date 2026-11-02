package backuparchive

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// ReadPlain opens a decrypted archive file (zstd-compressed tar) and calls fn
// for every entry, in archive order. fn must not keep the reader.
func ReadPlain(path string, fn func(hdr *tar.Header, tr *tar.Reader) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return ReadStream(f, fn)
}

// ReadStream is ReadPlain for an already opened decrypted stream.
func ReadStream(r io.Reader, fn func(hdr *tar.Header, tr *tar.Reader) error) error {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(hdr, tr); err != nil {
			return err
		}
	}
}

// ReadManifestFile returns the manifest stored inside a decrypted archive.
// It must be the first entry; an archive without one is not a v1 archive.
func ReadManifestFile(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadManifestStream(f)
}

// ReadManifestStream reads the manifest from a decrypted archive stream.
func ReadManifestStream(r io.Reader) (*Manifest, error) {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	hdr, err := tr.Next()
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	if hdr.Name != ManifestName {
		return nil, fmt.Errorf("archive does not start with %s (got %q)", ManifestName, hdr.Name)
	}
	var m Manifest
	if err := json.NewDecoder(tr).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if m.Format != FormatVersion {
		return nil, fmt.Errorf("unsupported manifest format %d", m.Format)
	}
	return &m, nil
}
