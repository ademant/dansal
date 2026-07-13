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

// createMagicToken generates and stores a magic login token for userID, replacing any existing one.
func createMagicToken(userID int) (token string, expiresAt time.Time, err error) {
	token, err = generateVerificationToken()
	if err != nil {
		return
	}
	expiresAt = time.Now().UTC().Add(time.Duration(config.Server.MagicLoginExpirySecs) * time.Second)
	db.Exec("DELETE FROM magic_login_tokens WHERE user_id=?", userID)
	_, err = db.Exec(
		"INSERT INTO magic_login_tokens (token, user_id, expires_at) VALUES (?, ?, ?)",
		sha256Hex(token), userID, expiresAt.Unix(),
	)
	return
}

// POST /api/v1/users/{id}/magic-link — admin generates a one-time login link for a user.
// Returns {"url":"...","expires_at":"...","sent_to":"<channel or empty>"}.
// If the user has a verified Telegram or Matrix contact, the link is sent there automatically.
// For email-less users with no messaging contact the URL is returned for the admin to share manually.
func generateAdminMagicLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("X-User-Role") != "admin" {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	targetID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}
	var userID int
	var userEmail, telegramChatID, matrixID string
	var telegramVerified, matrixVerified int
	err = db.QueryRow(
		`SELECT id, COALESCE(email,''), COALESCE(telegram_chat_id,''), COALESCE(telegram_verified,0),
		 COALESCE(matrix,''), COALESCE(matrix_verified,0)
		 FROM users WHERE id=? AND disabled=0`, targetID,
	).Scan(&userID, &userEmail, &telegramChatID, &telegramVerified, &matrixID, &matrixVerified)
	if err == sql.ErrNoRows {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	token, expiresAt, err := createMagicToken(userID)
	if err != nil {
		writeError(w, "Failed to create token", http.StatusInternalServerError)
		return
	}
	magicURL := buildBaseURL(r) + "/login/magic/" + token
	msgText := fmt.Sprintf("An admin has generated a login link for your account:\n\n%s\n\nThis link expires in %d minutes and can only be used once.",
		magicURL, config.Server.MagicLoginExpirySecs/60)

	// sentTo reflects the attempted channel, not confirmed delivery — the
	// actual send happens asynchronously below so this handler never blocks
	// on Telegram/Matrix API latency.
	sentTo := ""
	if telegramVerified == 1 && telegramChatID != "" {
		sentTo = "telegram"
		go func() {
			if err2 := sendTelegramMessage(telegramChatID, msgText); err2 != nil {
				log.Printf("magic: telegram send failed for user %d: %v", userID, err2)
			}
		}()
	} else if matrixVerified == 1 && matrixID != "" {
		sentTo = "matrix"
		go func() {
			if err2 := sendMatrixMessage(matrixID, msgText); err2 != nil {
				log.Printf("magic: matrix send failed for user %d: %v", userID, err2)
			}
		}()
	}

	log.Printf("magic: admin user %s generated login link for user %d (sent_to=%q)", r.Header.Get("X-User-ID"), userID, sentTo)
	json.NewEncoder(w).Encode(map[string]string{
		"url":        magicURL,
		"expires_at": expiresAt.Format(time.RFC3339),
		"sent_to":    sentTo,
	})
}

// POST /api/v1/login/magic — request a magic login link.
// Body: {"email":"..."}, optional "channel":"email"|"telegram".
// Always returns 204 to prevent user enumeration.
// Returns 429 when the per-user rate limit is active.
func requestMagicLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email   string `json:"email"`
		Channel string `json:"channel"` // "email" (default) or "telegram"
		BaseURL string `json:"base_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Email == "" {
		writeError(w, "email is required", http.StatusBadRequest)
		return
	}
	if req.Channel == "" {
		req.Channel = "email"
	}

	var user User
	var emailVerified, telegramVerified, matrixVerified int
	var telegramChatID, matrixID, lastMagicSentAt string

	const q = `SELECT id, email, COALESCE(display_name,''), role, created_at,
	            COALESCE(email_verified,0), COALESCE(last_magic_sent_at,''),
	            COALESCE(telegram_verified,0), COALESCE(telegram_chat_id,''),
	            COALESCE(matrix_verified,0), COALESCE(matrix,'')
	           FROM users WHERE `
	err := db.QueryRow(q+"email=?", req.Email).Scan(
		&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.CreatedAt,
		&emailVerified, &lastMagicSentAt, &telegramVerified, &telegramChatID,
		&matrixVerified, &matrixID)

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

	token, _, err := createMagicToken(user.ID)
	if err != nil {
		writeError(w, "Failed to create magic token", http.StatusInternalServerError)
		return
	}

	db.Exec("UPDATE users SET last_magic_sent_at=? WHERE id=?", time.Now().UTC().Unix(), user.ID)

	base := req.BaseURL
	if base == "" {
		base = buildBaseURL(r)
	}
	magicURL := base + "/login/magic/" + token

	msgText := fmt.Sprintf(
		"Use the link below to sign in to dansal:\n\n%s\n\nThis link expires in %d minutes and can only be used once.",
		magicURL, config.Server.MagicLoginExpirySecs/60,
	)

	// Sent asynchronously — the response doesn't wait on delivery, so a
	// failure here is logged and cleans up the token, but is never surfaced
	// to the caller (matches this handler's "always returns 204" contract).
	go func() {
		var err error
		switch req.Channel {
		case "telegram":
			err = sendTelegramMessage(telegramChatID, msgText)
		case "matrix":
			err = sendMatrixMessage(matrixID, msgText)
		default: // "email"
			_, err = SendEmail(user.Email, "Your passwordless login link", msgText, false)
		}
		if err != nil {
			db.Exec("DELETE FROM magic_login_tokens WHERE token=?", sha256Hex(token))
			log.Printf("magic: send failed for user %d (%s) via %s: %v", user.ID, user.Email, req.Channel, err)
			return
		}
		log.Printf("magic: sent login link to user %d (%s) via %s", user.ID, user.Email, req.Channel)
	}()

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/login/magic/{token} — consume a magic login token and issue a session.
func useMagicLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	token := r.PathValue("token")

	var id, userID int
	var expiresAt string
	err := db.QueryRow(
		"SELECT id, user_id, expires_at FROM magic_login_tokens WHERE token=?", sha256Hex(token),
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

	// Reset failed-login counter; magic link proves delivery-channel access
	// but must not lift an explicit admin suspension.
	db.Exec("UPDATE users SET failed_login_count=0, failed_login_since=NULL WHERE id=?", userID)
	credentials.pruneByUserID(userID)

	user, err := getUserByID(userID)
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if user.Disabled {
		writeError(w, "This account has been disabled. Please contact the administrator.", http.StatusForbidden)
		return
	}

	clientIP := getClientIP(r)
	sessionToken, sessionExpiry, err := createTokenInDB(user.ID, r.UserAgent(), clientIP, "")
	if err != nil {
		log.Printf("magic: failed to create session for user %d: %v", user.ID, err)
		writeError(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	log.Printf("magic: user %d (%s) logged in via magic link from %s", user.ID, user.Email, clientIP)
	json.NewEncoder(w).Encode(LoginResponse{
		Token:     sessionToken,
		ExpiresAt: sessionExpiry.Format(time.RFC3339),
		User:      user,
	})
}
