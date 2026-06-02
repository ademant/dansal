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
	ID               int                `json:"id"`
	EventID          int                `json:"event_id"`
	Type             string             `json:"type"`
	City             string             `json:"city"`
	Persons          int                `json:"persons"`
	Message          string             `json:"message,omitempty"`
	Nickname         string             `json:"nickname"`
	TelegramUsername string             `json:"telegram_username,omitempty"`
	EmailVerified    bool               `json:"email_verified"`
	CreatedAt        string             `json:"created_at"`
	Event            *ContactPostEvent  `json:"event,omitempty"`
}

// ContactPostEvent holds the event summary included in the global contact-post listing.
type ContactPostEvent struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	StartTime string `json:"start_time"`
	Town      string `json:"town,omitempty"`
	Country   string `json:"country,omitempty"`
}

// containsLink returns true if s contains any URL or mailto: link.
func containsLink(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "http://") || strings.Contains(lower, "https://") || strings.Contains(lower, "mailto:")
}

// computeContactPostExpiry returns the post expiry based on type.
// lost_item / found_item: flat 30 days from now.
// All other types: min(now+30d, event end_time+3d).
func computeContactPostExpiry(eventID int, postType string) time.Time {
	ceiling := time.Now().UTC().Add(30 * 24 * time.Hour)
	if lostFoundTypes[postType] {
		return ceiling
	}
	var endTimeStr string
	if err := db.QueryRow("SELECT end_time FROM events WHERE id=?", eventID).Scan(&endTimeStr); err == nil {
		if ts, err := strconv.ParseInt(strings.TrimSpace(endTimeStr), 10, 64); err == nil {
			candidate := time.Unix(ts, 0).UTC().Add(3 * 24 * time.Hour)
			if candidate.Before(ceiling) {
				return candidate
			}
		}
	}
	return ceiling
}

// isFirstLiveBoardPost returns true when postID is the only live, verified post for eventID.
func isFirstLiveBoardPost(eventID, postID int) bool {
	var count int
	db.QueryRow(
		"SELECT COUNT(*) FROM contact_posts WHERE event_id=? AND email_verified=1 AND expires_at>? AND id!=?",
		eventID, time.Now().UTC().Unix(), postID,
	).Scan(&count)
	return count == 0
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

// validContactPostTypes is the set of allowed contact post type values.
var validContactPostTypes = map[string]bool{
	"ride_offer": true, "ride_request": true,
	"sleep_offer": true, "sleep_request": true,
	"ticket_offer": true, "ticket_request": true,
	"lost_item": true, "found_item": true,
}

// lostFoundTypes holds the types that use a flat 30-day expiry regardless of event end.
var lostFoundTypes = map[string]bool{"lost_item": true, "found_item": true}

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
		eventID, time.Now().UTC().Unix(),
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
// Public. Creates a board post.
// Logged-in users: post is immediately verified, no email sent.
// Anonymous users: post is unverified; a confirmation email with the manage link is sent.
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

	req.Type = strings.TrimSpace(req.Type)
	req.City = strings.TrimSpace(req.City)
	req.Nickname = strings.TrimSpace(req.Nickname)
	req.Email = strings.TrimSpace(req.Email)
	req.Telegram = strings.TrimPrefix(strings.TrimSpace(req.Telegram), "@")

	if req.Type == "" || req.City == "" {
		writeError(w, "type and city are required", http.StatusBadRequest)
		return
	}
	if !validContactPostTypes[req.Type] {
		writeError(w, "invalid type", http.StatusBadRequest)
		return
	}
	if containsLink(req.Message) {
		writeError(w, "message must not contain links", http.StatusBadRequest)
		return
	}
	if req.Persons < 1 {
		req.Persons = 1
	}

	// Check whether the caller is a logged-in user (#385).
	callerID, _ := callerFromRequest(r)
	if callerID > 0 {
		// Fetch verified email and nickname from account.
		var userEmail, userNick string
		db.QueryRow("SELECT COALESCE(email,''), COALESCE(NULLIF(display_name,''), CASE WHEN email IS NOT NULL THEN SUBSTR(email,1,INSTR(email,'@')-1) ELSE '' END) FROM users WHERE id=?", callerID).Scan(&userEmail, &userNick)
		if req.Nickname == "" {
			req.Nickname = userNick
		}
		if req.Email == "" {
			req.Email = userEmail
		}
	}

	if req.Nickname == "" {
		writeError(w, "nickname is required", http.StatusBadRequest)
		return
	}

	// Anonymous path requires contact info.
	if callerID == 0 && req.Email == "" && req.Telegram == "" {
		writeError(w, "email or telegram username is required", http.StatusBadRequest)
		return
	}
	if req.Email != "" && !isValidEmail(req.Email) {
		writeError(w, "invalid email address", http.StatusBadRequest)
		return
	}
	useTelegram := req.Telegram != ""
	if useTelegram && config.Server.TelegramBotName == "" {
		writeError(w, "telegram not configured on this server", http.StatusBadRequest)
		return
	}

	expiresAt := computeContactPostExpiry(eventID, req.Type)
	if !time.Now().Before(expiresAt) {
		writeError(w, "this event has ended — board posts are no longer accepted", http.StatusGone)
		return
	}

	manageToken, err := generateVerificationToken()
	if err != nil {
		writeError(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	var userIDArg any
	if callerID > 0 {
		userIDArg = callerID
	}

	if callerID > 0 {
		// Logged-in: immediately verified, no email.
		result, err := db.Exec(
			`INSERT INTO contact_posts (event_id, type, city, persons, message, nickname, email, telegram_username, manage_token, email_verified, user_id, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			eventID, req.Type, req.City, req.Persons, req.Message, req.Nickname, req.Email, req.Telegram,
			manageToken, userIDArg, expiresAt.Unix(),
		)
		if err != nil {
			writeError(w, "failed to create post", http.StatusInternalServerError)
			return
		}
		id, _ := result.LastInsertId()
		log.Printf("contact_posts: logged-in user %d created verified post %d", callerID, id)
		base := buildBaseURL(r)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"id":         id,
			"message":    "Post created.",
			"manage_url": base + "/contact-posts/manage/" + manageToken,
			"first_post": isFirstLiveBoardPost(eventID, int(id)),
		})
		return
	}

	// In open-posting mode posts are immediately visible; otherwise email/Telegram
	// verification is required before the post appears.
	openPosting := config.Server.BoardOpenPosting
	initialVerified := 0
	if openPosting {
		initialVerified = 1
	}

	result, err := db.Exec(
		`INSERT INTO contact_posts (event_id, type, city, persons, message, nickname, email, telegram_username, manage_token, email_verified, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, req.Type, req.City, req.Persons, req.Message, req.Nickname, req.Email, req.Telegram,
		manageToken, initialVerified, expiresAt.Unix(),
	)
	if err != nil {
		writeError(w, "failed to create post", http.StatusInternalServerError)
		return
	}
	id, _ := result.LastInsertId()

	base := buildBaseURL(r)
	manageURL := base + "/contact-posts/manage/" + manageToken

	if useTelegram {
		if openPosting {
			// Post is already live; send manage link only.
			log.Printf("contact_posts: anonymous open-posting created post %d", id)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"id":         id,
				"message":    "Post created.",
				"manage_url": manageURL,
				"first_post": isFirstLiveBoardPost(eventID, int(id)),
			})
		} else {
			// Telegram bot verifies via /start manage_token.
			botURL := "https://t.me/" + config.Server.TelegramBotName + "?start=" + manageToken
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"id":                  id,
				"message":             "Open the Telegram bot to confirm your post.",
				"telegram_verify_url": botURL,
			})
		}
		return
	}

	var emailSubject, emailBody string
	if openPosting {
		emailSubject = "Your contact board post is live"
		emailBody = fmt.Sprintf(
			"Hello %s,\n\nYour board post is live. Use this link to edit or remove it at any time:\n\n%s\n\nThe link is valid until %s.\n",
			req.Nickname, manageURL, expiresAt.Format("2006-01-02"),
		)
	} else {
		emailSubject = "Your contact board post"
		emailBody = fmt.Sprintf(
			"Hello %s,\n\nYour board post is live once confirmed. Use this link to verify, edit, or remove it at any time:\n\n%s\n\nThe link is valid until %s.\n",
			req.Nickname, manageURL, expiresAt.Format("2006-01-02"),
		)
	}
	go func() {
		if _, err := SendEmail(req.Email, emailSubject, emailBody); err != nil {
			log.Printf("contact_posts: manage email failed for post %d: %v", id, err)
		}
	}()

	w.WriteHeader(http.StatusCreated)
	if openPosting {
		json.NewEncoder(w).Encode(map[string]any{
			"id":      id,
			"message": "Post created.",
		})
	} else {
		json.NewEncoder(w).Encode(map[string]any{
			"id":      id,
			"message": "A confirmation email has been sent. Your post will appear once verified.",
		})
	}
}

// GET /api/v1/contact-posts/manage/{token}
// Public. Looks up a post by manage_token. Auto-verifies if email_verified=0.
// Returns the post fields plus expired bool; used by the manage page.
func getContactPostByToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	token := r.PathValue("token")

	var id, eventID, persons, emailVerified int
	var postType, city, message, nickname, tgUsername, expiresAtStr string
	err := db.QueryRow(
		`SELECT id, event_id, type, city, persons, COALESCE(message,''), nickname,
		        COALESCE(telegram_username,''), email_verified, expires_at
		 FROM contact_posts WHERE manage_token=?`, token,
	).Scan(&id, &eventID, &postType, &city, &persons, &message, &nickname, &tgUsername, &emailVerified, &expiresAtStr)
	if err == sql.ErrNoRows {
		writeError(w, "post not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	exp, err := parseTokenExpiration(expiresAtStr)
	expired := err != nil || time.Now().After(exp)

	justVerified := false
	firstPost := false
	if !expired && emailVerified == 0 {
		db.Exec("UPDATE contact_posts SET email_verified=1 WHERE id=?", id)
		emailVerified = 1
		justVerified = true
		firstPost = isFirstLiveBoardPost(eventID, id)
		log.Printf("contact_posts: manage-page verified post %d (first=%v)", id, firstPost)
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"id":                id,
		"event_id":          eventID,
		"type":              postType,
		"city":              city,
		"persons":           persons,
		"message":           message,
		"nickname":          nickname,
		"telegram_username": tgUsername,
		"email_verified":    emailVerified == 1,
		"expires_at":        expiresAtStr,
		"expired":           expired,
		"just_verified":     justVerified,
		"first_post":        firstPost,
	})
}

// PATCH /api/v1/contact-posts/{id}?token={manage_token}
// Public. Edits type, city, persons, message, nickname. Token must match and post must not be expired.
func updateContactPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	postID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid post id", http.StatusBadRequest)
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, "token required", http.StatusBadRequest)
		return
	}

	var storedToken, expiresAtStr string
	err = db.QueryRow("SELECT manage_token, expires_at FROM contact_posts WHERE id=?", postID).
		Scan(&storedToken, &expiresAtStr)
	if err == sql.ErrNoRows {
		writeError(w, "post not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if storedToken != token {
		writeError(w, "invalid token", http.StatusForbidden)
		return
	}
	exp, err := parseTokenExpiration(expiresAtStr)
	if err != nil || time.Now().After(exp) {
		writeError(w, "post has expired", http.StatusGone)
		return
	}

	var req struct {
		Type     string `json:"type"`
		City     string `json:"city"`
		Persons  int    `json:"persons"`
		Message  string `json:"message"`
		Nickname string `json:"nickname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Type = strings.TrimSpace(req.Type)
	req.City = strings.TrimSpace(req.City)
	req.Nickname = strings.TrimSpace(req.Nickname)
	if req.Type != "" && !validContactPostTypes[req.Type] {
		writeError(w, "invalid type", http.StatusBadRequest)
		return
	}
	if containsLink(req.Message) {
		writeError(w, "message must not contain links", http.StatusBadRequest)
		return
	}
	if req.Persons < 1 {
		req.Persons = 1
	}

	_, err = db.Exec(
		`UPDATE contact_posts SET
		   type    = CASE WHEN ?1 != '' THEN ?1 ELSE type END,
		   city    = CASE WHEN ?2 != '' THEN ?2 ELSE city END,
		   persons = ?3,
		   message = ?4,
		   nickname = CASE WHEN ?5 != '' THEN ?5 ELSE nickname END
		 WHERE id = ?6`,
		req.Type, req.City, req.Persons, req.Message, req.Nickname, postID,
	)
	if err != nil {
		writeError(w, "failed to update post", http.StatusInternalServerError)
		return
	}
	log.Printf("contact_posts: updated post %d via manage token", postID)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/contact-posts/token/{token}
// Public. Deletes a post by manage_token, with expiry check.
func deleteContactPostByManageToken(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	var id int
	var expiresAtStr string
	err := db.QueryRow("SELECT id, expires_at FROM contact_posts WHERE manage_token=?", token).
		Scan(&id, &expiresAtStr)
	if err == sql.ErrNoRows {
		writeError(w, "invalid manage link", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	exp, err := parseTokenExpiration(expiresAtStr)
	if err != nil || time.Now().After(exp) {
		writeError(w, "post has expired", http.StatusGone)
		return
	}

	db.Exec("DELETE FROM contact_posts WHERE id=?", id)
	log.Printf("contact_posts: self-deleted post %d via manage token", id)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/contact-posts/{id}
// Requires auth. Allowed for: admin role, or org member of the event's organisation.
func deleteContactPost(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

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
// Public. Contacts the poster.
// Logged-in users: message forwarded immediately, no contact_request row created.
// Anonymous users: creates a pending contact_request and sends a verification email/Telegram link.
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
	if containsLink(req.Message) {
		writeError(w, "message must not contain links", http.StatusBadRequest)
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

	// Logged-in user: forward immediately without creating a contact_request (#385).
	callerID, _ := callerFromRequest(r)
	if callerID > 0 {
		var senderEmail, senderTelegram string
		db.QueryRow("SELECT COALESCE(email,''), COALESCE(telegram,'') FROM users WHERE id=?", callerID).
			Scan(&senderEmail, &senderTelegram)
		senderContact := senderEmail
		if senderTelegram != "" {
			senderContact = "@" + senderTelegram
		}
		if posterChatID != "" {
			msg := fmt.Sprintf("Someone replied to your board post!\n\nMessage:\n%s\n\nContact them at: %s", req.Message, senderContact)
			go func() {
				if err := sendTelegramMessage(posterChatID, msg); err != nil {
					log.Printf("contact_posts: telegram forward (logged-in) failed for post %d: %v", postID, err)
				}
			}()
		} else if posterEmail != "" {
			body := fmt.Sprintf(
				"Hello %s,\n\nSomeone saw your contact board post on dansal and wants to get in touch:\n\n---\n%s\n---\n\nYou can reach them at: %s\n\nThis message was forwarded by dansal.\n",
				posterNick, req.Message, senderContact,
			)
			go func() {
				if _, err := SendEmail(posterEmail, "Someone wants to contact you (dansal board)", body); err != nil {
					log.Printf("contact_posts: email forward (logged-in) failed for post %d: %v", postID, err)
				}
			}()
		}
		log.Printf("contact_posts: logged-in user %d directly contacted poster of post %d", callerID, postID)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Anonymous path: require contact info and create a pending contact_request.
	if req.Email == "" && req.Telegram == "" {
		writeError(w, "email or telegram is required", http.StatusBadRequest)
		return
	}
	if req.Email != "" && !isValidEmail(req.Email) {
		writeError(w, "invalid email address", http.StatusBadRequest)
		return
	}
	useTelegram := req.Telegram != ""
	if useTelegram && config.Server.TelegramBotName == "" {
		writeError(w, "telegram not configured on this server", http.StatusBadRequest)
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
		postID, req.Email, req.Telegram, req.Message, verifyToken, reqExpiry.Unix(),
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
		if _, err := SendEmail(req.Email, "Confirm your message (dansal board)", emailBody); err != nil {
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
			if _, err := SendEmail(posterEmail, "Someone wants to contact you (dansal board)", body); err != nil {
				log.Printf("contact_requests: email forward failed for post %d: %v", postID, err)
			}
		}()
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "verified"})
}

// GlobalContactPost extends ContactPost with event context for the global board.

// GET /api/v1/contact-posts
// Public. Returns all live verified posts across all published events.
// Query params: type (comma-separated), town, q (free-text), limit, offset.
func listAllContactPosts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()

	limit := 50
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	offset := 0
	if n, err := strconv.Atoi(q.Get("offset")); err == nil && n >= 0 {
		offset = n
	}

	now := time.Now().UTC().Unix()
	query := `SELECT cp.id, cp.event_id, cp.type, cp.city, cp.persons,
	                 COALESCE(cp.message,''), cp.nickname, COALESCE(cp.telegram_username,''),
	                 cp.email_verified, cp.created_at,
	                 e.title, e.start_time, COALESCE(l.town,''), COALESCE(l.country,'')
	          FROM contact_posts cp
	          JOIN events e ON e.id = cp.event_id
	          LEFT JOIN locations l ON l.id = e.location_id
	          WHERE cp.email_verified=1 AND cp.expires_at > ? AND e.is_published=1`
	args := []any{now}

	if types := q.Get("type"); types != "" {
		parts := strings.Split(types, ",")
		placeholders := make([]string, 0, len(parts))
		for _, t := range parts {
			t = strings.TrimSpace(t)
			if validContactPostTypes[t] {
				placeholders = append(placeholders, "?")
				args = append(args, t)
			}
		}
		if len(placeholders) > 0 {
			query += " AND cp.type IN (" + strings.Join(placeholders, ",") + ")"
		}
	}
	if town := strings.TrimSpace(q.Get("town")); town != "" {
		query += " AND lower(l.town)=lower(?)"
		args = append(args, town)
	}
	if search := strings.TrimSpace(q.Get("q")); search != "" {
		query += " AND (lower(cp.message) LIKE lower(?) OR lower(cp.nickname) LIKE lower(?))"
		like := "%" + search + "%"
		args = append(args, like, like)
	}

	query += " ORDER BY cp.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	posts := []ContactPost{}
	for rows.Next() {
		var cp ContactPost
		var ev int
		var startEpoch int64
		var evTitle, evTown, evCountry string
		if err := rows.Scan(
			&cp.ID, &cp.EventID, &cp.Type, &cp.City, &cp.Persons,
			&cp.Message, &cp.Nickname, &cp.TelegramUsername,
			&ev, &cp.CreatedAt,
			&evTitle, &startEpoch, &evTown, &evCountry,
		); err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cp.EmailVerified = ev == 1
		cp.Event = &ContactPostEvent{
			ID:        cp.EventID,
			Title:     evTitle,
			StartTime: time.Unix(startEpoch, 0).UTC().Format(time.RFC3339),
			Town:      evTown,
			Country:   evCountry,
		}
		posts = append(posts, cp)
	}
	json.NewEncoder(w).Encode(posts)
}
