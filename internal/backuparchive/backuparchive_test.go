package backuparchive

import (
	"archive/tar"
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"golang.org/x/crypto/ssh"
)

func sshPubLine(t *testing.T, pub any) string {
	t.Helper()
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " test@example"
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestParseRecipientAccepts(t *testing.T) {
	x, _ := age.GenerateX25519Identity()
	if _, info, err := ParseRecipient(x.Recipient().String()); err != nil || info.Type != "age-x25519" {
		t.Fatalf("age1 key: info=%+v err=%v", info, err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	edKey, _ := ssh.NewPublicKey(pub)
	if _, info, err := ParseRecipient(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(edKey)))); err != nil || info.Type != "ssh-ed25519" {
		t.Fatalf("ssh-ed25519 key: info=%+v err=%v", info, err)
	}
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	if _, info, err := ParseRecipient(sshPubLine(t, &rsaKey.PublicKey)); err != nil || info.Type != "ssh-rsa" {
		t.Fatalf("ssh-rsa 2048: info=%+v err=%v", info, err)
	}
}

func TestParseRecipientRefuses(t *testing.T) {
	rsa1024, _ := rsa.GenerateKey(rand.Reader, 1024)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecPub, _ := ssh.NewPublicKey(&ec.PublicKey)
	cases := map[string]string{
		"rsa 1024":       sshPubLine(t, &rsa1024.PublicKey),
		"ecdsa":          strings.TrimSpace(string(ssh.MarshalAuthorizedKey(ecPub))),
		"post-quantum":   "age1pq1qqqqqqqqqqqqqqqqqqqq",
		"yubikey plugin": "age1yubikey1qqqqqqqqqqqqqqqqqqqqqqqqqq",
		"garbage":        "hello world",
	}
	for name, line := range cases {
		if _, _, err := ParseRecipient(line); err == nil {
			t.Errorf("%s: accepted %q, want refusal", name, line)
		}
	}
}

func TestParseRecipientsSkipsCommentsAndBlanks(t *testing.T) {
	x, _ := age.GenerateX25519Identity()
	recips, infos, err := ParseRecipients([]string{"# admin", "", "  " + x.Recipient().String() + "  "})
	if err != nil || len(recips) != 1 || len(infos) != 1 {
		t.Fatalf("recips=%d infos=%d err=%v", len(recips), len(infos), err)
	}
}

// roundTrip encrypts a fixed payload to recipients and decrypts it with the
// identities, checking the plaintext matches.
func roundTrip(t *testing.T, recips []age.Recipient, ids []age.Identity) {
	t.Helper()
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	enc := filepath.Join(dir, "enc")
	out := filepath.Join(dir, "out")
	payload := bytes.Repeat([]byte("dansal backup payload "), 1000)
	writeFile(t, plain, payload)
	if err := EncryptFile(plain, enc, recips...); err != nil {
		t.Fatal(err)
	}
	if err := DecryptFile(enc, out, ids...); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, payload) {
		t.Fatal("decrypted payload differs")
	}
}

func TestRoundTripAgeX25519(t *testing.T) {
	x, _ := age.GenerateX25519Identity()
	roundTrip(t, []age.Recipient{x.Recipient()}, []age.Identity{x})
}

func TestRoundTripSSHEd25519(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	block, _ := ssh.MarshalPrivateKey(priv, "test")
	keyFile := filepath.Join(dir, "id_ed25519")
	writeFile(t, keyFile, pem.EncodeToMemory(block))
	edKey, _ := ssh.NewPublicKey(pub)
	recips, _, err := ParseRecipients([]string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(edKey)))})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := LoadIdentities(keyFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, recips, ids)
}

func TestRoundTripSSHRSA(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	dir := t.TempDir()
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	keyFile := filepath.Join(dir, "id_rsa")
	writeFile(t, keyFile, pem.EncodeToMemory(block))
	recips, _, err := ParseRecipients([]string{sshPubLine(t, &key.PublicKey)})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := LoadIdentities(keyFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, recips, ids)
}

func TestRoundTripEncryptedSSHKeyPromptsForPassphrase(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	block, _ := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("hunter2"))
	keyFile := filepath.Join(dir, "id_ed25519")
	writeFile(t, keyFile, pem.EncodeToMemory(block))
	edKey, _ := ssh.NewPublicKey(pub)
	recips, _, _ := ParseRecipients([]string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(edKey)))})
	prompted := false
	ids, err := LoadIdentities(keyFile, func() ([]byte, error) { prompted = true; return []byte("hunter2"), nil })
	if err != nil {
		t.Fatal(err)
	}
	if !prompted {
		t.Fatal("passphrase was not requested")
	}
	roundTrip(t, recips, ids)
}

func TestRoundTripPassphrase(t *testing.T) {
	r, err := PassphraseRecipient("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	i, _ := PassphraseIdentity("correct horse")
	roundTrip(t, []age.Recipient{r}, []age.Identity{i})

	wrong, _ := PassphraseIdentity("battery staple")
	dir := t.TempDir()
	plain := filepath.Join(dir, "p")
	enc := filepath.Join(dir, "e")
	writeFile(t, plain, []byte("x"))
	if err := EncryptFile(plain, enc, r); err != nil {
		t.Fatal(err)
	}
	if err := DecryptFile(enc, filepath.Join(dir, "o"), wrong); err == nil {
		t.Fatal("wrong passphrase decrypted the archive")
	}
}

func TestArchiveRoundTripAndManifest(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "a.avif")
	writeFile(t, img, []byte("image-bytes"))
	hash, size, err := HashFile(img)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manifest{
		Format:  FormatVersion,
		Kind:    KindFull,
		ChainID: "chain-1",
		Archive: "dansal-full-test.tar.zst",
		Created: time.Now().UTC().Truncate(time.Second),
		Files:   []FileRecord{{Name: "images/a.avif", Size: size, SHA256: hash}},
		Images:  map[string]string{"a.avif": hash},
	}
	archive := filepath.Join(dir, m.Archive)
	f, _ := os.Create(archive)
	if err := WriteTar(f, m, []Entry{{Name: "images/a.avif", Src: img}}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := ReadManifestFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindFull || got.Images["a.avif"] != hash {
		t.Fatalf("manifest mismatch: %+v", got)
	}
	var names []string
	if err := ReadPlain(archive, func(h *tar.Header, _ *tar.Reader) error { names = append(names, h.Name); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != ManifestName || names[1] != "images/a.avif" {
		t.Fatalf("entries = %v", names)
	}
}

func TestResolveChainOrdersFullThenDependents(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, kind Kind, seq int, base string) {
		m := &Manifest{Format: FormatVersion, Kind: kind, ChainID: "c", Sequence: seq, Base: base, Archive: name, Created: time.Now()}
		writeFile(t, filepath.Join(dir, name), []byte("x"))
		if err := WriteSidecar(filepath.Join(dir, name), m); err != nil {
			t.Fatal(err)
		}
	}
	mk("full.tar.zst", KindFull, 0, "")
	mk("inc1.tar.zst", KindIncremental, 1, "full.tar.zst")
	mk("inc2.tar.zst", KindIncremental, 2, "inc1.tar.zst")
	chain, err := ResolveChain(dir, filepath.Join(dir, "inc2.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range chain {
		got = append(got, filepath.Base(a.Path))
	}
	if strings.Join(got, ",") != "full.tar.zst,inc1.tar.zst,inc2.tar.zst" {
		t.Fatalf("chain = %v", got)
	}
	os.Remove(filepath.Join(dir, "inc1.tar.zst"))
	if _, err := ResolveChain(dir, filepath.Join(dir, "inc2.tar.zst")); err == nil {
		t.Fatal("missing base not reported")
	}
}

// One archive, three recipient types: each identity decrypts it.
func TestMixedRecipientsEachDecrypt(t *testing.T) {
	x, _ := age.GenerateX25519Identity()
	edPub, edPriv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	block, _ := ssh.MarshalPrivateKey(edPriv, "")
	edKeyFile := filepath.Join(dir, "id_ed25519")
	writeFile(t, edKeyFile, pem.EncodeToMemory(block))
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsaBlock := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}
	rsaKeyFile := filepath.Join(dir, "id_rsa")
	writeFile(t, rsaKeyFile, pem.EncodeToMemory(rsaBlock))
	edSSH, _ := ssh.NewPublicKey(edPub)

	recips, infos, err := ParseRecipients([]string{
		x.Recipient().String(),
		strings.TrimSpace(string(ssh.MarshalAuthorizedKey(edSSH))),
		sshPubLine(t, &rsaKey.PublicKey),
	})
	if err != nil || len(recips) != 3 || len(infos) != 3 {
		t.Fatalf("recipients: n=%d err=%v", len(recips), err)
	}
	plain := filepath.Join(dir, "p")
	enc := filepath.Join(dir, "e")
	writeFile(t, plain, []byte("mixed"))
	if err := EncryptFile(plain, enc, recips...); err != nil {
		t.Fatal(err)
	}
	for name, keyFile := range map[string]string{"ssh-ed25519": edKeyFile, "ssh-rsa": rsaKeyFile} {
		ids, err := LoadIdentities(keyFile, nil)
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "o-"+name)
		if err := DecryptFile(enc, out, ids...); err != nil {
			t.Fatalf("%s identity: %v", name, err)
		}
	}
	out := filepath.Join(dir, "o-age")
	if err := DecryptFile(enc, out, x); err != nil {
		t.Fatalf("age identity: %v", err)
	}
}
