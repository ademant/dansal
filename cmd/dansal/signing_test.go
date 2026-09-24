package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func withSigningConfig(t *testing.T, cfg RequestSigningConfig) {
	t.Helper()
	old := config
	config = &Config{Server: ServerConfig{Signing: cfg}}
	t.Cleanup(func() { config = old })
}

// TestSigningSecretEncryptDecrypt_Roundtrip covers #1366's at-rest encryption
// for api_keys.signing_secret_enc: with a key set it round-trips through the
// "v1:" prefix; with no key set (the documented fallback) it's a no-op.
func TestSigningSecretEncryptDecrypt_Roundtrip(t *testing.T) {
	oldKey := signingKey
	t.Cleanup(func() { signingKey = oldKey })

	signingKey, _ = hex.DecodeString(strings.Repeat("ab", 32))
	enc := signingSecretEncrypt("super-secret")
	if !strings.HasPrefix(enc, signingEncPrefix) {
		t.Fatalf("expected %q prefix, got %q", signingEncPrefix, enc)
	}
	got, err := signingSecretDecrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "super-secret" {
		t.Errorf("roundtrip = %q, want %q", got, "super-secret")
	}

	signingKey = nil
	plain := signingSecretEncrypt("no-key-set")
	if plain != "no-key-set" {
		t.Errorf("with no signingKey, encrypt should be a no-op, got %q", plain)
	}
	got2, err := signingSecretDecrypt(plain)
	if err != nil || got2 != "no-key-set" {
		t.Errorf("decrypt of unprefixed value = (%q, %v), want (%q, nil)", got2, err, "no-key-set")
	}
}

// TestCanonicalSigningPayload_QueryOrderIndependent covers #1366's resolved
// open question: the signature base is built from parsed+re-sorted query
// values (url.Values.Encode()), so two different wire orderings of the same
// params produce an identical canonical payload.
func TestCanonicalSigningPayload_QueryOrderIndependent(t *testing.T) {
	a := canonicalSigningPayload("POST", "/api/v1/events", "b=2&a=1", "1700000000", "", "nonce")
	b := canonicalSigningPayload("POST", "/api/v1/events", "a=1&b=2", "1700000000", "", "nonce")
	if a != b {
		t.Errorf("canonical payloads differ by wire query order:\n  a=%q\n  b=%q", a, b)
	}
	if !strings.Contains(a, "a=1&b=2") {
		t.Errorf("expected alphabetically-sorted query in payload, got %q", a)
	}
}

func signPayload(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func newSignedRequest(t *testing.T, secret, method, target, nonce string, ts time.Time, body string) *http.Request {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}

	tsStr := strconv.FormatInt(ts.Unix(), 10)
	var bodyHash string
	if body != "" {
		sum := sha256.Sum256([]byte(body))
		bodyHash = hex.EncodeToString(sum[:])
	}
	payload := canonicalSigningPayload(method, req.URL.Path, req.URL.RawQuery, tsStr, bodyHash, nonce)
	sig := signPayload(secret, payload)

	req.Header.Set("X-Wpd-Timestamp", tsStr)
	req.Header.Set("X-Wpd-Nonce", nonce)
	req.Header.Set("X-Wpd-Signature", sig)
	return req
}

// TestVerifyRequestSignature covers #1366's acceptance criteria: valid
// signature, missing header, expired timestamp, replayed nonce, wrong
// secret, tampered path, tampered body.
func TestVerifyRequestSignature(t *testing.T) {
	const secret = "test-signing-secret"
	skew := 5 * time.Minute
	// Fixed-length (32 hex chars) nonces, one per subtest so the replay
	// cache (keyed by apiKeyID+nonce) never crosses subtests unintentionally.
	nonceFor := func(n int) string { return strings.Repeat(strconv.Itoa(n), 32)[:32] }

	t.Run("valid signature", func(t *testing.T) {
		req := newSignedRequest(t, secret, "POST", "/api/v1/events/42", nonceFor(1), time.Now(), `{"a":1}`)
		if err := verifyRequestSignature(req, 1, secret, skew); err != nil {
			t.Errorf("expected valid signature to pass, got %v", err)
		}
	})

	t.Run("missing header", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/events/42", nil)
		if err := verifyRequestSignature(req, 2, secret, skew); err == nil {
			t.Error("expected error for missing signing headers")
		}
	})

	t.Run("expired timestamp", func(t *testing.T) {
		req := newSignedRequest(t, secret, "POST", "/api/v1/events/42", nonceFor(3), time.Now().Add(-time.Hour), "")
		if err := verifyRequestSignature(req, 3, secret, skew); err == nil {
			t.Error("expected error for timestamp outside skew")
		}
	})

	t.Run("replayed nonce", func(t *testing.T) {
		n := nonceFor(4)
		req1 := newSignedRequest(t, secret, "POST", "/api/v1/events/42", n, time.Now(), "")
		if err := verifyRequestSignature(req1, 4, secret, skew); err != nil {
			t.Fatalf("first use should pass: %v", err)
		}
		req2 := newSignedRequest(t, secret, "POST", "/api/v1/events/42", n, time.Now(), "")
		if err := verifyRequestSignature(req2, 4, secret, skew); err == nil {
			t.Error("expected error for replayed nonce on the same api key id")
		}
	})

	t.Run("wrong secret", func(t *testing.T) {
		req := newSignedRequest(t, "a-different-secret", "POST", "/api/v1/events/42", nonceFor(5), time.Now(), "")
		if err := verifyRequestSignature(req, 6, secret, skew); err == nil {
			t.Error("expected error for signature made with the wrong secret")
		}
	})

	t.Run("tampered path", func(t *testing.T) {
		req := newSignedRequest(t, secret, "POST", "/api/v1/events/42", nonceFor(6), time.Now(), "")
		req.URL.Path = "/api/v1/events/999"
		if err := verifyRequestSignature(req, 7, secret, skew); err == nil {
			t.Error("expected error for a path that changed after signing")
		}
	})

	t.Run("tampered body", func(t *testing.T) {
		req := newSignedRequest(t, secret, "POST", "/api/v1/events/42", nonceFor(7), time.Now(), `{"a":1}`)
		req.Body = http.NoBody
		req2 := httptest.NewRequest("POST", "/api/v1/events/42", strings.NewReader(`{"a":2}`))
		req2.Header = req.Header
		if err := verifyRequestSignature(req2, 8, secret, skew); err == nil {
			t.Error("expected error for a body that changed after signing")
		}
	})
}

// TestCheckRequestSignature_RequireSignatureGating covers #1366: a key with
// require_signature=0 is a silent no-op even with garbage/no headers; a key
// with require_signature=1 enforces valid headers.
func TestCheckRequestSignature_RequireSignatureGating(t *testing.T) {
	setupDedupTestDB(t)
	withSigningConfig(t, RequestSigningConfig{Enabled: true, MaxSkewSecs: 300})

	db.Exec("INSERT INTO users (id, role, display_name, password_hash) VALUES (1, 'publisher', 'Pub', '')")
	res, _ := db.Exec("INSERT INTO api_keys (user_id, name, api_key, require_signature) VALUES (1, 'k', 'x', 0)")
	notRequiredID, _ := res.LastInsertId()

	req := httptest.NewRequest("POST", "/api/v1/events", nil)
	if err := checkRequestSignature(req, int(notRequiredID)); err != nil {
		t.Errorf("require_signature=0 should be a no-op, got %v", err)
	}

	secret := "s3cr3t"
	res2, _ := db.Exec(
		"INSERT INTO api_keys (user_id, name, api_key, require_signature, signing_secret_enc) VALUES (1, 'k2', 'y', 1, ?)",
		signingSecretEncrypt(secret),
	)
	requiredID, _ := res2.LastInsertId()

	unsigned := httptest.NewRequest("POST", "/api/v1/events", nil)
	if err := checkRequestSignature(unsigned, int(requiredID)); err == nil {
		t.Error("require_signature=1 with no headers should fail")
	}

	signed := newSignedRequest(t, secret, "POST", "/api/v1/events", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now(), "")
	if err := checkRequestSignature(signed, int(requiredID)); err != nil {
		t.Errorf("require_signature=1 with a valid signature should pass, got %v", err)
	}
}

// TestResolveCaller_SignatureRequiredEndToEnd covers #1366 wired all the way
// through resolveCaller: a publisher API key with require_signature=1 is
// rejected without a valid signature and accepted with one, while a
// non-publisher (or a key that hasn't opted in) is unaffected.
func TestResolveCaller_SignatureRequiredEndToEnd(t *testing.T) {
	setupDedupTestDB(t)
	withSigningConfig(t, RequestSigningConfig{Enabled: true, MaxSkewSecs: 300})

	db.Exec("INSERT INTO users (id, role, display_name, password_hash, disabled) VALUES (1, 'publisher', 'Pub', '', 0)")
	secret := "e2e-secret"
	db.Exec(
		"INSERT INTO api_keys (user_id, name, api_key, require_signature, signing_secret_enc) VALUES (1, 'k', ?, 1, ?)",
		hashAPIKey("ak_test123"), signingSecretEncrypt(secret),
	)

	t.Run("rejected without signature", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/events", nil)
		req.Header.Set("Authorization", "Bearer ak_test123")
		w := httptest.NewRecorder()
		ok, _ := resolveCaller(w, req)
		if ok {
			t.Error("expected resolveCaller to reject an unsigned write from a require_signature key")
		}
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
	})

	t.Run("accepted with valid signature", func(t *testing.T) {
		req := newSignedRequest(t, secret, "POST", "/api/v1/events", "ffffffffffffffffffffffffffffffff", time.Now(), "")
		req.Header.Set("Authorization", "Bearer ak_test123")
		w := httptest.NewRecorder()
		ok, _ := resolveCaller(w, req)
		if !ok {
			t.Errorf("expected resolveCaller to accept a validly-signed write, body=%s", w.Body.String())
		}
	})

	t.Run("exempt path bypasses signature check", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/publishers/token", nil)
		req.Header.Set("Authorization", "Bearer ak_test123")
		w := httptest.NewRecorder()
		ok, _ := resolveCaller(w, req)
		if !ok {
			t.Errorf("expected the exempt path to skip signature enforcement, body=%s", w.Body.String())
		}
	})
}

// TestRotateSigningSecret_RequiresCurrentSignatureWhenEnforced is a
// regression test for a security-review finding on #1366: rotating the
// signing secret must not be possible with just the bearer API key once a
// key has require_signature=1 — otherwise a leaked token alone (with no
// signing secret at all) could mint itself a fresh secret and then satisfy
// the signature check on every other write, defeating the feature's entire
// point. A key that hasn't opted into enforcement yet (require_signature=0)
// has nothing to protect, so bearer-only rotation is fine there.
func TestRotateSigningSecret_RequiresCurrentSignatureWhenEnforced(t *testing.T) {
	setupDedupTestDB(t)
	withSigningConfig(t, RequestSigningConfig{Enabled: true, MaxSkewSecs: 300})

	db.Exec("INSERT INTO users (id, role, display_name, password_hash, disabled) VALUES (1, 'publisher', 'Pub', '', 0)")
	oldSecret := "old-secret"
	db.Exec(
		"INSERT INTO api_keys (user_id, name, api_key, require_signature, signing_secret_enc) VALUES (1, 'k', ?, 1, ?)",
		hashAPIKey("ak_rotatetest"), signingSecretEncrypt(oldSecret),
	)

	t.Run("rejected with bearer token alone", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/apikeys/rotate-signing-secret", nil)
		req.Header.Set("Authorization", "Bearer ak_rotatetest")
		w := httptest.NewRecorder()
		rotateSigningSecret(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401, body=%s", w.Code, w.Body.String())
		}

		// Confirm the secret was NOT changed by the rejected attempt.
		var stillEnc string
		db.QueryRow("SELECT signing_secret_enc FROM api_keys WHERE api_key=?", hashAPIKey("ak_rotatetest")).Scan(&stillEnc)
		got, _ := signingSecretDecrypt(stillEnc)
		if got != oldSecret {
			t.Errorf("secret changed despite rejected rotation: got %q, want unchanged %q", got, oldSecret)
		}
	})

	t.Run("accepted with a valid current-secret signature", func(t *testing.T) {
		req := newSignedRequest(t, oldSecret, "POST", "/api/v1/apikeys/rotate-signing-secret", "22222222222222222222222222222222"[:32], time.Now(), "")
		req.Header.Set("Authorization", "Bearer ak_rotatetest")
		w := httptest.NewRecorder()
		rotateSigningSecret(w, req)
		if w.Code != 0 && w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "signing_secret") {
			t.Errorf("expected a new signing_secret in the response, got %s", w.Body.String())
		}
	})
}
