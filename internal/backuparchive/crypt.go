package backuparchive

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"golang.org/x/crypto/ssh"
)

// RecipientInfo is what the server logs about a configured recipient: its
// type and a short fingerprint. The key line itself is never logged.
type RecipientInfo struct {
	Type        string
	Fingerprint string
}

// ParseRecipient parses one public-key recipient line. Accepted:
//
//	age1…                 X25519 (age native)
//	ssh-ed25519 AAAA…     Ed25519 SSH key
//	ssh-rsa AAAA…         RSA SSH key, at least 2048 bits
//
// Anything else (post-quantum age1pq…, plugin recipients such as
// age1yubikey…, ECDSA and FIDO SSH keys) is refused: Debian's age 1.2.1 cannot
// decrypt those, and archives must stay decryptable with the Debian tools.
func ParseRecipient(line string) (age.Recipient, RecipientInfo, error) {
	s := strings.TrimSpace(line)
	info := RecipientInfo{Fingerprint: fingerprint(s)}
	switch {
	case strings.HasPrefix(s, "age1pq") || strings.HasPrefix(s, "age1yubikey") || strings.HasPrefix(s, "age1tpm"):
		return nil, info, fmt.Errorf("recipient type not supported (post-quantum or plugin recipients cannot be decrypted by Debian's age): %s", info.Fingerprint)
	case strings.HasPrefix(s, "age1"):
		r, err := age.ParseX25519Recipient(s)
		if err != nil {
			return nil, info, fmt.Errorf("invalid age recipient (%s): %w", info.Fingerprint, err)
		}
		info.Type = "age-x25519"
		return r, info, nil
	case strings.HasPrefix(s, "ssh-ed25519 "):
		r, err := agessh.ParseRecipient(s)
		if err != nil {
			return nil, info, fmt.Errorf("invalid ssh-ed25519 recipient (%s): %w", info.Fingerprint, err)
		}
		info.Type = "ssh-ed25519"
		return r, info, nil
	case strings.HasPrefix(s, "ssh-rsa "):
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s))
		if err != nil {
			return nil, info, fmt.Errorf("invalid ssh-rsa recipient (%s): %w", info.Fingerprint, err)
		}
		cpk, ok := pk.(ssh.CryptoPublicKey)
		if !ok {
			return nil, info, fmt.Errorf("invalid ssh-rsa recipient (%s)", info.Fingerprint)
		}
		rk, ok := cpk.CryptoPublicKey().(*rsa.PublicKey)
		if !ok || rk.N.BitLen() < 2048 {
			return nil, info, fmt.Errorf("ssh-rsa recipient (%s) must be at least 2048 bits", info.Fingerprint)
		}
		r, err := agessh.ParseRecipient(s)
		if err != nil {
			return nil, info, fmt.Errorf("invalid ssh-rsa recipient (%s): %w", info.Fingerprint, err)
		}
		info.Type = "ssh-rsa"
		return r, info, nil
	}
	return nil, info, fmt.Errorf("unsupported recipient type (%s): expected age1…, ssh-ed25519 or ssh-rsa", info.Fingerprint)
}

// ParseRecipients parses a list of recipient lines. Blank lines and lines
// starting with # are skipped (so a recipients file can be passed directly).
func ParseRecipients(lines []string) ([]age.Recipient, []RecipientInfo, error) {
	var recips []age.Recipient
	var infos []RecipientInfo
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		r, info, err := ParseRecipient(t)
		if err != nil {
			return nil, nil, err
		}
		recips = append(recips, r)
		infos = append(infos, info)
	}
	return recips, infos, nil
}

// ReadRecipientsFile returns the non-comment lines of a recipients file
// (the same format `age -R` reads).
func ReadRecipientsFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out, sc.Err()
}

func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// PassphraseRecipient returns an age passphrase recipient (scrypt).
func PassphraseRecipient(pass string) (age.Recipient, error) {
	if pass == "" {
		return nil, errors.New("passphrase is empty")
	}
	return age.NewScryptRecipient(pass)
}

// EncryptTo returns a writer that encrypts to recipients into dst. Close it
// to finish the stream; it does not close dst.
func EncryptTo(dst io.Writer, recipients ...age.Recipient) (io.WriteCloser, error) {
	return age.Encrypt(dst, recipients...)
}

// EncryptFile encrypts plainPath to dstPath with the given recipients and
// removes nothing; the caller owns the plaintext.
func EncryptFile(plainPath, dstPath string, recipients ...age.Recipient) error {
	in, err := os.Open(plainPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
	if err != nil {
		return err
	}
	w, err := age.Encrypt(out, recipients...)
	if err != nil {
		out.Close()
		return err
	}
	if _, err := io.Copy(w, in); err != nil {
		out.Close()
		return err
	}
	if err := w.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// DecryptFile decrypts srcPath (age format) to dstPath using identities.
func DecryptFile(srcPath, dstPath string, identities ...age.Identity) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	r, err := age.Decrypt(in, identities...)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// LoadIdentities reads an age identity file (AGE-SECRET-KEY-1 lines) or an
// SSH private key (ed25519 or rsa, encrypted or not). An encrypted SSH key
// asks for its passphrase through prompt.
func LoadIdentities(path string, prompt func() ([]byte, error)) ([]age.Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(data, []byte("AGE-SECRET-KEY-1")) {
		// Not an age identity file: treat it as an SSH private key (OpenSSH,
		// PKCS#1 RSA or PKCS#8 PEM, encrypted or not).
		id, err := parseSSHIdentity(data, prompt)
		if err != nil {
			return nil, fmt.Errorf("identity %s: %w", path, err)
		}
		return []age.Identity{id}, nil
	}
	ids, err := age.ParseIdentities(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("identity %s: %w", path, err)
	}
	return ids, nil
}

func parseSSHIdentity(pem []byte, prompt func() ([]byte, error)) (age.Identity, error) {
	raw, err := ssh.ParseRawPrivateKey(pem)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		if prompt == nil {
			return nil, errors.New("SSH identity is passphrase-protected and no prompt is available")
		}
		pass, perr := prompt()
		if perr != nil {
			return nil, perr
		}
		raw, err = ssh.ParseRawPrivateKeyWithPassphrase(pem, pass)
	}
	if err != nil {
		return nil, err
	}
	switch k := raw.(type) {
	case *ed25519.PrivateKey:
		return agessh.NewEd25519Identity(*k)
	case ed25519.PrivateKey:
		return agessh.NewEd25519Identity(k)
	case *rsa.PrivateKey:
		return agessh.NewRSAIdentity(k)
	}
	return nil, fmt.Errorf("unsupported SSH identity type %T", raw)
}

// PassphraseIdentity returns the identity matching PassphraseRecipient.
func PassphraseIdentity(pass string) (age.Identity, error) {
	return age.NewScryptIdentity(pass)
}
