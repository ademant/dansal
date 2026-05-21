package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

)

// POST /api/v1/login/magic — request a magic login link.
// Body: {"username":"..."} or {"email":"..."}, optional "channel":"email"|"telegram".
// Always returns 204 to prevent user enumeration.
// Returns 429 when the per-user rate limit is active.
func requestMagicLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Channel  string `json:"channel"` // "email" (default) or "telegram"
		BaseURL  string `json:"base_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Username == "" && req.Email == "" {
		writeError(w, "username or email is required", http.StatusBadRequest)
		return
	}
	if req.Channel == "" {
		req.Channel = "email"
	}

	var user User
	var emailVerified, telegramVerified, matrixVerified int
	var telegramChatID, matrixID, lastMagicSentAt string

	const q = `SELECT id, username, email, role, created_at,
	            COALESCE(email_verified,0), COALESCE(last_magic_sent_at,''),
	            COALESCE(telegram_verified,0), COALESCE(telegram_chat_id,''),
	            COALESCE(matrix_verified,0), COALESCE(matrix,'')
	           FROM users WHERE `
	var err error
	if req.Username != "" {
		err = db.QueryRow(q+"username=?", req.Username).Scan(
			&user.ID, &user.Username, &user.Email, &user.Role, &user.CreatedAt,
			&emailVerified, &lastMagicSentAt, &telegramVerified, &telegramChatID,
			&matrixVerified, &matrixID)
	} else {
		err = db.QueryRow(q+"email=?", req.Email).Scan(
			&user.ID, &user.Username, &user.Email, &user.Role, &user.CreatedAt,
			&emailVerified, &lastMagicSentAt, &telegramVerified, &telegramChatID,
			&matrixVerified, &matrixID)
	}

	// Anti-enumeration: silently succeed if user not found or channel not available.
	if err == sql.ErrNoRows {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	switch req.Channel {
	case "telegram":
		if telegramVerified == 0 || telegramChatID == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	case "matrix":
		if matrixVerified == 0 || matrixID == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	default: // "email"
		if emailVerified == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	// Per-user rate limit: enforce minimum seconds between magic link requests.
	rateSecs := config.Server.MagicLoginRateSecs
	if lastMagicSentAt != "" {
		if last, err := parseTokenExpiration(lastMagicSentAt); err == nil {
			retryAfter := int(time.Until(last.Add(time.Duration(rateSecs) * time.Second)).Seconds())
			if retryAfter > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				writeError(w, "Too many magic link requests", http.StatusTooManyRequests)
				return
			}
		}
	}

	token, err := generateVerificationToken()
	if err != nil {
		writeError(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(config.Server.MagicLoginExpirySecs) * time.Second)

	// Replace any existing unused magic token for this user.
	db.Exec("DELETE FROM magic_login_tokens WHERE user_id=?", user.ID)

	_, err = db.Exec(
		"INSERT INTO magic_login_tokens (token, user_id, expires_at) VALUES (?, ?, ?)",
		token, user.ID, expiresAt.Unix(),
	)
	if err != nil {
		writeError(w, "Failed to create magic token", http.StatusInternalServerError)
		return
	}

	db.Exec("UPDATE users SET last_magic_sent_at=? WHERE id=?", now.Unix(), user.ID)

	base := req.BaseURL
	if base == "" {
		base = buildMagicBase(r)
	}
	magicURL := base + "/api/v1/login/magic/" + token

	msgText := fmt.Sprintf(
		"Hello %s,\n\nclick the link below to log in without a password:\n\n%s\n\nThis link expires in %d minutes and can only be used once.",
		user.Username, magicURL, config.Server.MagicLoginExpirySecs/60,
	)

	switch req.Channel {
	case "telegram":
		if err := sendTelegramMessage(telegramChatID, msgText); err != nil {
			db.Exec("DELETE FROM magic_login_tokens WHERE token=?", token)
			log.Printf("magic: telegram send failed for user %d (%s): %v", user.ID, user.Username, err)
			writeError(w, "Failed to send login link: "+err.Error(), http.StatusBadGateway)
			return
		}
	case "matrix":
		if err := sendMatrixMessage(matrixID, msgText); err != nil {
			db.Exec("DELETE FROM magic_login_tokens WHERE token=?", token)
			log.Printf("magic: matrix send failed for user %d (%s): %v", user.ID, user.Username, err)
			writeError(w, "Failed to send login link: "+err.Error(), http.StatusBadGateway)
			return
		}
	default: // "email"
		if err := SendEmail(user.Email, "Your passwordless login link", msgText); err != nil {
			db.Exec("DELETE FROM magic_login_tokens WHERE token=?", token)
			log.Printf("magic: send failed for user %d (%s): %v", user.ID, user.Username, err)
			writeError(w, "Failed to send login link: "+err.Error(), http.StatusBadGateway)
			return
		}
	}

	log.Printf("magic: sent login link to user %d (%s) via %s", user.ID, user.Username, req.Channel)
	w.WriteHeader(http.StatusNoContent)
}

// buildMagicBase returns the base URL (scheme + host) for magic link construction.
func buildMagicBase(r *http.Request) string {
	base := config.Server.BaseURL
	if base != "" {
		return base
	}
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// GET /api/v1/login/magic/{token} — consume a magic login token and issue a session.
func useMagicLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	token := r.PathValue("token")

	var id, userID int
	var expiresAt string
	err := db.QueryRow(
		"SELECT id, user_id, expires_at FROM magic_login_tokens WHERE token=?", token,
	).Scan(&id, &userID, &expiresAt)
	if err == sql.ErrNoRows {
		writeError(w, "Invalid or expired login link", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	exp, err := parseTokenExpiration(expiresAt)
	if err != nil || time.Now().After(exp) {
		db.Exec("DELETE FROM magic_login_tokens WHERE id=?", id)
		writeError(w, "Login link has expired", http.StatusGone)
		return
	}

	// Consume the token immediately to prevent replay.
	db.Exec("DELETE FROM magic_login_tokens WHERE id=?", id)

	// Load user; re-enable if disabled (magic link proves email ownership).
	var user User
	db.Exec("UPDATE users SET disabled=0, failed_login_count=0, failed_login_since=NULL WHERE id=?", userID)
	credentials.pruneByUserID(userID)

	err = db.QueryRow(
		"SELECT id, username, email, role, created_at FROM users WHERE id=?", userID,
	).Scan(&user.ID, &user.Username, &user.Email, &user.Role, &user.CreatedAt)
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	clientIP := getClientIP(r)
	sessionToken, sessionExpiry, err := createTokenInDB(user.ID, r.UserAgent(), clientIP, "")
	if err != nil {
		log.Printf("magic: failed to create session for user %d: %v", user.ID, err)
		writeError(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	log.Printf("magic: user %d (%s) logged in via magic link from %s", user.ID, user.Username, clientIP)
	json.NewEncoder(w).Encode(LoginResponse{
		Token:     sessionToken,
		ExpiresAt: sessionExpiry.Format(time.RFC3339),
		User:      user,
	})
}
