package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// #1366: opt-in HMAC request signing, one secret per API key. A publisher
// binds each authenticated write to a client-held secret so a leaked Bearer
// token alone can't be replayed — defense in depth on top of the existing
// IP-pinned session token. Off by default at two independent levels:
// server.signing.enabled (process-wide) and api_keys.require_signature
// (per key), so existing publishers are unaffected until they opt in.

// signingKey is the AES-256 master key used to encrypt api_keys.signing_secret_enc
// at rest. Loaded once at startup from DANSAL_SIGNING_KEY (64 hex chars = 32
// bytes). Nil means encryption is disabled and the secret is stored plaintext
// — same fallback dansal_web's dbcrypto.go uses for ActivityPub private keys,
// chosen deliberately so an operator who forgets to set the env var gets a
// working (if less defended) server rather than a startup crash.
var signingKey []byte

const signingKeyEnvVar = "DANSAL_SIGNING_KEY"
const signingEncPrefix = "v1:"

// initSigningKey loads DANSAL_SIGNING_KEY from the environment. Must be
// called before any handler that reads/writes api_keys.signing_secret_enc.
func initSigningKey() {
	v := os.Getenv(signingKeyEnvVar)
	if v == "" {
		log.Printf("warning: %s is not set — publisher signing secrets stored in plaintext in DB; set this env var to enable encryption at rest", signingKeyEnvVar)
		return
	}
	key, err := hex.DecodeString(v)
	if err != nil || len(key) != 32 {
		log.Fatalf("%s must be 64 hex characters (32 bytes); generate with: openssl rand -hex 32", signingKeyEnvVar)
	}
	signingKey = key
}

// signingSecretEncrypt encrypts plain for storage in api_keys.signing_secret_enc.
// Returns "v1:<base64(nonce+ciphertext)>" when signingKey is set, plain otherwise.
func signingSecretEncrypt(plain string) string {
	if len(signingKey) == 0 {
		return plain
	}
	block, err := aes.NewCipher(signingKey)
	if err != nil {
		log.Fatalf("signingSecretEncrypt: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		log.Fatalf("signingSecretEncrypt: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		log.Fatalf("signingSecretEncrypt: %v", err)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return signingEncPrefix + base64.StdEncoding.EncodeToString(ct)
}

// signingSecretDecrypt reverses signingSecretEncrypt. A value with no "v1:"
// prefix was written while encryption was disabled and is returned as-is.
func signingSecretDecrypt(stored string) (string, error) {
	if !strings.HasPrefix(stored, signingEncPrefix) {
		return stored, nil
	}
	if len(signingKey) == 0 {
		return "", fmt.Errorf("signing secret is encrypted but %s is not set", signingKeyEnvVar)
	}
	ct, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, signingEncPrefix))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(signingKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ct) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	plain, err := gcm.Open(nil, ct[:gcm.NonceSize()], ct[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt failed")
	}
	return string(plain), nil
}

// dbExecer covers both *sql.DB and *sql.Tx, so issueSigningSecretIfEnabled
// can run inside a caller's transaction or standalone.
type dbExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// issueSigningSecretIfEnabled generates and stores a signing secret for a
// freshly created/rotated api_keys row, but only when signing is enabled
// process-wide — otherwise it's a no-op returning "" so callers can omit the
// field from their response entirely (see #1366's acceptance criterion:
// "Publisher creation response includes signing_secret when the enabling
// flag is set"). require_signature is deliberately left at its default (0):
// issuing a secret lets a client start signing requests before enforcement
// is turned on for that key, which an admin/webmin toggle does separately.
func issueSigningSecretIfEnabled(exec dbExecer, keyID int) (string, error) {
	if !config.Server.Signing.Enabled {
		return "", nil
	}
	secret, err := generateSigningSecret()
	if err != nil {
		return "", err
	}
	if _, err := exec.Exec("UPDATE api_keys SET signing_secret_enc=? WHERE id=?", signingSecretEncrypt(secret), keyID); err != nil {
		return "", err
	}
	return secret, nil
}

// generateSigningSecret returns a fresh 32-byte secret, hex-encoded.
func generateSigningSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// signingExemptPaths are authenticated write endpoints that are explicitly
// out of scope for signature enforcement: the pre-auth token exchange, done
// by an org member/admin on the publisher's behalf, not by the publisher's
// own signed client.
var signingExemptPaths = map[string]bool{
	"/api/v1/publishers/token": true,
}

// requiresSignatureCheck reports whether r is in scope for signature
// verification at all: signing must be enabled process-wide, the method
// must be a write, and the path must not be explicitly exempted. This is
// deliberately independent of whether any particular caller's key actually
// requires a signature — that's decided per key in checkRequestSignature.
func requiresSignatureCheck(r *http.Request) bool {
	if config == nil || !config.Server.Signing.Enabled {
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return !signingExemptPaths[r.URL.Path]
}

// signingMaxSkew returns the configured skew tolerance, defaulting to 300s.
func signingMaxSkew() time.Duration {
	if config.Server.Signing.MaxSkewSecs <= 0 {
		return 300 * time.Second
	}
	return time.Duration(config.Server.Signing.MaxSkewSecs) * time.Second
}

// checkRequestSignature looks up apiKeyID's signing settings and, only when
// that specific key has opted in (require_signature=1), verifies r's
// X-Wpd-* headers. A key that hasn't opted in is a silent no-op — signing
// is per-publisher opt-in, not forced by the process-wide enabled flag alone.
func checkRequestSignature(r *http.Request, apiKeyID int) error {
	var requireSig int
	var secretEnc sql.NullString
	if err := db.QueryRow(
		"SELECT require_signature, signing_secret_enc FROM api_keys WHERE id = ?",
		apiKeyID,
	).Scan(&requireSig, &secretEnc); err != nil {
		return fmt.Errorf("could not look up signing key")
	}
	if requireSig == 0 {
		return nil
	}
	if !secretEnc.Valid || secretEnc.String == "" {
		return fmt.Errorf("signing required but no secret is configured")
	}
	secret, err := signingSecretDecrypt(secretEnc.String)
	if err != nil {
		return fmt.Errorf("could not read signing secret")
	}
	return verifyRequestSignature(r, apiKeyID, secret, signingMaxSkew())
}

// canonicalSigningPayload builds the five-line canonical form that both
// dansal and wp-dansal sign: method, path+query, timestamp, sha256(body)
// hex, nonce. The query string is rebuilt from parsed values via
// url.Values.Encode() (alphabetical by key, then value, standard
// percent-encoding) rather than taken verbatim from the wire, so the client
// never has to guess the server's canonicalization — it must independently
// produce the same canonical form when signing (e.g. PHP: ksort() + RFC3986
// http_build_query()), but doesn't need byte-identical wire bytes.
func canonicalSigningPayload(method, path, rawQuery, timestamp, bodyHashHex, nonce string) string {
	p := path
	if rawQuery != "" {
		if values, err := url.ParseQuery(rawQuery); err == nil {
			if canonical := values.Encode(); canonical != "" {
				p += "?" + canonical
			}
		}
	}
	return strings.Join([]string{strings.ToUpper(method), p, timestamp, bodyHashHex, nonce}, "\n")
}

// signingNonceCache remembers "<apiKeyID>:<nonce>" pairs seen within the
// configured skew window, so a captured request can't be replayed verbatim.
// In-memory only — documented MVP limitation (#1366): a multi-instance
// deployment would need shared state, which is out of scope for now.
var signingNonceCache = struct {
	mu   sync.Mutex
	seen map[string]time.Time
}{seen: make(map[string]time.Time)}

// signingNonceSeen reports whether apiKeyID+nonce has already been recorded
// within ttl, recording it (with a fresh expiry) if not. Also opportunistically
// sweeps expired entries so the map doesn't grow unbounded between restarts.
func signingNonceSeen(apiKeyID int, nonce string, ttl time.Duration) bool {
	key := strconv.Itoa(apiKeyID) + ":" + nonce
	now := time.Now()

	signingNonceCache.mu.Lock()
	defer signingNonceCache.mu.Unlock()

	if exp, ok := signingNonceCache.seen[key]; ok && now.Before(exp) {
		return true
	}
	if len(signingNonceCache.seen) > 10000 {
		for k, exp := range signingNonceCache.seen {
			if now.After(exp) {
				delete(signingNonceCache.seen, k)
			}
		}
	}
	signingNonceCache.seen[key] = now.Add(ttl)
	return false
}

// verifyRequestSignature checks X-Wpd-Timestamp/X-Wpd-Nonce/X-Wpd-Signature
// against secret (already decrypted), enforcing the skew window and the
// nonce-replay cache. On success, r.Body is left readable for the real
// handler (it's consumed here to hash it, then replaced).
func verifyRequestSignature(r *http.Request, apiKeyID int, secret string, maxSkew time.Duration) error {
	ts := r.Header.Get("X-Wpd-Timestamp")
	nonce := r.Header.Get("X-Wpd-Nonce")
	sig := r.Header.Get("X-Wpd-Signature")
	if ts == "" || nonce == "" || sig == "" {
		return fmt.Errorf("missing signing headers")
	}

	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp")
	}
	skew := time.Since(time.Unix(tsInt, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > maxSkew {
		return fmt.Errorf("timestamp outside allowed skew")
	}

	if len(nonce) != 32 {
		return fmt.Errorf("invalid nonce")
	}
	if signingNonceSeen(apiKeyID, nonce, 2*maxSkew) {
		return fmt.Errorf("nonce already used")
	}

	var bodyHashHex string
	if r.Body != nil && r.Body != http.NoBody {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		if len(bodyBytes) > 0 {
			sum := sha256.Sum256(bodyBytes)
			bodyHashHex = hex.EncodeToString(sum[:])
		}
	}

	payload := canonicalSigningPayload(r.Method, r.URL.Path, r.URL.RawQuery, ts, bodyHashHex, nonce)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(sig)) != 1 {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}
