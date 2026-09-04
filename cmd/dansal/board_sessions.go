package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

const (
	bsSliding  = 30 * 24 * time.Hour // sliding expiry window
	bsAbsolute = 90 * 24 * time.Hour // absolute max from session creation
	bsRenewTTL = 30 * time.Minute    // one-time renew-link lifetime
)

// lookupBoardSession reads the X-Board-Session header, validates the token
// hash against verified_email_sessions, slides expires_at within the absolute
// cap, updates last_seen_at, and returns (id, email, nickname, ok).
// ok is false when the header is absent, the session is not found, or it has
// expired (either sliding or absolute).
func lookupBoardSession(r *http.Request) (id int, email, nickname string, ok bool) {
	raw := r.Header.Get("X-Board-Session")
	if raw == "" {
		return
	}
	h := sha256Hex(raw)
	now := time.Now().Unix()
	var absoluteExpiry, expiresAt int64
	err := db.QueryRow(
		`SELECT id, email, nickname, absolute_expiry, expires_at
		 FROM verified_email_sessions WHERE token_hash=? AND expires_at>? AND absolute_expiry>?`,
		h, now, now,
	).Scan(&id, &email, &nickname, &absoluteExpiry, &expiresAt)
	if err != nil {
		return
	}
	// Slide the expiry forward, capped by the absolute limit.
	newExpiry := min(now+int64(bsSliding.Seconds()), absoluteExpiry)
	db.Exec("UPDATE verified_email_sessions SET expires_at=?, last_seen_at=? WHERE id=?", newExpiry, now, id)
	ok = true
	return
}

// POST /api/v1/board-sessions
// Body: {"manage_token": "..."}
// Validates manage_token (must be a live email-verified contact post), creates
// a verified_email_sessions row, and returns {"token","expires_at"}.
func createBoardSessionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		ManageToken string `json:"manage_token"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.ManageToken == "" {
		writeError(w, "manage_token is required", http.StatusBadRequest)
		return
	}

	// Resolve manage_token to email+nickname; post must be live and verified.
	now := time.Now().Unix()
	var email, nickname string
	var emailVerified int
	err := db.QueryRow(
		`SELECT COALESCE(email,''), COALESCE(nickname,''), email_verified
		 FROM contact_posts WHERE manage_token=? AND expires_at>?`,
		req.ManageToken, now,
	).Scan(&email, &nickname, &emailVerified)
	if err != nil || email == "" || emailVerified == 0 {
		writeError(w, "invalid or unverified manage token", http.StatusUnauthorized)
		return
	}

	raw, err := generateToken(32)
	if err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := sha256Hex(raw)

	created := now
	absoluteExpiry := created + int64(bsAbsolute.Seconds())
	expiresAt := min(created+int64(bsSliding.Seconds()), absoluteExpiry)

	// Upsert: if the same email already has a session, replace its token so
	// the old cookie is invalidated and the consent page issues a fresh one.
	_, err = db.Exec(
		`INSERT INTO verified_email_sessions
		 (token_hash, email, nickname, created_at, absolute_expiry, expires_at, last_seen_at)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT DO NOTHING`,
		h, email, nickname, created, absoluteExpiry, expiresAt, created,
	)
	if err != nil {
		// Very rare hash collision; surface as internal error.
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}

	expiresTime := time.Unix(expiresAt, 0).UTC()
	json.NewEncoder(w).Encode(map[string]string{
		"token":      raw,
		"expires_at": expiresTime.Format(time.RFC3339),
	})
}

// GET /api/v1/board-sessions/me
// Header: X-Board-Session: <token>
// Returns {"email","nickname","expires_at"} or 401.
func getBoardSessionMeHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, email, nickname, ok := lookupBoardSession(r)
	if !ok {
		writeError(w, "invalid or expired session", http.StatusUnauthorized)
		return
	}
	// Re-read the fresh expires_at after the slide.
	var expiresAt int64
	db.QueryRow(
		"SELECT expires_at FROM verified_email_sessions WHERE token_hash=?",
		sha256Hex(r.Header.Get("X-Board-Session")),
	).Scan(&expiresAt)
	json.NewEncoder(w).Encode(map[string]string{
		"email":      email,
		"nickname":   nickname,
		"expires_at": time.Unix(expiresAt, 0).UTC().Format(time.RFC3339),
	})
}

// DELETE /api/v1/board-sessions/me
// Header: X-Board-Session: <token>
// Deletes the session row. Always 204.
func deleteBoardSessionMeHandler(w http.ResponseWriter, r *http.Request) {
	raw := r.Header.Get("X-Board-Session")
	if raw != "" {
		db.Exec("DELETE FROM verified_email_sessions WHERE token_hash=?", sha256Hex(raw))
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/board-sessions/renew-request
// Body: {"email":"...","base_url":"..."}
// If email is known (has a live email-verified board post), sends a one-time
// renew link. Always returns 200 (enumeration resistance).
func requestBoardSessionRenewHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		Email   string `json:"email"`
		BaseURL string `json:"base_url"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	// Background: only send if email is known.
	if req.Email == "" || !smtpEnabled() {
		return
	}
	now := time.Now().Unix()
	var count int
	db.QueryRow(
		"SELECT COUNT(*) FROM contact_posts WHERE LOWER(email)=LOWER(?) AND email_verified=1 AND expires_at>?",
		req.Email, now,
	).Scan(&count)
	if count == 0 {
		return
	}

	raw, err := generateToken(32)
	if err != nil {
		log.Printf("board-sessions: generate renew token: %v", err)
		return
	}
	expiresAt := now + int64(bsRenewTTL.Seconds())
	if _, err := db.Exec(
		"INSERT OR REPLACE INTO verified_email_session_renew_tokens (token_hash, email, expires_at) VALUES (?,?,?)",
		sha256Hex(raw), req.Email, expiresAt,
	); err != nil {
		log.Printf("board-sessions: store renew token: %v", err)
		return
	}

	base := req.BaseURL
	if base == "" {
		base = publicBaseURL()
	}
	renewURL := fmt.Sprintf("%s/board/renew-session/%s", base, raw)
	body := fmt.Sprintf(
		"Hello,\n\nUse this link to restore your board access on this browser:\n\n%s\n\nThe link expires in 30 minutes. If you did not request this, you can ignore this email.\n",
		renewURL,
	)
	go func() {
		if _, err := SendEmail(req.Email, "Restore your board access", body, false); err != nil {
			log.Printf("board-sessions: renew email to %s: %v", req.Email, err)
		}
	}()
}

// GET /api/v1/board-sessions/renew/{token}
// Consumes a single-use renew token and issues a new verified_email_sessions row.
// Returns {"token","expires_at"} on success, 401 if invalid/expired.
func useBoardSessionRenewHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	raw := r.PathValue("token")
	if raw == "" {
		writeError(w, "token required", http.StatusBadRequest)
		return
	}
	h := sha256Hex(raw)
	now := time.Now().Unix()

	var email string
	var expiresAt int64
	err := db.QueryRow(
		"SELECT email, expires_at FROM verified_email_session_renew_tokens WHERE token_hash=?", h,
	).Scan(&email, &expiresAt)
	if err != nil || now > expiresAt {
		// Consume regardless (idempotent cleanup) and return 401.
		db.Exec("DELETE FROM verified_email_session_renew_tokens WHERE token_hash=?", h)
		writeError(w, "renew link is invalid or expired", http.StatusUnauthorized)
		return
	}
	// Single-use: delete immediately.
	db.Exec("DELETE FROM verified_email_session_renew_tokens WHERE token_hash=?", h)

	// Look up nickname from any live post for this email.
	var nickname string
	db.QueryRow(
		"SELECT COALESCE(nickname,'') FROM contact_posts WHERE LOWER(email)=LOWER(?) AND email_verified=1 AND expires_at>? ORDER BY created_at DESC LIMIT 1",
		email, now,
	).Scan(&nickname)

	sessionRaw, err := generateToken(32)
	if err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}
	sessionHash := sha256Hex(sessionRaw)
	absolute := now + int64(bsAbsolute.Seconds())
	sliding := min(now+int64(bsSliding.Seconds()), absolute)
	if _, err := db.Exec(
		`INSERT INTO verified_email_sessions
		 (token_hash, email, nickname, created_at, absolute_expiry, expires_at, last_seen_at)
		 VALUES (?,?,?,?,?,?,?)`,
		sessionHash, email, nickname, now, absolute, sliding, now,
	); err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{
		"token":      sessionRaw,
		"expires_at": time.Unix(sliding, 0).UTC().Format(time.RFC3339),
	})
}

// cleanExpiredBoardSessions removes expired rows from both verified_email_sessions
// and verified_email_session_renew_tokens. Called from the hourly cleanup goroutine.
func cleanExpiredBoardSessions(now int64) {
	db.Exec("DELETE FROM verified_email_sessions WHERE expires_at < ? AND absolute_expiry < ?", now, now)
	db.Exec("DELETE FROM verified_email_session_renew_tokens WHERE expires_at < ?", now)
}
