package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

)

type ContactPost struct {
	ID               int    `json:"id"`
	EventID          int    `json:"event_id"`
	Type             string `json:"type"`
	City             string `json:"city"`
	Persons          int    `json:"persons"`
	Message          string `json:"message,omitempty"`
	Nickname         string `json:"nickname"`
	TelegramUsername string `json:"telegram_username,omitempty"`
	EmailVerified    bool   `json:"email_verified"`
	CreatedAt        string `json:"created_at"`
}

// containsLink returns true if s contains any URL or mailto: link.
func containsLink(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "http://") || strings.Contains(lower, "https://") || strings.Contains(lower, "mailto:")
}

// computeContactPostExpiry returns the earlier of (now+30 days) and (event end_time+3 days).
func computeContactPostExpiry(eventID int) time.Time {
	ceiling := time.Now().UTC().Add(30 * 24 * time.Hour)
	var endTimeStr string
	if err := db.QueryRow("SELECT end_time FROM events WHERE id=?", eventID).Scan(&endTimeStr); err == nil {
		// end_time is stored as a Unix timestamp integer in the events table.
		if ts, err := strconv.ParseInt(strings.TrimSpace(endTimeStr), 10, 64); err == nil {
			candidate := time.Unix(ts, 0).UTC().Add(3 * 24 * time.Hour)
			if candidate.Before(ceiling) {
				return candidate
			}
		}
	}
	return ceiling
}

// isOrgMemberOfEvent returns true when userID is a member of the organisation
// that owns the given event.
func isOrgMemberOfEvent(userID, eventID int) bool {
	var orgID int
	err := db.QueryRow("SELECT COALESCE(organization_id,0) FROM events WHERE id=?", eventID).Scan(&orgID)
	if err != nil || orgID == 0 {
		return false
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM organization_members WHERE organization_id=? AND user_id=?", orgID, userID).Scan(&count)
	return count > 0
}

// GET /api/v1/events/{id}/contact-posts
// Public. Returns only email-verified posts; email field is never returned.
func listContactPosts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}

	rows, err := db.Query(
		`SELECT id, event_id, type, city, persons, COALESCE(message,''), nickname, COALESCE(telegram_username,''), email_verified, created_at
		 FROM contact_posts
		 WHERE event_id=? AND email_verified=1 AND expires_at > ?
		 ORDER BY created_at ASC`,
		eventID, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	posts := []ContactPost{}
	for rows.Next() {
		var p ContactPost
		var ev int
		if err := rows.Scan(&p.ID, &p.EventID, &p.Type, &p.City, &p.Persons, &p.Message, &p.Nickname, &p.TelegramUsername, &ev, &p.CreatedAt); err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		p.EmailVerified = ev == 1
		posts = append(posts, p)
	}
	json.NewEncoder(w).Encode(posts)
}

// POST /api/v1/events/{id}/contact-posts
// Public. Creates an unverified post and sends a verification email.
func createContactPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}

	// Check event exists.
	var dummy int
	if err := db.QueryRow("SELECT id FROM events WHERE id=?", eventID).Scan(&dummy); err == sql.ErrNoRows {
		writeError(w, "event not found", http.StatusNotFound)
		return
	}

	var req struct {
		Type     string `json:"type"`
		City     string `json:"city"`
		Persons  int    `json:"persons"`
		Message  string `json:"message"`
		Nickname string `json:"nickname"`
		Email    string `json:"email"`
		Telegram string `json:"telegram"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate required fields.
	req.Type = strings.TrimSpace(req.Type)
	req.City = strings.TrimSpace(req.City)
	req.Nickname = strings.TrimSpace(req.Nickname)
	req.Email = strings.TrimSpace(req.Email)
	req.Telegram = strings.TrimPrefix(strings.TrimSpace(req.Telegram), "@")
	if req.Type == "" || req.City == "" || req.Nickname == "" {
		writeError(w, "type, city and nickname are required", http.StatusBadRequest)
		return
	}
	if req.Email == "" && req.Telegram == "" {
		writeError(w, "email or telegram username is required", http.StatusBadRequest)
		return
	}
	validTypes := map[string]bool{"ride_offer": true, "ride_request": true, "sleep_offer": true, "sleep_request": true, "ticket_offer": true, "ticket_request": true}
	if !validTypes[req.Type] {
		writeError(w, "type must be one of: ride_offer, ride_request, sleep_offer, sleep_request, ticket_offer, ticket_request", http.StatusBadRequest)
		return
	}
	if req.Email != "" && !strings.Contains(req.Email, "@") {
		writeError(w, "invalid email address", http.StatusBadRequest)
		return
	}
	if containsLink(req.Message) {
		writeError(w, "message must not contain links", http.StatusBadRequest)
		return
	}
	useTelegram := req.Telegram != ""
	if useTelegram && config.Server.TelegramBotName == "" {
		writeError(w, "telegram not configured on this server", http.StatusBadRequest)
		return
	}
	if req.Persons < 1 {
		req.Persons = 1
	}

	verifyToken, err := generateVerificationToken()
	if err != nil {
		writeError(w, "failed to generate token", http.StatusInternalServerError)
		return
	}
	deleteToken, err := generateVerificationToken()
	if err != nil {
		writeError(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	expiresAt := computeContactPostExpiry(eventID)

	result, err := db.Exec(
		`INSERT INTO contact_posts (event_id, type, city, persons, message, nickname, email, telegram_username, verify_token, delete_token, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, req.Type, req.City, req.Persons, req.Message, req.Nickname, req.Email, req.Telegram,
		verifyToken, deleteToken, expiresAt.Format(time.RFC3339),
	)
	if err != nil {
		writeError(w, "failed to create post", http.StatusInternalServerError)
		return
	}
	id, _ := result.LastInsertId()

	base := buildBaseURL(r)
	deleteURL := base + "/contact-posts/delete/" + deleteToken

	if useTelegram {
		// Return a t.me link; the user opens the bot which sends /start TOKEN to verify.
		botURL := "https://t.me/" + config.Server.TelegramBotName + "?start=" + verifyToken
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"id":                  id,
			"message":             "Open the Telegram bot to confirm your post.",
			"telegram_verify_url": botURL,
			"delete_url":          deleteURL,
		})
		return
	}

	// Email verification path — link to the web frontend so users see a proper page.
	verifyURL := base + "/contact-posts/verify/" + verifyToken
	emailBody := fmt.Sprintf(
		"Hello %s,\n\nPlease confirm your contact board post by clicking this link:\n\n%s\n\nYour post will become visible once confirmed.\n\nTo delete your post at any time use:\n\n%s\n\nThis post expires on %s.\n",
		req.Nickname, verifyURL, deleteURL, expiresAt.Format("2006-01-02"),
	)
	go func() {
		if err := SendEmail(req.Email, "Confirm your contact board post", emailBody); err != nil {
			log.Printf("contact_posts: verify email failed for post %d: %v", id, err)
		}
	}()

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"id":      id,
		"message": "A confirmation email has been sent. Your post will appear once verified.",
	})
}

// GET /api/v1/contact-posts/verify/{token}
// Public. Marks the post as verified.
func verifyContactPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	token := r.PathValue("token")

	var id int
	var expiresAt string
	err := db.QueryRow(
		"SELECT id, expires_at FROM contact_posts WHERE verify_token=?", token,
	).Scan(&id, &expiresAt)
	if err == sql.ErrNoRows {
		writeError(w, "invalid or already used verification link", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	exp, err := parseTokenExpiration(expiresAt)
	if err != nil || time.Now().After(exp) {
		db.Exec("DELETE FROM contact_posts WHERE id=?", id)
		writeError(w, "verification link has expired", http.StatusGone)
		return
	}

	db.Exec("UPDATE contact_posts SET email_verified=1, verify_token=NULL WHERE id=?", id)
	log.Printf("contact_posts: verified post %d", id)
	json.NewEncoder(w).Encode(map[string]string{"status": "verified"})
}

// GET /api/v1/contact-posts/delete/{token}
// Public. Lets the original poster delete their own post via the link in the email.
func deleteContactPostByToken(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	var id int
	err := db.QueryRow("SELECT id FROM contact_posts WHERE delete_token=?", token).Scan(&id)
	if err == sql.ErrNoRows {
		writeError(w, "invalid delete link", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	db.Exec("DELETE FROM contact_posts WHERE id=?", id)
	log.Printf("contact_posts: self-deleted post %d", id)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/contact-posts/{id}
// Requires auth. Allowed for: admin role, or org member of the event's organisation.
func deleteContactPost(w http.ResponseWriter, r *http.Request) {
	callerID, _ := strconv.Atoi(r.Header.Get("X-User-ID"))
	callerRole := r.Header.Get("X-User-Role")

	postID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid post id", http.StatusBadRequest)
		return
	}

	var eventID int
	err = db.QueryRow("SELECT event_id FROM contact_posts WHERE id=?", postID).Scan(&eventID)
	if err == sql.ErrNoRows {
		writeError(w, "post not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if callerRole != RoleAdmin && !isOrgMemberOfEvent(callerID, eventID) {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	db.Exec("DELETE FROM contact_posts WHERE id=?", postID)
	log.Printf("contact_posts: admin/org deleted post %d by user %d", postID, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/contact-posts/{id}/contact
// Public. Creates a pending contact request; sends a verification email or Telegram link to sender.
func contactPoster(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	postID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid post id", http.StatusBadRequest)
		return
	}

	var req struct {
		Email    string `json:"email"`
		Telegram string `json:"telegram"`
		Message  string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	req.Telegram = strings.TrimPrefix(strings.TrimSpace(req.Telegram), "@")
	req.Message = strings.TrimSpace(req.Message)

	if req.Message == "" {
		writeError(w, "message is required", http.StatusBadRequest)
		return
	}
	if req.Email == "" && req.Telegram == "" {
		writeError(w, "email or telegram is required", http.StatusBadRequest)
		return
	}
	if req.Email != "" && !strings.Contains(req.Email, "@") {
		writeError(w, "invalid email address", http.StatusBadRequest)
		return
	}
	if containsLink(req.Message) {
		writeError(w, "message must not contain links", http.StatusBadRequest)
		return
	}
	useTelegram := req.Telegram != ""
	if useTelegram && config.Server.TelegramBotName == "" {
		writeError(w, "telegram not configured on this server", http.StatusBadRequest)
		return
	}

	var posterEmail, posterNick, posterChatID string
	var emailVerified int
	var expiresAt string
	err = db.QueryRow(
		"SELECT email, nickname, email_verified, expires_at, COALESCE(poster_telegram_chat_id,'') FROM contact_posts WHERE id=?", postID,
	).Scan(&posterEmail, &posterNick, &emailVerified, &expiresAt, &posterChatID)
	if err == sql.ErrNoRows {
		writeError(w, "post not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if emailVerified == 0 {
		writeError(w, "post not verified", http.StatusBadRequest)
		return
	}
	exp, err := parseTokenExpiration(expiresAt)
	if err != nil || time.Now().After(exp) {
		writeError(w, "post has expired", http.StatusGone)
		return
	}

	verifyToken, err := generateVerificationToken()
	if err != nil {
		writeError(w, "failed to generate token", http.StatusInternalServerError)
		return
	}
	reqExpiry := time.Now().UTC().Add(24 * time.Hour)
	_, err = db.Exec(
		`INSERT INTO contact_requests (post_id, sender_email, sender_telegram, message, verify_token, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		postID, req.Email, req.Telegram, req.Message, verifyToken, reqExpiry.Format(time.RFC3339),
	)
	if err != nil {
		writeError(w, "failed to create contact request", http.StatusInternalServerError)
		return
	}

	base := buildBaseURL(r)

	if useTelegram {
		botURL := "https://t.me/" + config.Server.TelegramBotName + "?start=" + verifyToken
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{
			"message":             "Open the Telegram bot to confirm your message.",
			"telegram_verify_url": botURL,
		})
		return
	}

	verifyURL := base + "/contact-requests/verify/" + verifyToken
	emailBody := fmt.Sprintf(
		"Hello,\n\nYou sent a message to %s via the dansal board. Please confirm your message by clicking this link:\n\n%s\n\nThis confirmation link expires in 24 hours.\n",
		posterNick, verifyURL,
	)
	go func() {
		if err := SendEmail(req.Email, "Confirm your message (dansal board)", emailBody); err != nil {
			log.Printf("contact_posts: contact request verify email failed for post %d: %v", postID, err)
		}
	}()

	log.Printf("contact_posts: created contact request for post %d", postID)
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{
		"message": "A confirmation email has been sent.",
	})
}

// GET /api/v1/contact-requests/verify/{token}
// Public. Marks the contact request as verified and forwards the message to the poster.
func verifyContactRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	token := r.PathValue("token")

	var reqID, postID int
	var senderEmail, senderTelegram, message, expiresAt string
	err := db.QueryRow(
		"SELECT id, post_id, sender_email, sender_telegram, message, expires_at FROM contact_requests WHERE verify_token=?",
		token,
	).Scan(&reqID, &postID, &senderEmail, &senderTelegram, &message, &expiresAt)
	if err == sql.ErrNoRows {
		writeError(w, "invalid or already used verification link", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	exp, err := parseTokenExpiration(expiresAt)
	if err != nil || time.Now().After(exp) {
		db.Exec("DELETE FROM contact_requests WHERE id=?", reqID)
		writeError(w, "verification link has expired", http.StatusGone)
		return
	}

	var posterEmail, posterNick, posterChatID string
	db.QueryRow(
		"SELECT email, nickname, COALESCE(poster_telegram_chat_id,'') FROM contact_posts WHERE id=?", postID,
	).Scan(&posterEmail, &posterNick, &posterChatID)

	db.Exec("UPDATE contact_requests SET verify_token=NULL WHERE id=?", reqID)
	log.Printf("contact_requests: verified request %d for post %d", reqID, postID)

	senderContact := senderEmail
	if senderTelegram != "" {
		senderContact = "@" + senderTelegram
	}

	if posterChatID != "" {
		msg := fmt.Sprintf("Someone replied to your board post!\n\nMessage:\n%s\n\nContact them at: %s", message, senderContact)
		go func() {
			if err := sendTelegramMessage(posterChatID, msg); err != nil {
				log.Printf("contact_requests: telegram forward failed for post %d: %v", postID, err)
			}
		}()
	} else if posterEmail != "" {
		body := fmt.Sprintf(
			"Hello %s,\n\nSomeone saw your contact board post on dansal and wants to get in touch:\n\n---\n%s\n---\n\nYou can reach them at: %s\n\nThis message was forwarded by dansal.\n",
			posterNick, message, senderContact,
		)
		go func() {
			if err := SendEmail(posterEmail, "Someone wants to contact you (dansal board)", body); err != nil {
				log.Printf("contact_requests: email forward failed for post %d: %v", postID, err)
			}
		}()
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "verified"})
}
