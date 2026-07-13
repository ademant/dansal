package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
)

// dbKey is the AES-256 master key for encrypting sensitive DB fields.
// Loaded at startup from DANSAL_DB_KEY (64 hex chars = 32 bytes).
// Nil means encryption is disabled and values are stored plaintext.
var dbKey []byte

const dbKeyEnvVar = "DANSAL_DB_KEY"
const encPrefix = "v1:"

// initDBKey loads DANSAL_DB_KEY from the environment. Must be called before
// any DB read/write that uses actorKeyEncrypt / actorKeyDecrypt.
func initDBKey() {
	v := os.Getenv(dbKeyEnvVar)
	if v == "" {
		log.Printf("warning: %s is not set — ActivityPub private keys stored in plaintext in DB; set this env var to enable encryption at rest", dbKeyEnvVar)
		return
	}
	key, err := hex.DecodeString(v)
	if err != nil || len(key) != 32 {
		log.Fatalf("%s must be 64 hex characters (32 bytes); generate with: openssl rand -hex 32", dbKeyEnvVar)
	}
	dbKey = key
}

// actorKeyEncrypt encrypts a plaintext PEM string for storage.
// Returns "v1:<base64(nonce+ciphertext)>" when dbKey is set, plaintext otherwise.
func actorKeyEncrypt(plainPEM string) string {
	if len(dbKey) == 0 {
		return plainPEM
	}
	block, err := aes.NewCipher(dbKey)
	if err != nil {
		log.Fatalf("actorKeyEncrypt: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		log.Fatalf("actorKeyEncrypt: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		log.Fatalf("actorKeyEncrypt: %v", err)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plainPEM), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(ct)
}

// actorKeyDecrypt decrypts a stored value back to a plaintext PEM.
// Handles both encrypted ("v1:...") and legacy plaintext ("-----BEGIN...") values.
func actorKeyDecrypt(stored string) (string, error) {
	if !strings.HasPrefix(stored, encPrefix) {
		return stored, nil // legacy plaintext
	}
	if len(dbKey) == 0 {
		return "", fmt.Errorf("actor key is encrypted but %s is not set", dbKeyEnvVar)
	}
	raw, err := base64.StdEncoding.DecodeString(stored[len(encPrefix):])
	if err != nil {
		return "", fmt.Errorf("decode actor key: %w", err)
	}
	block, err := aes.NewCipher(dbKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("actor key ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt actor key: %w", err)
	}
	return string(plain), nil
}

// migrateActorKeyEncryption re-encrypts any plaintext private keys in the
// actors table. Called at startup when dbKey is set; idempotent.
func migrateActorKeyEncryption(db *sql.DB) {
	if len(dbKey) == 0 {
		return
	}
	rows, err := db.Query("SELECT id, private_key_pem FROM actors")
	if err != nil {
		log.Printf("actorKeyMigration: query: %v", err)
		return
	}
	defer rows.Close()

	type row struct {
		id  int
		pem string
	}
	var plain []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.pem); err != nil {
			continue
		}
		if !strings.HasPrefix(r.pem, encPrefix) {
			plain = append(plain, r)
		}
	}
	rows.Close()

	for _, r := range plain {
		enc := actorKeyEncrypt(r.pem)
		if _, err := db.Exec("UPDATE actors SET private_key_pem=? WHERE id=?", enc, r.id); err != nil {
			log.Printf("actorKeyMigration: update actor %d: %v", r.id, err)
		}
	}
	if len(plain) > 0 {
		log.Printf("actorKeyMigration: encrypted %d plaintext actor key(s)", len(plain))
	}
}
