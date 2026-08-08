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
	ID               int               `json:"id"`
	EventID          int               `json:"event_id"`
	Type             string            `json:"type"`
	City             string            `json:"city"`
	Persons          int               `json:"persons"`
	Message          string            `json:"message,omitempty"`
	Nickname         string            `json:"nickname"`
	TelegramUsername string            `json:"telegram_username,omitempty"`
	EmailVerified    bool              `json:"email_verified"`
	CreatedAt        string            `json:"created_at"`
	Event            *ContactPostEvent `json:"event,omitempty"`
	ImageURLs        []string          `json:"image_urls,omitempty"`
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

// wipeAndDeleteContactPost clears a post's private contact fields (email,
// telegram_username, poster_telegram_chat_id, manage_token) before deleting
// the row (#1041), rather than relying on the DELETE alone — a backup or
// WAL snapshot taken between the two statements then already sees blanked
// contact data instead of the real values, so nothing sensitive lingers in
// backups/logs beyond the moment of deletion.
func wipeAndDeleteContactPost(id int) error {
	// Delete image files before the DB row (ON DELETE CASCADE handles DB rows).
	// Best-effort — a stale file doesn't block deletion.
	deleteContactPostImageFiles(id)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		"UPDATE contact_posts SET email='', telegram_username=NULL, poster_telegram_chat_id=NULL, manage_token=NULL WHERE id=?",
		id,
	); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM contact_posts WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// isOrgMemberOfEvent returns true when userID is a member of the organisation
// that owns the given event.
func isOrgMemberOfEvent(userID, eventID int) bool {
	var orgID int
	err := db.QueryRow("SELECT COALESCE(organization_id,0) FROM events WHERE id=?", eventID).Scan(&orgID)
	if err != nil || orgID == 0 {
		return false
	}
	return isOrgMember(userID, orgID)
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

// cityRequiredTypes holds the types for which a non-empty city is required.
// ticket_*, lost_item, found_item are scoped to the event itself — no departure/stay city needed.
var cityRequiredTypes = map[string]bool{"ride_offer": true, "ride_request": true, "sleep_offer": true, "sleep_request": true}

// postTypeCategoryGroup maps each post type to its category name for cap enforcement (#1048).
var postTypeCategoryGroup = map[string]string{
	"ride_offer": "ride", "ride_request": "ride",
	"sleep_offer": "sleep", "sleep_request": "sleep",
	"ticket_offer": "ticket", "ticket_request": "ticket",
	"lost_item": "lost_found", "found_item": "lost_found",
}

// postCategoryCap is the maximum number of live posts per identity per event
// for each category. "Live" means expires_at > now, regardless of email_verified.
var postCategoryCap = map[string]int{
	"ride": 1, "sleep": 1, "ticket": 1, "lost_found": 5,
}

// boardPostCapExceeded reports whether the identity (email / callerID / tgUsername)
// already has postCategoryCap or more live posts in the same category as postType
// on eventID. excludeID is the post being replaced by an edit (0 for new posts).
func boardPostCapExceeded(eventID int, postType, email, tgUsername string, callerID, excludeID int) bool {
	category := postTypeCategoryGroup[postType]
	cap, ok := postCategoryCap[category]
	if !ok {
		return false
	}
	// Build IN list: all type strings in the same category.
	var ph []string
	args := []any{eventID, excludeID}
	for t, c := range postTypeCategoryGroup {
		if c == category {
			ph = append(ph, "?")
			args = append(args, t)
		}
	}
	args = append(args,
		time.Now().UTC().Unix(),
		email, email,
		callerID, callerID,
		email, callerID, tgUsername,
	)
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM contact_posts
		WHERE event_id = ?
		  AND id != ?
		  AND type IN (`+strings.Join(ph, ",")+`)
		  AND expires_at > ?
		  AND (
		    (? != '' AND LOWER(email) = LOWER(?))
		    OR (? > 0 AND user_id = ?)
		    OR (? = '' AND ? = 0 AND email = '' AND telegram_username != '' AND telegram_username = ?)
		  )`, args...).Scan(&count)
	return count >= cap
}

// checkBoardPostCap loads a post's event/identity from the DB and calls
// boardPostCapExceeded for newType. Writes a 409 and returns false if the cap
// would be exceeded. Used by PUT and PATCH to re-check on type changes.
func checkBoardPostCap(w http.ResponseWriter, postID int, newType string) bool {
	var eventID int
	var email, tg string
	var uid sql.NullInt64
	if err := db.QueryRow(
		"SELECT event_id, COALESCE(email,''), COALESCE(telegram_username,''), user_id FROM contact_posts WHERE id=?",
		postID,
	).Scan(&eventID, &email, &tg, &uid); err != nil {
		return true // post not found — handled later in the handler
	}
	callerUID := 0
	if uid.Valid {
		callerUID = int(uid.Int64)
	}
	if boardPostCapExceeded(eventID, newType, email, tg, callerUID, postID) {
		msg := capExceededMsg(newType)
		writeError(w, msg, http.StatusConflict)
		return false
	}
	return true
}

// capExceededMsg returns a human-readable message for a 409 cap rejection.
func capExceededMsg(postType string) string {
	if postTypeCategoryGroup[postType] == "lost_found" {
		return "you have reached the maximum number of lost/found posts for this event"
	}
	return "you already have a post in this category — edit or delete it first"
}

// ContactPostCreateRequest is the body accepted by POST /api/v1/events/{id}/contact-posts.
type ContactPostCreateRequest struct {
	Type     string `json:"type" enum:"ride_offer,ride_request,sleep_offer,sleep_request,ticket_offer,ticket_request,lost_item,found_item"`
	City     string `json:"city"`
	OsmID    *int64 `json:"osm_id"`
	Persons  int    `json:"persons"`
	Message  string `json:"message"`
	Nickname string `json:"nickname"`
	Email    string `json:"email"`
	Telegram string `json:"telegram"`
}

// ContactPostWriteRequest is the body accepted by PUT /api/v1/contact-posts/{id}
// (Content-Type: application/json). Full replace of the editable fields
// (type, city, persons, message, nickname) — any field omitted is cleared to
// its zero value, same as PUT semantics elsewhere in the API. Authorization
// is via the ?token= manage_token query parameter, not auth()/Bearer, since
// board posts have no user account backing them (see #726).
type ContactPostWriteRequest struct {
	Type     string `json:"type" enum:"ride_offer,ride_request,sleep_offer,sleep_request,ticket_offer,ticket_request,lost_item,found_item"`
	City     string `json:"city"`
	Persons  int    `json:"persons"`
	Message  string `json:"message"`
	Nickname string `json:"nickname"`
}

// ContactPostMergePatchRequest is the body accepted by PATCH
// /api/v1/contact-posts/{id} (Content-Type: application/merge-patch+json —
// RFC 7396). Every field is a pointer: an omitted key leaves the existing
// value unchanged; a present key sets it (an explicit "" clears a plain text
// field). Authorization is via the ?token= manage_token query parameter, not
// auth()/Bearer (see #726).
type ContactPostMergePatchRequest struct {
	Type     *string `json:"type,omitempty" enum:"ride_offer,ride_request,sleep_offer,sleep_request,ticket_offer,ticket_request,lost_item,found_item"`
	City     *string `json:"city,omitempty"`
	Persons  *int    `json:"persons,omitempty"`
	Message  *string `json:"message,omitempty"`
	Nickname *string `json:"nickname,omitempty"`
}

// GET /api/v1/events/{id}/contact-posts
// Public. Returns only email-verified posts; email field is never returned.
func listContactPosts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	eventID, err := intPathValue(r, "id")
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
		writeInternalError(w, err)
		return
	}
	defer rows.Close()

	posts := []ContactPost{}
	for rows.Next() {
		var p ContactPost
		var ev int
		if err := rows.Scan(&p.ID, &p.EventID, &p.Type, &p.City, &p.Persons, &p.Message, &p.Nickname, &p.TelegramUsername, &ev, &p.CreatedAt); err != nil {
			writeInternalError(w, err)
			return
		}
		p.EmailVerified = ev == 1
		posts = append(posts, p)
	}
	attachContactPostImages(posts)
	json.NewEncoder(w).Encode(posts)
}

// POST /api/v1/events/{id}/contact-posts
// Public. Creates a board post.
// Logged-in users: post is immediately verified, no email sent.
// Anonymous users: post is unverified; a confirmation email with the manage link is sent.
func createContactPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	eventID, err := intPathValue(r, "id")
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

	var req ContactPostCreateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	req.Type = strings.TrimSpace(req.Type)
	req.City = strings.TrimSpace(req.City)
	req.Nickname = strings.TrimSpace(req.Nickname)
	req.Email = strings.TrimSpace(req.Email)
	req.Telegram = strings.TrimPrefix(strings.TrimSpace(req.Telegram), "@")

	if req.Type == "" {
		writeError(w, "type is required", http.StatusBadRequest)
		return
	}
	if !validContactPostTypes[req.Type] {
		writeError(w, "invalid type", http.StatusBadRequest)
		return
	}
	if cityRequiredTypes[req.Type] && req.City == "" {
		writeError(w, "city is required for this post type", http.StatusBadRequest)
		return
	}
	// ticket_*, lost_item, found_item: ignore osm_id and clear city
	if !cityRequiredTypes[req.Type] {
		req.City = ""
		req.OsmID = nil
	}
	if containsLink(req.Message) {
		writeError(w, "message must not contain links", http.StatusBadRequest)
		return
	}
	// lost/found have no meaningful person count; normalize to 1
	if lostFoundTypes[req.Type] {
		req.Persons = 1
	} else if req.Persons < 1 {
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
	if req.Email != "" && looksLikeGmailDotSpam(req.Email) {
		writeError(w, "invalid email address", http.StatusBadRequest)
		return
	}
	if req.Email != "" {
		var open int
		db.QueryRow(
			"SELECT COUNT(*) FROM contact_posts WHERE LOWER(email)=LOWER(?) AND email_verified=0 AND expires_at>strftime('%s','now')",
			req.Email,
		).Scan(&open)
		if open >= config.Server.MaxOpenTokensPerAddress {
			writeError(w, "Too many pending verifications for this address. Please complete or expire existing ones first.", http.StatusTooManyRequests)
			return
		}
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

	// Per-identity per-event board post cap (#1048).
	if boardPostCapExceeded(eventID, req.Type, req.Email, req.Telegram, callerID, 0) {
		writeError(w, capExceededMsg(req.Type), http.StatusConflict)
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

	var osmIDArg any
	if req.OsmID != nil {
		osmIDArg = *req.OsmID
	}

	if callerID > 0 {
		// Logged-in: immediately verified, no email.
		result, err := db.Exec(
			`INSERT INTO contact_posts (event_id, type, city, osm_id, persons, message, nickname, email, telegram_username, manage_token, email_verified, user_id, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			eventID, req.Type, req.City, osmIDArg, req.Persons, req.Message, req.Nickname, req.Email, req.Telegram,
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
		`INSERT INTO contact_posts (event_id, type, city, osm_id, persons, message, nickname, email, telegram_username, manage_token, email_verified, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, req.Type, req.City, osmIDArg, req.Persons, req.Message, req.Nickname, req.Email, req.Telegram,
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
		if _, err := SendEmail(req.Email, emailSubject, emailBody, false); err != nil {
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

	images := contactPostImagesForPost(id)

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
		"images":            images,
	})
}

// checkContactPostManageToken loads a post's manage_token/expires_at and
// verifies the caller-supplied token matches and the post hasn't expired.
// Writes the error response and returns false if access should be denied.
func checkContactPostManageToken(w http.ResponseWriter, postID int, token string) bool {
	if token == "" {
		writeError(w, "token required", http.StatusBadRequest)
		return false
	}
	var storedToken, expiresAtStr string
	err := db.QueryRow("SELECT manage_token, expires_at FROM contact_posts WHERE id=?", postID).
		Scan(&storedToken, &expiresAtStr)
	if err == sql.ErrNoRows {
		writeError(w, "post not found", http.StatusNotFound)
		return false
	}
	if err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return false
	}
	if storedToken != token {
		writeError(w, "invalid token", http.StatusForbidden)
		return false
	}
	exp, err := parseTokenExpiration(expiresAtStr)
	if err != nil || time.Now().After(exp) {
		writeError(w, "post has expired", http.StatusGone)
		return false
	}
	return true
}

// writeContactPostFields runs the UPDATE shared by putContactPost (PUT) and
// updateContactPost (PATCH) — both end up replacing the same five editable
// columns, just via different validation paths (#1012).
func writeContactPostFields(postID int, typ, city, message, nickname string, persons int) error {
	_, err := db.Exec(
		"UPDATE contact_posts SET type=?, city=?, persons=?, message=?, nickname=? WHERE id=?",
		typ, city, persons, message, nickname, postID,
	)
	return err
}

// PUT /api/v1/contact-posts/{id}?token={manage_token}
// Public. Full replace of type, city, persons, message, nickname. Token must
// match and post must not be expired. Authorization is the manage_token, not
// auth()/Bearer — see #726.
func putContactPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	postID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid post id", http.StatusBadRequest)
		return
	}
	if !checkContactPostManageToken(w, postID, r.URL.Query().Get("token")) {
		return
	}

	var req ContactPostWriteRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	req.Type = strings.TrimSpace(req.Type)
	req.City = strings.TrimSpace(req.City)
	req.Nickname = strings.TrimSpace(req.Nickname)
	if req.Type == "" || req.Nickname == "" {
		writeError(w, "type and nickname are required", http.StatusBadRequest)
		return
	}
	if !validContactPostTypes[req.Type] {
		writeError(w, "invalid type", http.StatusBadRequest)
		return
	}
	if cityRequiredTypes[req.Type] && req.City == "" {
		writeError(w, "city is required for this post type", http.StatusBadRequest)
		return
	}
	if !cityRequiredTypes[req.Type] {
		req.City = ""
	}
	if containsLink(req.Message) {
		writeError(w, "message must not contain links", http.StatusBadRequest)
		return
	}
	if lostFoundTypes[req.Type] {
		req.Persons = 1
	} else if req.Persons < 1 {
		req.Persons = 1
	}

	// Re-check cap for the new type (#1048): the edit may move the post to a
	// different or conflicting category.
	if !checkBoardPostCap(w, postID, req.Type) {
		return
	}

	if err := writeContactPostFields(postID, req.Type, req.City, req.Message, req.Nickname, req.Persons); err != nil {
		writeError(w, "failed to update post", http.StatusInternalServerError)
		return
	}
	log.Printf("contact_posts: replaced post %d via manage token", postID)
	w.WriteHeader(http.StatusNoContent)
}

// PATCH /api/v1/contact-posts/{id}?token={manage_token}
// Public. Partial update (RFC 7396 JSON Merge Patch) of type, city, persons,
// message, nickname. Token must match and post must not be expired.
// Authorization is the manage_token, not auth()/Bearer — see #726.
func updateContactPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if ct := r.Header.Get("Content-Type"); ct != "application/merge-patch+json" {
		writeError(w, "PATCH requires Content-Type: application/merge-patch+json", http.StatusUnsupportedMediaType)
		return
	}
	postID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid post id", http.StatusBadRequest)
		return
	}
	if !checkContactPostManageToken(w, postID, r.URL.Query().Get("token")) {
		return
	}

	var type_, city, message, nickname string
	var persons int
	if err := db.QueryRow(
		"SELECT type, city, persons, COALESCE(message,''), nickname FROM contact_posts WHERE id=?", postID,
	).Scan(&type_, &city, &persons, &message, &nickname); err != nil {
		writeError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var req ContactPostMergePatchRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Type != nil {
		t := strings.TrimSpace(*req.Type)
		if !validContactPostTypes[t] {
			writeError(w, "invalid type", http.StatusBadRequest)
			return
		}
		type_ = t
	}
	if req.City != nil {
		city = strings.TrimSpace(*req.City)
	}
	if req.Nickname != nil {
		nickname = strings.TrimSpace(*req.Nickname)
	}
	if req.Message != nil {
		message = *req.Message
	}
	if containsLink(message) {
		writeError(w, "message must not contain links", http.StatusBadRequest)
		return
	}
	// Re-check city requirement after any type or city change
	if cityRequiredTypes[type_] && city == "" {
		writeError(w, "city is required for this post type", http.StatusBadRequest)
		return
	}
	if !cityRequiredTypes[type_] {
		city = ""
	}
	if req.Persons != nil {
		persons = *req.Persons
	}
	if lostFoundTypes[type_] {
		persons = 1
	} else if persons < 1 {
		persons = 1
	}

	// Re-check cap when type changed (#1048).
	if req.Type != nil && !checkBoardPostCap(w, postID, type_) {
		return
	}

	if err := writeContactPostFields(postID, type_, city, message, nickname, persons); err != nil {
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

	if err := wipeAndDeleteContactPost(id); err != nil {
		writeInternalError(w, err)
		return
	}
	log.Printf("contact_posts: self-deleted post %d via manage token", id)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/contact-posts/{id}
// Requires auth. Allowed for: admin role, or org member of the event's organisation.
func deleteContactPost(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	postID, err := intPathValue(r, "id")
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

	if err := wipeAndDeleteContactPost(postID); err != nil {
		writeInternalError(w, err)
		return
	}
	log.Printf("contact_posts: admin/org deleted post %d by user %d", postID, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/contact-posts/{id}/contact
// Public. Contacts the poster.
// Logged-in users: message forwarded immediately, no contact_request row created.
// Anonymous users: creates a pending contact_request and sends a verification email/Telegram link.
func contactPoster(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	postID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid post id", http.StatusBadRequest)
		return
	}

	var req struct {
		Email    string `json:"email"`
		Telegram string `json:"telegram"`
		Message  string `json:"message"`
	}
	if !decodeJSONBody(w, r, &req) {
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
				if _, err := SendEmail(posterEmail, "Someone wants to contact you (dansal board)", body, true); err != nil {
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
	if req.Email != "" && looksLikeGmailDotSpam(req.Email) {
		writeError(w, "invalid email address", http.StatusBadRequest)
		return
	}
	if req.Email != "" {
		var open int
		db.QueryRow(
			"SELECT COUNT(*) FROM contact_requests WHERE LOWER(sender_email)=LOWER(?) AND verify_token IS NOT NULL AND expires_at>strftime('%s','now')",
			req.Email,
		).Scan(&open)
		if open >= config.Server.MaxOpenTokensPerAddress {
			writeError(w, "Too many pending verifications for this address. Please complete or expire existing ones first.", http.StatusTooManyRequests)
			return
		}
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
		if _, err := SendEmail(req.Email, "Confirm your message (dansal board)", emailBody, false); err != nil {
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
			if _, err := SendEmail(posterEmail, "Someone wants to contact you (dansal board)", body, true); err != nil {
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
		query += ` AND (lower(cp.message) LIKE lower(?) ESCAPE '\' OR lower(cp.nickname) LIKE lower(?) ESCAPE '\')`
		like := "%" + escapeLike(search) + "%"
		args = append(args, like, like)
	}

	query += " ORDER BY cp.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		writeInternalError(w, err)
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
			writeInternalError(w, err)
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
	attachContactPostImages(posts)
	json.NewEncoder(w).Encode(posts)
}

// attachContactPostImages populates each post's ImageURLs field from the DB.
// A single batch query avoids N+1 when showing a list. Only lost_item /
// found_item posts ever have images, so for other types the field stays nil.
func attachContactPostImages(posts []ContactPost) {
	if len(posts) == 0 {
		return
	}
	ph := make([]string, len(posts))
	args := make([]any, len(posts))
	for i, p := range posts {
		ph[i] = "?"
		args[i] = p.ID
	}
	rows, err := db.Query(
		"SELECT contact_post_id, id FROM contact_post_images WHERE contact_post_id IN ("+strings.Join(ph, ",")+") ORDER BY id",
		args...,
	)
	if err != nil {
		return
	}
	defer rows.Close()
	idxByID := make(map[int]int, len(posts))
	for i, p := range posts {
		idxByID[p.ID] = i
	}
	for rows.Next() {
		var postID, imgID int
		rows.Scan(&postID, &imgID)
		if idx, ok := idxByID[postID]; ok {
			posts[idx].ImageURLs = append(posts[idx].ImageURLs, contactPostImageURL(imgID))
		}
	}
}

// POST /api/v1/contact-posts/resend-manage
// Public. Given an email address, looks up all live verified contact_posts
// for that email and sends one email listing all their manage URLs. Always
// returns 200 (enumeration resistance — no hint whether posts exist for
// that address).
func resendContactManage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || !isValidEmail(req.Email) {
		w.WriteHeader(http.StatusOK)
		return
	}

	base := buildBaseURL(r)

	type postRow struct {
		postType   string
		nickname   string
		expiresAt  time.Time
		eventTitle string
		manageURL  string
	}

	rows, err := db.Query(
		`SELECT cp.manage_token, cp.type, cp.nickname, cp.expires_at, e.title
		   FROM contact_posts cp
		   JOIN events e ON e.id = cp.event_id
		  WHERE LOWER(cp.email) = LOWER(?)
		    AND cp.email_verified = 1
		    AND cp.expires_at > ?
		  ORDER BY cp.expires_at ASC`,
		req.Email, time.Now().UTC().Unix(),
	)
	if err != nil {
		log.Printf("contact_posts: resend-manage query error email_hash=%s: %v", sha256Hex(req.Email)[:8], err)
		w.WriteHeader(http.StatusOK)
		return
	}
	defer rows.Close()

	var posts []postRow
	for rows.Next() {
		var p postRow
		var token string
		var expiresEpoch int64
		if err := rows.Scan(&token, &p.postType, &p.nickname, &expiresEpoch, &p.eventTitle); err != nil {
			continue
		}
		p.expiresAt = time.Unix(expiresEpoch, 0).UTC()
		p.manageURL = base + "/contact-posts/manage/" + token
		posts = append(posts, p)
	}

	if len(posts) > 0 {
		go func() {
			var sb strings.Builder
			sb.WriteString("Hello,\n\nHere are your manage links for your active board posts:\n\n")
			for _, p := range posts {
				sb.WriteString(fmt.Sprintf(
					"Event: %s\nType:  %s\nLink:  %s\nValid until: %s\n\n",
					p.eventTitle, p.postType, p.manageURL, p.expiresAt.Format("2006-01-02"),
				))
			}
			if _, err := SendEmail(req.Email, "Your board post manage links", sb.String(), false); err != nil {
				log.Printf("contact_posts: resend-manage email failed email_hash=%s: %v", sha256Hex(req.Email)[:8], err)
			}
		}()
	}

	log.Printf("contact_posts: resend-manage email_hash=%s posts=%d", sha256Hex(req.Email)[:8], len(posts))
	w.WriteHeader(http.StatusOK)
}
