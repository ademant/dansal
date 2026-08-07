package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/pbkdf2"
)

// Argon2id parameters per RFC 9106's "second recommended" option.
const (
	argon2Time    = 3
	argon2Memory  = 64 * 1024 // KiB (64 MiB)
	argon2Threads = 4
	argon2KeyLen  = 32
	argon2SaltLen = 16
)

// PBKDF2-HMAC-SHA256 iteration count per OWASP's current recommendation
// (600,000 — the 210,000 figure was superseded, #987). This is the FIPS
// 140-approved option (see #802) — pick it via password_kdf in config.yaml
// when FIPS compliance is required.
const (
	pbkdf2Iterations = 600000
	pbkdf2KeyLen     = 32
	pbkdf2SaltLen    = 16
)

// passwordKDF returns the configured algorithm for hashing new passwords,
// falling back to argon2id for an empty or unrecognized value.
func passwordKDF() string {
	if config.Server.PasswordKDF == "pbkdf2" {
		return "pbkdf2"
	}
	return "argon2id"
}

func hashArgon2id(password string) string {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		panic(fmt.Sprintf("argon2id: %v", err))
	}
	hash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2Memory, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash))
}

func checkArgon2id(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 6 {
		return false
	}
	var version, memory, time int
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(time), uint32(memory), threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func hashPBKDF2(password string) string {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		panic(fmt.Sprintf("pbkdf2: %v", err))
	}
	hash := pbkdf2.Key([]byte(password), salt, pbkdf2Iterations, pbkdf2KeyLen, sha256.New)
	return fmt.Sprintf("$pbkdf2-sha256$i=%d$%s$%s",
		pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash))
}

func checkPBKDF2(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 5 {
		return false
	}
	var iterations int
	if _, err := fmt.Sscanf(parts[2], "i=%d", &iterations); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	got := pbkdf2.Key([]byte(password), salt, iterations, len(want), sha256.New)
	return subtle.ConstantTimeCompare(got, want) == 1
}
