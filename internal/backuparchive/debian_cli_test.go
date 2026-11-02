package backuparchive

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"golang.org/x/crypto/ssh"
)

// TestDebianAgeCLIDecryptsArchives writes an archive for each supported
// recipient type and decrypts it with the `age` command, as an operator on a
// Debian host would. Skipped when age is not installed.
func TestDebianAgeCLIDecryptsArchives(t *testing.T) {
	ageBin, err := exec.LookPath("age")
	if err != nil {
		t.Skip("age CLI not installed")
	}
	dir := t.TempDir()
	m := &Manifest{Format: FormatVersion, Kind: KindFull, ChainID: "c", Archive: "x", Images: map[string]string{}}
	payload := []byte("hello from dansal")
	plain := filepath.Join(dir, "payload")
	os.WriteFile(plain, payload, 0600)

	// age X25519
	x, _ := age.GenerateX25519Identity()
	xKeyFile := filepath.Join(dir, "age.key")
	os.WriteFile(xKeyFile, []byte(x.String()+"\n"), 0600)

	// ssh-ed25519
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	sshKeyFile := filepath.Join(dir, "id_ed25519")
	os.WriteFile(sshKeyFile, pem.EncodeToMemory(block), 0600)
	edPub, _ := ssh.NewPublicKey(pub)
	sshLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(edPub)))
	sshRecips, _, err := ParseRecipients([]string{sshLine})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		recips  []age.Recipient
		keyFile string
	}{
		{"age-x25519", []age.Recipient{x.Recipient()}, xKeyFile},
		{"ssh-ed25519", sshRecips, sshKeyFile},
	}
	for _, c := range cases {
		archive := filepath.Join(dir, c.name+".tar.zst.age")
		f, _ := os.Create(archive)
		w, err := EncryptTo(f, c.recips...)
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteTar(w, m, []Entry{{Name: "payload", Src: plain}}); err != nil {
			t.Fatal(err)
		}
		w.Close()
		f.Close()

		var out bytes.Buffer
		cmd := exec.Command(ageBin, "-d", "-i", c.keyFile, archive)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		raw, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: age -d failed: %v: %s", c.name, err, stderr.String())
		}
		out.Write(raw)
		got, err := readTarPayload(out.Bytes())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("%s: payload differs after age -d", c.name)
		}
	}
}

// readTarPayload decodes a zstd tar stream and returns the "payload" entry.
func readTarPayload(data []byte) ([]byte, error) {
	var out []byte
	err := ReadStream(bytes.NewReader(data), func(hdr *tar.Header, tr *tar.Reader) error {
		if hdr.Name == "payload" {
			b, err := io.ReadAll(tr)
			out = b
			return err
		}
		return nil
	})
	return out, err
}

// TestZstdCLIReadsArchive checks the documented manual inspection:
// zstd -dc | tar -t lists the manifest first, then the payload.
func TestZstdCLIReadsArchive(t *testing.T) {
	zstdBin, err := exec.LookPath("zstd")
	if err != nil {
		t.Skip("zstd CLI not installed")
	}
	tarBin, err := exec.LookPath("tar")
	if err != nil {
		t.Skip("tar not installed")
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "payload")
	os.WriteFile(plain, []byte("zstd check"), 0600)
	m := &Manifest{Format: FormatVersion, Kind: KindFull, ChainID: "c", Archive: "x", Images: map[string]string{}}
	archive := filepath.Join(dir, "a.tar.zst")
	f, _ := os.Create(archive)
	if err := WriteTar(f, m, []Entry{{Name: "payload", Src: plain}}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	zcmd := exec.Command(zstdBin, "-dc", archive)
	tcmd := exec.Command(tarBin, "-t")
	pipe, _ := zcmd.StdoutPipe()
	tcmd.Stdin = pipe
	var listing bytes.Buffer
	tcmd.Stdout = &listing
	if err := zcmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := tcmd.Run(); err != nil {
		t.Fatalf("tar -t: %v", err)
	}
	zcmd.Wait()
	if got := strings.Fields(listing.String()); len(got) != 2 || got[0] != ManifestName || got[1] != "payload" {
		t.Fatalf("listing = %v", got)
	}
}
