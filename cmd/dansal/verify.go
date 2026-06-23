package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// buildBaseURL returns the public base URL for link generation.
// Priority: configured base_url > X-Base-URL header (set by dansal-web) > infer from request.
func buildBaseURL(r *http.Request) string {
	if base := strings.TrimRight(config.Server.BaseURL, "/"); base != "" {
		return base
	}
	if base := strings.TrimRight(r.Header.Get("X-Base-URL"), "/"); base != "" {
		return base
	}
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// buildVerifyURL constructs the verification link using buildBaseURL.
// Uses /verify/{token} (served by dansal-web as an HTML page) rather than
// the /api/ path, which nginx routes directly to the API returning raw JSON.
func buildVerifyURL(r *http.Request, token string) string {
	return buildBaseURL(r) + "/verify/" + token
}

func generateVerificationToken() (string, error) {
	return generateToken(24)
}

// POST /api/v1/users/{id}/verify — generate and send a verification link.
// Callers may only verify their own account unless they are admin.
func sendVerification(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	targetID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}
	if callerID != targetID && callerRole != RoleAdmin {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		Channel string `json:"channel"`
		BaseURL string `json:"base_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Channel == "" {
		writeError(w, "channel is required (email, telegram, matrix)", http.StatusBadRequest)
		return
	}
	if req.Channel != "email" && req.Channel != "telegram" && req.Channel != "matrix" {
		writeError(w, "channel must be one of: email, telegram, matrix", http.StatusBadRequest)
		return
	}

	user, err := getUserByID(targetID)
	if err == sql.ErrNoRows {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	switch req.Channel {
	case "email":
		if user.Email == "" {
			writeError(w, "User has no email address", http.StatusBadRequest)
			return
		}
	case "telegram":
		if user.Telegram == "" {
			writeError(w, "User has no Telegram handle", http.StatusBadRequest)
			return
		}
	case "matrix":
		if user.Matrix == "" {
			writeError(w, "User has no Matrix ID", http.StatusBadRequest)
			return
		}
		if !isValidMatrixID(user.Matrix) {
			writeError(w, "Invalid Matrix ID format (must be @localpart:server) — update it in settings first", http.StatusBadRequest)
			return
		}
	}

	// Replace any existing pending token for this user+channel.
	db.Exec("DELETE FROM verification_tokens WHERE user_id=? AND channel=?", targetID, req.Channel)

	token, err := generateVerificationToken()
	if err != nil {
		writeError(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}
	expiresAt := time.Now().UTC().Add(time.Duration(config.Server.VerificationExpiryHours) * time.Hour)
	_, err = db.Exec(
		"INSERT INTO verification_tokens (token, user_id, channel, expires_at) VALUES (?, ?, ?, ?)",
		token, targetID, req.Channel, expiresAt.Unix(),
	)
	if err != nil {
		writeError(w, "Failed to create verification token", http.StatusInternalServerError)
		return
	}

	var vURL string
	if req.BaseURL != "" {
		vURL = strings.TrimRight(req.BaseURL, "/") + "/verify/" + token
	} else {
		vURL = buildVerifyURL(r, token)
	}

	// Telegram uses a deep link: the bot cannot push to users who haven't started it.
	// Return the link so the frontend can show it to the user.
	if req.Channel == "telegram" {
		botName := config.Server.TelegramBotName
		if botName == "" {
			db.Exec("DELETE FROM verification_tokens WHERE token=?", token)
			writeError(w, "telegram_bot_name not configured", http.StatusInternalServerError)
			return
		}
		deepLink := "https://t.me/" + botName + "?start=" + token
		log.Printf("verify: generated telegram deep link for user %d (%s)", targetID, user.Email)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"deep_link": deepLink})
		return
	}

	var sendErr error
	switch req.Channel {
	case "email":
		var msgID string
		msgID, sendErr = sendEmailVerification(user, vURL)
		if sendErr == nil {
			db.Exec("UPDATE verification_tokens SET message_id=? WHERE token=?", msgID, token)
		}
	case "matrix":
		sendErr = sendMatrixVerification(user, vURL)
	}
	if sendErr != nil {
		db.Exec("DELETE FROM verification_tokens WHERE token=?", token)
		log.Printf("verify: send failed for user %d channel %s: %v", targetID, req.Channel, sendErr)
		writeError(w, "Failed to send verification: "+sendErr.Error(), http.StatusBadGateway)
		return
	}

	log.Printf("verify: sent %s verification to user %d (%s)", req.Channel, targetID, user.Email)
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/verify/{token} — public; marks the account verified and consumes the token.
func consumeVerification(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	token := r.PathValue("token")

	var id, userID int
	var channel, expiresAt string
	err := db.QueryRow(
		"SELECT id, user_id, channel, expires_at FROM verification_tokens WHERE token=?", token,
	).Scan(&id, &userID, &channel, &expiresAt)
	if err == sql.ErrNoRows {
		writeError(w, "Invalid or expired verification link", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	exp, err := parseTokenExpiration(expiresAt)
	if err != nil || time.Now().After(exp) {
		db.Exec("DELETE FROM verification_tokens WHERE id=?", id)
		writeError(w, "Verification link has expired", http.StatusGone)
		return
	}

	col := map[string]string{
		"email":    "email_verified",
		"telegram": "telegram_verified",
		"matrix":   "matrix_verified",
	}[channel]
	if col == "" {
		writeError(w, "Unknown channel", http.StatusInternalServerError)
		return
	}

	db.Exec(fmt.Sprintf("UPDATE users SET %s=1 WHERE id=?", col), userID)
	db.Exec("DELETE FROM verification_tokens WHERE id=?", id)
	log.Printf("verify: %s verified for user %d", channel, userID)

	json.NewEncoder(w).Encode(map[string]string{"channel": channel, "status": "verified"})
}

func sendEmailVerification(user User, verifyURL string) (string, error) {
	body := fmt.Sprintf(
		"Hello %s,\n\nplease verify your email address:\n\n%s\n\nThis link expires in %d hours.\n",
		user.DisplayOrEmail(), verifyURL, config.Server.VerificationExpiryHours,
	)
	return SendEmail(user.Email, "Verify your email address", body, false)
}

func sendTelegramVerification(user User, verifyURL string) error {
	botToken := config.Server.TelegramBotToken
	if botToken == "" {
		return fmt.Errorf("telegram_bot_token not configured")
	}
	text := fmt.Sprintf(
		"Hello %s, please verify your Telegram account:\n\n%s\n\nThis link expires in %d hours.",
		user.DisplayOrEmail(), verifyURL, config.Server.VerificationExpiryHours,
	)
	payload, _ := json.Marshal(map[string]any{
		"chat_id": user.Telegram,
		"text":    text,
	})
	apiURL := "https://api.telegram.org/bot" + botToken + "/sendMessage"
	resp, err := http.Post(apiURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("Telegram API: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if !result.OK {
		return fmt.Errorf("Telegram API: %s", result.Description)
	}
	return nil
}

// sendMatrixMessage opens a DM room with matrixID and sends text.
func sendMatrixMessage(matrixID, text string) error {
	homeserver := strings.TrimRight(config.Server.MatrixHomeserver, "/")
	accessToken := config.Server.MatrixAccessToken
	if homeserver == "" || accessToken == "" {
		return fmt.Errorf("matrix_homeserver or matrix_access_token not configured")
	}

	client := &http.Client{Timeout: 30 * time.Second}

	// Create a minimal private room — no preset, no inline invite.
	// "trusted_private_chat" is not recognised by all Conduit versions and
	// triggers M_BAD_JSON; splitting create + invite avoids the issue.
	createBody, _ := json.Marshal(map[string]any{
		"is_direct":  true,
		"visibility": "private",
	})
	req, _ := http.NewRequest("POST", homeserver+"/_matrix/client/v3/createRoom", bytes.NewReader(createBody))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Matrix createRoom: %w", err)
	}
	defer resp.Body.Close()

	var roomResult struct {
		RoomID  string `json:"room_id"`
		Error   string `json:"error"`
		ErrCode string `json:"errcode"`
	}
	json.NewDecoder(resp.Body).Decode(&roomResult)
	if roomResult.RoomID == "" {
		return fmt.Errorf("Matrix createRoom failed: %s: %s", roomResult.ErrCode, roomResult.Error)
	}

	// Invite the target user into the room.
	inviteBody, _ := json.Marshal(map[string]string{"user_id": matrixID})
	inviteURL := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/invite",
		homeserver, url.PathEscape(roomResult.RoomID))
	req3, _ := http.NewRequest("POST", inviteURL, bytes.NewReader(inviteBody))
	req3.Header.Set("Authorization", "Bearer "+accessToken)
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := client.Do(req3)
	if err != nil {
		return fmt.Errorf("Matrix invite: %w", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode >= 300 {
		var invErr struct {
			ErrCode string `json:"errcode"`
			Error   string `json:"error"`
		}
		json.NewDecoder(resp3.Body).Decode(&invErr)
		return fmt.Errorf("Matrix invite failed: %s: %s", invErr.ErrCode, invErr.Error)
	}

	txnID := strconv.FormatInt(time.Now().UnixNano(), 10)
	sendURL := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s",
		homeserver, url.PathEscape(roomResult.RoomID), txnID)
	msgBody, _ := json.Marshal(map[string]any{"msgtype": "m.text", "body": text})
	req2, _ := http.NewRequest("PUT", sendURL, bytes.NewReader(msgBody))
	req2.Header.Set("Authorization", "Bearer "+accessToken)
	req2.Header.Set("Content-Type", "application/json")

	resp2, err := client.Do(req2)
	if err != nil {
		return fmt.Errorf("Matrix send message: %w", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode >= 300 {
		return fmt.Errorf("Matrix send message: HTTP %d", resp2.StatusCode)
	}
	return nil
}

func sendMatrixVerification(user User, verifyURL string) error {
	return sendMatrixMessage(user.Matrix, fmt.Sprintf(
		"Hello %s, please verify your Matrix account:\n\n%s\n\nThis link expires in %d hours.",
		user.DisplayOrEmail(), verifyURL, config.Server.VerificationExpiryHours,
	))
}
