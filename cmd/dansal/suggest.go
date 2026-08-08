package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	suggestPreviewRateLimiter *RateLimiter
	suggestRateLimiter        *RateLimiter
)

func initSuggestRateLimiters() {
	suggestPreviewRateLimiter = NewRateLimiter(5, 10*time.Minute)
	suggestRateLimiter = NewRateLimiter(3, 10*time.Minute)
}

type SuggestRequest struct {
	Title              string                  `json:"title"`
	Description        string                  `json:"description"`
	StartTime          string                  `json:"start_time"`
	EndTime            string                  `json:"end_time"`
	HasBall            bool                    `json:"has_ball"`
	HasWorkshop        bool                    `json:"has_workshop"`
	HasFestival        bool                    `json:"has_festival"`
	WorkshopDifficulty string                  `json:"workshop_difficulty,omitempty"`
	IsCancelled        bool                    `json:"is_cancelled"`
	Tags               []string                `json:"tags"`
	DanceIDs           []int                   `json:"dance_ids,omitempty"`
	URL                string                  `json:"url,omitempty"`
	Food               string                  `json:"food,omitempty"`
	Drink              string                  `json:"drink,omitempty"`
	Location           EventLocationRequest    `json:"location"`
	Email              string                  `json:"email"`
	SuggesterName      string                  `json:"suggester_name,omitempty"`
	Phone2             string                  `json:"phone2"` // honeypot
	Pricing            *Pricing                `json:"pricing,omitempty"`
	ContactName        string                  `json:"contact_name,omitempty"`
	ContactEmail       string                  `json:"contact_email,omitempty"`
	Musicians          []string                `json:"musicians,omitempty"`
	Instructors        []string                `json:"instructors,omitempty"`
	Timetable          []TimetableEntryRequest `json:"timetable,omitempty"`
}

// findOrCreateMusicianID resolves a musician by name (case-insensitive),
// creating a new unreviewed record if no match exists. Used by the anonymous
// suggestion flow, which has no authenticated caller to attribute creation to.
func findOrCreateMusicianID(q querier, name string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, nil
	}
	var id int
	err := q.QueryRow("SELECT id FROM musicians WHERE bandname = ? COLLATE NOCASE", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	err = q.QueryRow("INSERT INTO musicians (bandname) VALUES (?) RETURNING id", name).Scan(&id)
	return id, err
}

// findOrCreateInstructorID mirrors findOrCreateMusicianID for instructors.
func findOrCreateInstructorID(q querier, name string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, nil
	}
	var id int
	err := q.QueryRow("SELECT id FROM instructors WHERE name = ? COLLATE NOCASE", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	err = q.QueryRow("INSERT INTO instructors (name) VALUES (?) RETURNING id", name).Scan(&id)
	return id, err
}

// POST /api/v1/events/suggest-preview — parse iCal or folkdance-JSON without
// auth or org_id requirement. Accepts either an uploaded file or a "url" form
// field to fetch server-side (via safeClient, which blocks private/loopback/
// link-local addresses to prevent SSRF); the rate limiter above bounds abuse
// of the latter.
func suggestPreviewHandler(w http.ResponseWriter, r *http.Request) {
	ip := getClientIP(r)
	if !suggestPreviewRateLimiter.Allow(ip) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeError(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	feedType := r.FormValue("type")
	src := FetchSource{Type: feedType}

	var body []byte
	if file, _, err := r.FormFile("file"); err == nil {
		defer file.Close()
		body, err = io.ReadAll(io.LimitReader(file, 10<<20))
		if err != nil {
			writeError(w, "read failed", http.StatusBadRequest)
			return
		}
	} else if rawURL := strings.TrimSpace(r.FormValue("url")); rawURL != "" {
		u, err := url.Parse(rawURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			writeError(w, "invalid URL", http.StatusBadRequest)
			return
		}
		src.URL = rawURL
		if feedType == "" && strings.Contains(strings.ToLower(rawURL), ".json") {
			feedType = "json"
		}

		resp, err := getWithRetry(r.Context(), safeClient, rawURL)
		if err != nil {
			writeError(w, "fetch failed", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			writeError(w, fmt.Sprintf("remote returned %d", resp.StatusCode), http.StatusBadGateway)
			return
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if err != nil {
			writeError(w, "read failed", http.StatusBadGateway)
			return
		}
	} else {
		writeError(w, "file or url is required", http.StatusBadRequest)
		return
	}

	if feedType == "" {
		feedType = "ical"
	}
	src.Type = feedType

	reqs, err := parseBodyToRequests(body, src)
	if err != nil {
		writeError(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if reqs == nil {
		reqs = []EventCreateRequest{}
	}
	json.NewEncoder(w).Encode(reqs)
}

// POST /api/v1/events/suggest — submit an anonymous event suggestion.
func suggestHandler(w http.ResponseWriter, r *http.Request) {
	ip := getClientIP(r)
	if !suggestRateLimiter.Allow(ip) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	body, ok := readBodyOrError(w, r)
	if !ok {
		return
	}

	var req SuggestRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Honeypot: silently accept without saving.
	if req.Phone2 != "" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if req.Title == "" || req.StartTime == "" {
		writeError(w, "title and start_time are required", http.StatusBadRequest)
		return
	}

	smtpConfigured := smtpEnabled()
	if smtpConfigured && req.Email == "" {
		writeError(w, "email is required", http.StatusBadRequest)
		return
	}
	if smtpConfigured && req.Email != "" {
		if !isValidEmail(req.Email) {
			writeError(w, "invalid email address", http.StatusBadRequest)
			return
		}
		if looksLikeGmailDotSpam(req.Email) {
			writeError(w, "invalid email address", http.StatusUnprocessableEntity)
			return
		}
		var open int
		db.QueryRow(
			"SELECT COUNT(*) FROM events WHERE LOWER(suggester_email)=LOWER(?) AND email_verified = 0",
			req.Email,
		).Scan(&open)
		if open >= config.Server.MaxOpenTokensPerAddress {
			writeError(w, "Too many pending verifications for this address. Please complete or expire existing ones first.", http.StatusTooManyRequests)
			return
		}
	}

	if containsLink(req.Title) || containsLink(req.Description) {
		writeError(w, "links are not allowed in title or description", http.StatusBadRequest)
		return
	}

	startTime, err := parseTimeToUnix(req.StartTime)
	if err != nil {
		writeError(w, "invalid start_time: "+err.Error(), http.StatusBadRequest)
		return
	}
	endTime := startTime + 3600
	if req.EndTime != "" {
		if et, err2 := parseTimeToUnix(req.EndTime); err2 == nil {
			endTime = et
		}
	}

	// Derive has_ball/workshop/festival from tags (and vice versa) so both
	// sources of truth stay in sync before writing to the DB.
	{
		w := EventWriteRequest{
			HasBall: req.HasBall, HasWorkshop: req.HasWorkshop, HasFestival: req.HasFestival,
			Tags: req.Tags,
		}
		syncEventTypeTags(&w)
		req.HasBall, req.HasWorkshop, req.HasFestival, req.Tags = w.HasBall, w.HasWorkshop, w.HasFestival, w.Tags
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	locID, err := ensureLocation(tx, req.Location)
	if err != nil {
		writeError(w, "location: "+err.Error(), http.StatusBadRequest)
		return
	}

	var suggestionToken string
	if smtpConfigured {
		suggestionToken, err = generateToken(32)
		if err != nil {
			writeError(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	var tokenArg any
	var tokenExpiryArg any
	// No SMTP configured: there's no verification step at all, so the
	// suggestion is immediately visible to admins for review, same as
	// before this token became a standing link.
	emailVerified := !smtpConfigured
	// Board session shortcut (#1047): if caller has a valid board session for
	// the same email, skip email verification and notify admins immediately.
	boardSessionVerified := false
	if smtpConfigured && req.Email != "" {
		if _, bsEmail, _, bsOk := lookupBoardSession(r); bsOk && strings.EqualFold(bsEmail, req.Email) {
			emailVerified = true
			boardSessionVerified = true
		}
	}
	if suggestionToken != "" {
		tokenArg = suggestionToken
		// Token is a standing edit link valid until 3 days after the event ends.
		// No 30-day cap: events scheduled far in advance keep a valid manage link
		// throughout their run-up period.
		tokenExpiryArg = time.Unix(endTime, 0).UTC().Add(3 * 24 * time.Hour).Unix()
	}

	var pricingArg any
	if req.Pricing != nil {
		if b, err := json.Marshal(req.Pricing); err == nil {
			pricingArg = string(b)
		}
	}

	if err := validateTimetableRequests(req.Timetable); err != nil {
		writeError(w, "timetable: "+err.Error(), http.StatusBadRequest)
		return
	}

	var eventID int64
	var shortCode string
	var insertErr error
	for range 5 {
		shortCode, insertErr = generateShortCode()
		if insertErr != nil {
			break
		}
		var res sql.Result
		res, insertErr = tx.Exec(
			`INSERT INTO events
			 (title, description, start_time, end_time, location_id,
			  has_ball, has_workshop, has_festival, is_cancelled, workshop_difficulty,
			  is_published, url, food, drink, pricing, contact_name, contact_email,
			  suggester_email, suggester_name, suggestion_token, suggestion_token_expires_at, email_verified, short_code)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, jsonb(?), ?, ?, ?, ?, ?, ?, ?, ?)`,
			req.Title, req.Description, startTime, endTime, locID,
			req.HasBall, req.HasWorkshop, req.HasFestival, req.IsCancelled, req.WorkshopDifficulty,
			urlVal(req.URL), req.Food, req.Drink, pricingArg, req.ContactName, req.ContactEmail,
			req.Email, req.SuggesterName, tokenArg, tokenExpiryArg, emailVerified, shortCode,
		)
		if insertErr == nil {
			eventID, _ = res.LastInsertId()
			break
		}
		if !strings.Contains(insertErr.Error(), "short_code") {
			break
		}
	}
	if insertErr != nil {
		writeError(w, "db error: "+insertErr.Error(), http.StatusInternalServerError)
		return
	}
	syncEventTags(tx, int(eventID), req.Tags)
	for _, danceID := range req.DanceIDs {
		tx.Exec("INSERT OR IGNORE INTO event_dances (event_id, dance_id) VALUES (?, ?)", eventID, danceID)
	}

	musicianIDs := make([]int, 0, len(req.Musicians))
	for _, name := range req.Musicians {
		if id, err := findOrCreateMusicianID(tx, name); err == nil && id > 0 {
			musicianIDs = append(musicianIDs, id)
		}
	}
	if err := batchInsertPairs(tx, "event_musicians", "event_id", "musician_id", int(eventID), musicianIDs); err != nil {
		writeError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	instructorIDs := make([]int, 0, len(req.Instructors))
	for _, name := range req.Instructors {
		if id, err := findOrCreateInstructorID(tx, name); err == nil && id > 0 {
			instructorIDs = append(instructorIDs, id)
		}
	}
	if err := batchInsertPairs(tx, "event_instructors", "event_id", "instructor_id", int(eventID), instructorIDs); err != nil {
		writeError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for _, ttReq := range req.Timetable {
		if _, err := insertEntry(tx, int(eventID), ttReq); err != nil {
			writeError(w, "timetable: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	if smtpConfigured && boardSessionVerified {
		// Board session verified the email — send manage link only; no verify step.
		base := buildBaseURL(r)
		manageURL := base + "/events/suggest/manage/" + suggestionToken
		subject := "Your event suggestion"
		if req.Title != "" {
			subject = fmt.Sprintf("Your event suggestion: %s", req.Title)
		}
		go func() {
			msg := fmt.Sprintf(
				"Thank you for suggesting an event! Your submission has been received.\n\n"+
					"Use this link to review or edit it at any time:\n\n%s\n",
				manageURL,
			)
			if _, err := SendEmail(req.Email, subject, msg, false); err != nil {
				log.Printf("suggest: send manage email (board-session path): %v", err)
			}
		}()
		go notifyAdminsSuggestion(req.Title, req.StartTime)
	} else if smtpConfigured {
		base := buildBaseURL(r)
		verifyURL := base + "/events/suggest/verify/" + suggestionToken
		manageURL := base + "/events/suggest/manage/" + suggestionToken
		subject := "Confirm your event suggestion"
		if req.Title != "" {
			subject = fmt.Sprintf("Confirm your event suggestion: %s, %s", req.Title, time.Unix(startTime, 0).UTC().Format("2 Jan 2006"))
		}
		go func() {
			msg := fmt.Sprintf(
				"Thank you for suggesting an event!\n\nPlease confirm your submission:\n\n%s\n\n"+
					"After confirming, you can use this same link at any time to review or edit your suggestion:\n\n%s\n\n"+
					"If you did not submit this suggestion, you can ignore this email.",
				verifyURL, manageURL,
			)
			if _, err := SendEmail(req.Email, subject, msg, false); err != nil {
				log.Printf("suggest: send verify email: %v", err)
			}
		}()
	} else {
		go notifyAdminsSuggestion(req.Title, req.StartTime)
	}

	w.WriteHeader(http.StatusAccepted)
}

// GET /api/v1/events/suggest/verify/{token} — confirm an email-verified suggestion.
// The token itself is no longer destroyed (#928): it becomes a standing
// edit-access link, mirroring contact_posts.manage_token. First visit just
// flips email_verified, same as getContactPostByToken does for the board.
func suggestVerifyHandler(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeError(w, "token required", http.StatusBadRequest)
		return
	}

	var eventID int
	var expiresAtStr string
	err := db.QueryRow("SELECT id, COALESCE(suggestion_token_expires_at,0) FROM events WHERE suggestion_token = ?", token).Scan(&eventID, &expiresAtStr)
	if err == sql.ErrNoRows {
		writeError(w, "token not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	if exp, err := parseTokenExpiration(expiresAtStr); err == nil && time.Now().UTC().After(exp) {
		writeError(w, "token expired", http.StatusGone)
		return
	}

	if _, err := db.Exec(`UPDATE events SET email_verified = 1 WHERE id = ?`, eventID); err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	go notifyAdminsSuggestion("", "")
	w.WriteHeader(http.StatusOK)
}

// notifyAdminsDuplicateReview notifies admins that two events were flagged as
// a possible duplicate pair needing manual merge review.
func notifyAdminsDuplicateReview(title string) {
	msg := fmt.Sprintf("Possible duplicate event detected: %q — review and merge if needed in the admin panel (Events, \"flagged\" filter).", title)

	rows, err := db.Query(`SELECT COALESCE(email,''), COALESCE(telegram_chat_id,'') FROM users WHERE role = 'admin'`)
	if err != nil {
		log.Printf("duplicate review: notify admins: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var email, chatID string
		if err := rows.Scan(&email, &chatID); err != nil {
			continue
		}
		notifyUser(chatID, email, "Possible duplicate event", msg)
	}
}

// notifyAdminsSuggestion sends a notification to admin users via Telegram and/or email.
func notifyAdminsSuggestion(title, startTime string) {
	msg := "A new event suggestion is waiting for review."
	if title != "" {
		msg = fmt.Sprintf("New event suggestion: %q (%s) — review it.", title, startTime)
	}

	rows, err := db.Query(`SELECT id, COALESCE(email,''), COALESCE(telegram_chat_id,'') FROM users WHERE role = 'admin'`)
	if err != nil {
		log.Printf("suggest: notify admins: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var adminID int
		var email, chatID string
		if err := rows.Scan(&adminID, &email, &chatID); err != nil {
			continue
		}
		notifyUser(chatID, email, "New event suggestion", msg+" — "+adminReviewLink(adminID, "/admin/events?unpublished=1&include_past=1"))
	}
}
