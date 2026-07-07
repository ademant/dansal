package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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

// POST /api/v1/events/suggest-preview — parse iCal or folkdance-JSON without auth or org_id requirement.
func suggestPreviewHandler(w http.ResponseWriter, r *http.Request) {
	ip := getIP(r)
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
	if feedType == "" {
		feedType = "ical"
	}

	src := FetchSource{Type: feedType}

	var body []byte
	if file, _, err := r.FormFile("file"); err == nil {
		defer file.Close()
		body, err = io.ReadAll(io.LimitReader(file, 10<<20))
		if err != nil {
			writeError(w, "read failed", http.StatusBadRequest)
			return
		}
	} else {
		writeError(w, "file is required", http.StatusBadRequest)
		return
	}

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
	ip := getIP(r)
	if !suggestRateLimiter.Allow(ip) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, config.Server.MaxBodyBytes))
	if err != nil {
		writeError(w, "read failed", http.StatusBadRequest)
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
		suggestionToken, err = randomHex32()
		if err != nil {
			writeError(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	var tokenArg any
	if suggestionToken != "" {
		tokenArg = suggestionToken
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
			  suggester_email, suggestion_token, short_code)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			req.Title, req.Description, startTime, endTime, locID,
			req.HasBall, req.HasWorkshop, req.HasFestival, req.IsCancelled, req.WorkshopDifficulty,
			urlVal(req.URL), req.Food, req.Drink, pricingArg, req.ContactName, req.ContactEmail,
			req.Email, tokenArg, shortCode,
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
	syncEventTags(tx, int(eventID), filterKnownTags(req.Tags))
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

	if smtpConfigured {
		base := buildBaseURL(r)
		verifyURL := base + "/events/suggest/verify/" + suggestionToken
		go func() {
			msg := fmt.Sprintf(
				"Thank you for suggesting an event!\n\nPlease confirm your submission:\n\n%s\n\nIf you did not submit this suggestion, you can ignore this email.",
				verifyURL,
			)
			if _, err := SendEmail(req.Email, "Confirm your event suggestion", msg, false); err != nil {
				log.Printf("suggest: send verify email: %v", err)
			}
		}()
	} else {
		go notifyAdminsSuggestion(req.Title, req.StartTime)
	}

	w.WriteHeader(http.StatusAccepted)
}

// GET /api/v1/events/suggest/verify/{token} — confirm an email-verified suggestion.
func suggestVerifyHandler(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		writeError(w, "token required", http.StatusBadRequest)
		return
	}

	result, err := db.Exec(`UPDATE events SET suggestion_token = NULL WHERE suggestion_token = ?`, token)
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		writeError(w, "token not found", http.StatusNotFound)
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
