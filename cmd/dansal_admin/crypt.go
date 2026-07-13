package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/term"
)

// Encrypted backup file format:
//
//	magic[9]  "DANSALENC"
//	version[1] = 0x01 (legacy scrypt) or 0x02 (pbkdf2, current — see #803)
//
// version 0x01 (legacy, read-only):
//
//	N[4]      scrypt N parameter, big-endian uint32
//	r[1]      scrypt r
//	p[1]      scrypt p
//	salt[32]  random salt
//	nonce[12] AES-256-GCM nonce
//	data[...]  AES-256-GCM ciphertext + 16-byte tag
//
// version 0x02 (current):
//
//	iterations[4] PBKDF2-HMAC-SHA256 iteration count, big-endian uint32
//	salt[32]      random salt
//	nonce[12]     AES-256-GCM nonce
//	data[...]     AES-256-GCM ciphertext + 16-byte tag
const encMagic = "DANSALENC"

const (
	scryptN   = 65536
	scryptR   = 8
	scryptP   = 1
	scryptKey = 32
)

// pbkdf2Iterations is the backup-file KDF iteration count (NIST SP 800-132 /
// OWASP 2024 recommendation for FIPS 140-compatible backup encryption).
const (
	backupPBKDF2Iterations = 600000
	backupKeyLen           = 32
)

func encryptFile(src, dst string, password []byte) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return err
	}

	key := pbkdf2.Key(password, salt, backupPBKDF2Iterations, backupKeyLen, sha256.New)

	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}

	ciphertext := gcm.Seal(nil, nonce, data, nil)

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	var iterBuf [4]byte
	binary.BigEndian.PutUint32(iterBuf[:], backupPBKDF2Iterations)

	for _, chunk := range [][]byte{
		[]byte(encMagic),
		{0x02},
		iterBuf[:],
		salt,
		nonce,
		ciphertext,
	} {
		if _, err := f.Write(chunk); err != nil {
			return err
		}
	}
	return nil
}

func decryptFile(src string, password []byte) ([]byte, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	magic := make([]byte, len(encMagic))
	if _, err := io.ReadFull(f, magic); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if string(magic) != encMagic {
		return nil, fmt.Errorf("not an encrypted dansal backup")
	}

	ver := make([]byte, 1)
	if _, err := io.ReadFull(f, ver); err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}

	var key []byte
	switch ver[0] {
	case 0x01:
		var nBuf [4]byte
		if _, err := io.ReadFull(f, nBuf[:]); err != nil {
			return nil, err
		}
		N := int(binary.BigEndian.Uint32(nBuf[:]))

		rp := make([]byte, 2)
		if _, err := io.ReadFull(f, rp); err != nil {
			return nil, err
		}
		r, p := int(rp[0]), int(rp[1])

		salt := make([]byte, 32)
		if _, err := io.ReadFull(f, salt); err != nil {
			return nil, err
		}

		var err error
		key, err = scrypt.Key(password, salt, N, r, p, scryptKey)
		if err != nil {
			return nil, err
		}
	case 0x02:
		var iterBuf [4]byte
		if _, err := io.ReadFull(f, iterBuf[:]); err != nil {
			return nil, err
		}
		iterations := int(binary.BigEndian.Uint32(iterBuf[:]))

		salt := make([]byte, 32)
		if _, err := io.ReadFull(f, salt); err != nil {
			return nil, err
		}

		key = pbkdf2.Key(password, salt, iterations, backupKeyLen, sha256.New)
	default:
		return nil, fmt.Errorf("unsupported version %d", ver[0])
	}

	nonce := make([]byte, 12)
	if _, err := io.ReadFull(f, nonce); err != nil {
		return nil, err
	}

	ciphertext, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: wrong password or corrupted file")
	}
	return plain, nil
}

func promptPassword(prompt string) ([]byte, error) {
	fmt.Fprint(os.Stderr, prompt)
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return pw, err
}
