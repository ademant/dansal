package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

)

// sendTelegramMessage sends a plain-text message to a Telegram chat.
// chatID is the numeric Telegram chat/user ID stored as a string.
func sendTelegramMessage(chatID, text string) error {
	botToken := config.Server.TelegramBotToken
	if botToken == "" {
		return fmt.Errorf("telegram_bot_token not configured")
	}
	payload, _ := json.Marshal(map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
	apiURL := "https://api.telegram.org/bot" + botToken + "/sendMessage"
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(apiURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram API: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if !result.OK {
		return fmt.Errorf("telegram API: %s", result.Description)
	}
	return nil
}

// tgUpdate is the subset of a Telegram Update object we care about.
type tgUpdate struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		MessageID int `json:"message_id"`
		From      struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"from"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

// POST /telegram/webhook — called by Telegram for every incoming update.
// Handles /start TOKEN commands to complete Telegram verification.
func telegramWebhookHandler(w http.ResponseWriter, r *http.Request) {
	var update tgUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		w.WriteHeader(http.StatusOK) // always return 200 to Telegram
		return
	}
	if update.Message == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	text := strings.TrimSpace(update.Message.Text)
	chatID := update.Message.Chat.ID
	chatIDStr := fmt.Sprintf("%d", chatID)

	// Handle /register {token} commands.
	if strings.HasPrefix(text, "/register") {
		parts := strings.Fields(text)
		if len(parts) < 2 {
			_ = sendTelegramMessage(chatIDStr, "Send /register <token> with the token from the registration page.")
			w.WriteHeader(http.StatusOK)
			return
		}
		regToken := parts[1]
		var regID int
		var regExpires string
		var regVerified int
		regErr := db.QueryRow(
			"SELECT id, expires_at, verified FROM pending_registrations WHERE verification_token=?",
			regToken,
		).Scan(&regID, &regExpires, &regVerified)
		if regErr != nil {
			_ = sendTelegramMessage(chatIDStr, "This registration token is invalid or has already been used.")
			w.WriteHeader(http.StatusOK)
			return
		}
		regExp, err := parseTokenExpiration(regExpires)
		if err != nil || time.Now().After(regExp) {
			db.Exec("DELETE FROM pending_registrations WHERE id=?", regID)
			_ = sendTelegramMessage(chatIDStr, "This registration token has expired.")
			w.WriteHeader(http.StatusOK)
			return
		}
		if regVerified == 1 {
			_ = sendTelegramMessage(chatIDStr, "This registration has already been verified.")
			w.WriteHeader(http.StatusOK)
			return
		}
		db.Exec("UPDATE pending_registrations SET verified=1, telegram_chat_id=? WHERE id=?", chatIDStr, regID)
		log.Printf("telegram webhook: verified pending registration %d via Telegram", regID)
		_ = sendTelegramMessage(chatIDStr, "✓ Verified! Your registration is now awaiting admin approval.")
		go notifyApprovers(regID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Only handle /start commands.
	if !strings.HasPrefix(text, "/start") {
		w.WriteHeader(http.StatusOK)
		return
	}

	parts := strings.Fields(text)
	if len(parts) < 2 {
		// /start without a token — generic greeting
		_ = sendTelegramMessage(chatIDStr, "Hi! Send me a /start link from the dansal website to verify your account.")
		w.WriteHeader(http.StatusOK)
		return
	}
	token := parts[1]

	// Look up the verification token — first try user account tokens.
	var tokenID, userID int
	var channel, expiresAt string
	err := db.QueryRow(
		"SELECT id, user_id, channel, expires_at FROM verification_tokens WHERE token=? AND channel='telegram'",
		token,
	).Scan(&tokenID, &userID, &channel, &expiresAt)
	if err == sql.ErrNoRows {
		// Not a user account token. Check contact board post tokens.
		var postID int
		var postExpires string
		err2 := db.QueryRow(
			"SELECT id, expires_at FROM contact_posts WHERE verify_token=? AND email_verified=0",
			token,
		).Scan(&postID, &postExpires)
		if err2 == nil {
			exp, err3 := parseTokenExpiration(postExpires)
			if err3 != nil || time.Now().After(exp) {
				db.Exec("DELETE FROM contact_posts WHERE id=?", postID)
				_ = sendTelegramMessage(chatIDStr, "This verification link has expired.")
				w.WriteHeader(http.StatusOK)
				return
			}
			db.Exec("UPDATE contact_posts SET email_verified=1, verify_token=NULL, poster_telegram_chat_id=? WHERE id=?", chatIDStr, postID)
			log.Printf("telegram webhook: verified contact post %d", postID)
			_ = sendTelegramMessage(chatIDStr, "✓ Your contact board post has been confirmed and is now visible!")
			w.WriteHeader(http.StatusOK)
			return
		}
		if err2 != nil && err2 != sql.ErrNoRows {
			log.Printf("telegram webhook: db error checking contact post: %v", err2)
			w.WriteHeader(http.StatusOK)
			return
		}

		// Not a contact post token. Check contact request tokens.
		var crID, crPostID int
		var crExpires, crSenderEmail, crSenderTelegram, crMessage string
		err3 := db.QueryRow(
			"SELECT id, post_id, sender_email, sender_telegram, message, expires_at FROM contact_requests WHERE verify_token=?",
			token,
		).Scan(&crID, &crPostID, &crExpires, &crSenderEmail, &crSenderTelegram, &crMessage)
		if err3 == sql.ErrNoRows {
			_ = sendTelegramMessage(chatIDStr, "This verification link is invalid or has already been used.")
			w.WriteHeader(http.StatusOK)
			return
		}
		if err3 != nil {
			log.Printf("telegram webhook: db error checking contact request: %v", err3)
			w.WriteHeader(http.StatusOK)
			return
		}
		crExp, err4 := parseTokenExpiration(crExpires)
		if err4 != nil || time.Now().After(crExp) {
			db.Exec("DELETE FROM contact_requests WHERE id=?", crID)
			_ = sendTelegramMessage(chatIDStr, "This verification link has expired.")
			w.WriteHeader(http.StatusOK)
			return
		}
		var posterEmail, posterNick, posterChatID string
		db.QueryRow(
			"SELECT email, nickname, COALESCE(poster_telegram_chat_id,'') FROM contact_posts WHERE id=?", crPostID,
		).Scan(&posterEmail, &posterNick, &posterChatID)
		db.Exec("UPDATE contact_requests SET verify_token=NULL WHERE id=?", crID)
		log.Printf("telegram webhook: verified contact request %d for post %d", crID, crPostID)
		senderContact := crSenderEmail
		if crSenderTelegram != "" {
			senderContact = "@" + crSenderTelegram
		}
		if posterChatID != "" {
			msg := fmt.Sprintf("Someone replied to your board post!\n\nMessage:\n%s\n\nContact them at: %s", crMessage, senderContact)
			go func() { _ = sendTelegramMessage(posterChatID, msg) }()
		} else if posterEmail != "" {
			body := fmt.Sprintf(
				"Hello %s,\n\nSomeone saw your contact board post on dansal and wants to get in touch:\n\n---\n%s\n---\n\nYou can reach them at: %s\n\nThis message was forwarded by dansal.\n",
				posterNick, crMessage, senderContact,
			)
			go func() { _, _ = SendEmail(posterEmail, "Someone wants to contact you (dansal board)", body) }()
		}
		_ = sendTelegramMessage(chatIDStr, "✓ Your message has been confirmed and forwarded to the poster!")
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		log.Printf("telegram webhook: db error: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Check expiry.
	exp, err := parseTokenExpiration(expiresAt)
	if err != nil || time.Now().After(exp) {
		db.Exec("DELETE FROM verification_tokens WHERE id=?", tokenID)
		_ = sendTelegramMessage(chatIDStr, "This verification link has expired. Please request a new one.")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Mark verified and store chat_id.
	db.Exec("UPDATE users SET telegram_verified=1, telegram_chat_id=? WHERE id=?", chatIDStr, userID)
	db.Exec("DELETE FROM verification_tokens WHERE id=?", tokenID)
	log.Printf("telegram webhook: verified user %d, chat_id=%s", userID, chatIDStr)

	_ = sendTelegramMessage(chatIDStr, "✓ Your Telegram account has been verified!")
	w.WriteHeader(http.StatusOK)
}

// POST /api/v1/users/{id}/telegram/message — admin sends a message to a verified user.
func sendTelegramMessageToUser(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid user ID", http.StatusBadRequest)
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeError(w, "text is required", http.StatusBadRequest)
		return
	}

	var chatID string
	err = db.QueryRow("SELECT COALESCE(telegram_chat_id,'') FROM users WHERE id=?", id).Scan(&chatID)
	if err == sql.ErrNoRows {
		writeError(w, "user not found", http.StatusNotFound)
		return
	}
	if chatID == "" {
		writeError(w, "user has no verified Telegram account", http.StatusBadRequest)
		return
	}

	if err := sendTelegramMessage(chatID, req.Text); err != nil {
		log.Printf("telegram send to user %d: %v", id, err)
		writeError(w, "failed to send message: "+err.Error(), http.StatusBadGateway)
		return
	}
	log.Printf("telegram: admin sent message to user %d (chat_id=%s)", id, chatID)
	w.WriteHeader(http.StatusNoContent)
}
