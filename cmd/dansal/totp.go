package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// callerUserID extracts the authenticated user ID from the request.
// Returns 0 when not set (should never happen inside TokenMiddleware-wrapped handlers).
func callerUserID(r *http.Request) int {
	id, _ := callerFromRequest(r)
	return id
}

// totpGenerateFromKey generates a 6-digit TOTP code per RFC 6238 / RFC 4226
// from an already-decoded key.
func totpGenerateFromKey(key []byte, t time.Time) string {
	counter := uint64(t.Unix()) / 30
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)
	h := hmac.New(sha1.New, key)
	h.Write(buf)
	sum := h.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := (int(sum[offset])&0x7f)<<24 |
		(int(sum[offset+1])&0xff)<<16 |
		(int(sum[offset+2])&0xff)<<8 |
		(int(sum[offset+3]) & 0xff)
	return fmt.Sprintf("%06d", code%1_000_000)
}

// totpGenerate generates a 6-digit TOTP code per RFC 6238 / RFC 4226.
func totpGenerate(secretBase32 string, t time.Time) (string, error) {
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secretBase32))
	if err != nil {
		return "", err
	}
	return totpGenerateFromKey(secret, t), nil
}

// totpValid checks code against ±1 time window (30 s each) to tolerate clock skew.
// For authentication use totpCheckAndMark, which also enforces replay protection.
func totpValid(secretBase32, code string, t time.Time) bool {
	code = strings.TrimSpace(code)
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secretBase32))
	if err != nil {
		return false
	}
	for _, offsetSecs := range []int64{-30, 0, 30} {
		if totpGenerateFromKey(key, t.Add(time.Duration(offsetSecs)*time.Second)) == code {
			return true
		}
	}
	return false
}

// totpCheckAndMark validates code and, if correct, atomically records it as used
// to prevent replay within the 90-second acceptance window (±1 step).
// Returns false for an invalid code or a code that was already used.
func totpCheckAndMark(userID int, secretBase32, code string, t time.Time) bool {
	if !totpValid(secretBase32, code, t) {
		return false
	}
	// The acceptance window spans three 30-second steps; expire records after 120s to be safe.
	expiry := t.Unix() + 120
	db.Exec("DELETE FROM totp_used_codes WHERE expires_at < ?", t.Unix())
	res, err := db.Exec(
		"INSERT OR IGNORE INTO totp_used_codes (user_id, code, expires_at) VALUES (?, ?, ?)",
		userID, code, expiry,
	)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0 // 0 rows = duplicate code rejected
}

// totpNewSecret generates a random 20-byte secret encoded as base32 (no padding).
func totpNewSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// totpURI builds the otpauth:// URI for QR code generation.
func totpURI(secretBase32, accountName, issuer string) string {
	u := url.URL{
		Scheme: "otpauth",
		Host:   "totp",
		Path:   "/" + url.PathEscape(issuer+":"+accountName),
	}
	q := url.Values{}
	q.Set("secret", secretBase32)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	u.RawQuery = q.Encode()
	return u.String()
}

// totpIssuer returns the TOTP issuer name from config or falls back to "Dansal".
func totpIssuer() string {
	if config != nil && config.Server.WebAuthnRPName != "" {
		return config.Server.WebAuthnRPName
	}
	return "Dansal"
}

// GET /api/v1/auth/totp/setup — generate a new TOTP secret for the current user.
// Stores the pending secret in users.totp_pending; returns {secret, uri}.
func totpSetupHandler(w http.ResponseWriter, r *http.Request) {
	userID := callerUserID(r)
	var email string
	db.QueryRow("SELECT COALESCE(email,'') FROM users WHERE id=?", userID).Scan(&email)

	secret, err := totpNewSecret()
	if err != nil {
		writeError(w, "could not generate secret", http.StatusInternalServerError)
		return
	}

	if _, err := db.Exec("UPDATE users SET totp_pending=? WHERE id=?", secret, userID); err != nil {
		writeError(w, "could not store pending secret", http.StatusInternalServerError)
		return
	}

	accountName := email
	if accountName == "" {
		accountName = fmt.Sprintf("user-%d", userID)
	}
	uri := totpURI(secret, accountName, totpIssuer())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"secret": secret, "uri": uri})
}

// POST /api/v1/auth/totp/confirm — verify a TOTP code and activate the pending secret.
func totpConfirmHandler(w http.ResponseWriter, r *http.Request) {
	userID := callerUserID(r)
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	if req.Code == "" {
		writeError(w, "code is required", http.StatusBadRequest)
		return
	}

	var pending string
	err := db.QueryRow("SELECT COALESCE(totp_pending,'') FROM users WHERE id=?", userID).Scan(&pending)
	if err != nil || pending == "" {
		writeError(w, "no TOTP setup in progress — call /auth/totp/setup first", http.StatusBadRequest)
		return
	}

	if !totpValid(pending, req.Code, time.Now()) {
		writeError(w, "invalid TOTP code", http.StatusUnauthorized)
		return
	}

	if _, err := db.Exec("UPDATE users SET totp_secret=totp_pending, totp_pending=NULL WHERE id=?", userID); err != nil {
		writeError(w, "could not activate TOTP", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// DELETE /api/v1/auth/totp — disable TOTP for the current user; requires current TOTP code.
func totpDisableHandler(w http.ResponseWriter, r *http.Request) {
	userID := callerUserID(r)
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	if req.Code == "" {
		writeError(w, "code is required", http.StatusBadRequest)
		return
	}

	var secret sql.NullString
	if err := db.QueryRow("SELECT totp_secret FROM users WHERE id=?", userID).Scan(&secret); err != nil || !secret.Valid || secret.String == "" {
		writeError(w, "TOTP is not enabled", http.StatusBadRequest)
		return
	}

	if !totpCheckAndMark(userID, secret.String, req.Code, time.Now()) {
		writeError(w, "invalid TOTP code", http.StatusUnauthorized)
		return
	}

	if _, err := db.Exec("UPDATE users SET totp_secret=NULL, totp_pending=NULL WHERE id=?", userID); err != nil {
		writeError(w, "could not disable TOTP", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
