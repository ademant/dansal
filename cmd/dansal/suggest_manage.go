package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// suggestManageLookup resolves a suggestion_token to its event id, verifying
// the token exists and has not expired (mirrors checkContactPostManageToken).
func suggestManageLookup(token string) (eventID int, err error) {
	var expiresAtStr string
	err = db.QueryRow("SELECT id, COALESCE(suggestion_token_expires_at,0) FROM events WHERE suggestion_token = ?", token).Scan(&eventID, &expiresAtStr)
	if err != nil {
		return 0, err
	}
	if exp, perr := parseTokenExpiration(expiresAtStr); perr == nil && time.Now().UTC().After(exp) {
		return 0, sql.ErrNoRows
	}
	return eventID, nil
}

// GET /api/v1/events/suggest/manage/{token} — public, token-gated. Returns
// the event's current data for the wizard's pre-fill mechanism (#928).
func getSuggestManageEvent(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	eventID, err := suggestManageLookup(token)
	if err == sql.ErrNoRows {
		writeError(w, "token not found or expired", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}
	event, err := fetchEventByID(db, eventID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	event.Musicians, _ = fetchEventMusicians(eventID)
	event.Instructors, _ = fetchEventInstructors(eventID)
	event.Timetable, _ = fetchTimetable(eventID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(event)
}

// pendingEditFields holds the "always needs review" subset of a manage-token
// edit (#928): everything except title/description/start_time/end_time,
// which may be safe to auto-apply. Stored verbatim as events.pending_edit_json.
type pendingEditFields struct {
	URL          string                  `json:"url,omitempty"`
	Location     *EventLocationRequest   `json:"location,omitempty"`
	Tags         []string                `json:"tags,omitempty"`
	DanceIDs     []int                   `json:"dance_ids,omitempty"`
	Pricing      *Pricing                `json:"pricing,omitempty"`
	Food         string                  `json:"food,omitempty"`
	Drink        string                  `json:"drink,omitempty"`
	ContactName  string                  `json:"contact_name,omitempty"`
	ContactEmail string                  `json:"contact_email,omitempty"`
	Musicians    []string                `json:"musicians,omitempty"`
	Instructors  []string                `json:"instructors,omitempty"`
	Timetable    []TimetableEntryRequest `json:"timetable,omitempty"`
	// Title/Description are only populated here when they were rejected from
	// the safe-field auto-apply because the new text contained a URL.
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

func (p pendingEditFields) isEmpty() bool {
	return p.URL == "" && p.Location == nil && len(p.Tags) == 0 && len(p.DanceIDs) == 0 &&
		p.Pricing == nil && p.Food == "" && p.Drink == "" && p.ContactName == "" && p.ContactEmail == "" &&
		len(p.Musicians) == 0 && len(p.Instructors) == 0 && len(p.Timetable) == 0 &&
		p.Title == "" && p.Description == ""
}

// PATCH /api/v1/events/suggest/manage/{token} — public, token-gated (#928).
// Before publish: free direct edit of the still-pending suggestion.
// After publish: only title/description/start_time/end_time may be applied
// directly, and only when the new text contains no URL; everything else is
// written to pending_edit_json for admin/org review.
func patchSuggestManageEvent(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	eventID, err := suggestManageLookup(token)
	if err == sql.ErrNoRows {
		writeError(w, "token not found or expired", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}

	var req SuggestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Title == "" || req.StartTime == "" {
		writeError(w, "title and start_time are required", http.StatusBadRequest)
		return
	}

	var isPublished int
	if err := db.QueryRow("SELECT is_published FROM events WHERE id=?", eventID).Scan(&isPublished); err != nil {
		writeInternalError(w, err)
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

	if isPublished == 0 {
		// Nothing public yet: free direct edit, full admin review still
		// happens at publish time regardless.
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
		var pricingArg any
		if req.Pricing != nil {
			if b, err := json.Marshal(req.Pricing); err == nil {
				pricingArg = string(b)
			}
		}
		if _, err := tx.Exec(
			`UPDATE events SET title=?, description=?, start_time=?, end_time=?, location_id=?,
			 has_ball=?, has_workshop=?, has_festival=?, workshop_difficulty=?, url=?, food=?, drink=?,
			 pricing=?, contact_name=?, contact_email=? WHERE id=?`,
			req.Title, req.Description, startTime, endTime, locID,
			req.HasBall, req.HasWorkshop, req.HasFestival, req.WorkshopDifficulty, urlVal(req.URL), req.Food, req.Drink,
			pricingArg, req.ContactName, req.ContactEmail, eventID,
		); err != nil {
			writeError(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		syncEventTags(tx, eventID, req.Tags)
		tx.Exec("DELETE FROM event_dances WHERE event_id = ?", eventID)
		for _, danceID := range req.DanceIDs {
			tx.Exec("INSERT OR IGNORE INTO event_dances (event_id, dance_id) VALUES (?, ?)", eventID, danceID)
		}
		if err := tx.Commit(); err != nil {
			writeError(w, "db error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"needs_review": false})
		return
	}

	// Published: split into safe-auto-apply vs pending-review.
	// Structured / organiser-controlled fields (food, drink, pricing, location,
	// timetable) are applied directly — the manage token is sent only to the
	// event's own suggester so it is trusted.
	// Free-text or high-risk fields (URL, tags, musicians, contact) still go
	// through pending review, but only when they actually differ from what is
	// already stored — the wizard prefills all fields, so unchanged values must
	// not trigger review.
	pending := pendingEditFields{
		URL: req.URL,
	}
	if tagsChanged(db, eventID, req.Tags) {
		pending.Tags = req.Tags
	}
	if danceIDsChanged(db, eventID, req.DanceIDs) {
		pending.DanceIDs = req.DanceIDs
	}
	if musiciansChanged(db, eventID, req.Musicians) {
		pending.Musicians = req.Musicians
	}
	if instructorsChanged(db, eventID, req.Instructors) {
		pending.Instructors = req.Instructors
	}
	if contactChanged(db, eventID, req.ContactName, req.ContactEmail) {
		pending.ContactName = req.ContactName
		pending.ContactEmail = req.ContactEmail
	}

	var oldStart int64
	var wasCancelled int
	db.QueryRow("SELECT start_time, is_cancelled FROM events WHERE id=?", eventID).Scan(&oldStart, &wasCancelled)

	safeUpdates := []string{}
	safeArgs := []any{}
	if containsLink(req.Title) {
		pending.Title = req.Title
	} else {
		safeUpdates = append(safeUpdates, "title=?")
		safeArgs = append(safeArgs, req.Title)
	}
	if containsLink(req.Description) {
		pending.Description = req.Description
	} else {
		safeUpdates = append(safeUpdates, "description=?")
		safeArgs = append(safeArgs, req.Description)
	}
	safeUpdates = append(safeUpdates, "start_time=?", "end_time=?", "food=?", "drink=?")
	safeArgs = append(safeArgs, startTime, endTime, req.Food, req.Drink)
	if isReschedule(oldStart, startTime, true, wasCancelled == 1) {
		safeUpdates = append(safeUpdates, "previous_start_time=?")
		safeArgs = append(safeArgs, oldStart)
	}
	if req.Pricing != nil {
		if b, err := json.Marshal(req.Pricing); err == nil {
			safeUpdates = append(safeUpdates, "pricing=?")
			safeArgs = append(safeArgs, string(b))
		}
	} else {
		safeUpdates = append(safeUpdates, "pricing=NULL")
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if req.Location.Location != "" && locationChanged(tx, eventID, req.Location) {
		if locID, err := ensureLocation(tx, req.Location); err == nil && locID > 0 {
			safeUpdates = append(safeUpdates, "location_id=?")
			safeArgs = append(safeArgs, locID)
		}
	}

	safeArgs = append(safeArgs, eventID)
	if _, err := tx.Exec("UPDATE events SET "+strings.Join(safeUpdates, ", ")+" WHERE id=?", safeArgs...); err != nil {
		writeError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Apply timetable directly: replace existing entries wholesale.
	if len(req.Timetable) > 0 {
		tx.Exec("DELETE FROM timetable_entries WHERE event_id = ?", eventID)
		for _, ttReq := range req.Timetable {
			insertEntry(tx, eventID, ttReq)
		}
	}

	if !pending.isEmpty() {
		b, _ := json.Marshal(pending)
		if _, err := tx.Exec("UPDATE events SET pending_edit_json=?, pending_edit_submitted_at=? WHERE id=?",
			string(b), time.Now().UTC().Unix(), eventID); err != nil {
			writeError(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	needsReview := !pending.isEmpty()
	if needsReview {
		go notifyReviewersPendingEdit(eventID)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"needs_review": needsReview})
}

// notifyReviewersPendingEdit alerts admins (and, when the event has an
// organization, its members) that a suggester submitted an edit needing
// review — the same reviewer set patchEvent already authorizes (#928).
func notifyReviewersPendingEdit(eventID int) {
	var title string
	var orgID sql.NullInt64
	if err := db.QueryRow("SELECT title, organization_id FROM events WHERE id=?", eventID).Scan(&title, &orgID); err != nil {
		log.Printf("pending-edit: notify: %v", err)
		return
	}
	editPath := fmt.Sprintf("/admin/events/%d/edit", eventID)

	seen := make(map[int]bool)
	notify := func(userID int) {
		if seen[userID] {
			return
		}
		seen[userID] = true
		var email, chatID string
		db.QueryRow("SELECT COALESCE(email,''), COALESCE(telegram_chat_id,'') FROM users WHERE id=?", userID).Scan(&email, &chatID)
		msg := fmt.Sprintf("A suggested edit to %q is waiting for review — %s", title, adminReviewLink(userID, editPath))
		notifyUser(chatID, email, "Event suggestion edit needs review", msg)
	}

	rows, err := db.Query(`SELECT id FROM users WHERE role = 'admin'`)
	if err == nil {
		for rows.Next() {
			var id int
			if rows.Scan(&id) == nil {
				notify(id)
			}
		}
		rows.Close()
	}
	if orgID.Valid {
		rows, err := db.Query(`SELECT user_id FROM organization_members WHERE organization_id = ?`, orgID.Int64)
		if err == nil {
			for rows.Next() {
				var id int
				if rows.Scan(&id) == nil {
					notify(id)
				}
			}
			rows.Close()
		}
	}
}

// POST /api/v1/events/{id}/pending-edit/approve — apply pending_edit_json to
// the live row and clear it. Same authorization as patchEvent (#928).
func approvePendingEdit(w http.ResponseWriter, r *http.Request) {
	handlePendingEdit(w, r, true)
}

// POST /api/v1/events/{id}/pending-edit/reject — discard pending_edit_json.
func rejectPendingEdit(w http.ResponseWriter, r *http.Request) {
	handlePendingEdit(w, r, false)
}

func handlePendingEdit(w http.ResponseWriter, r *http.Request, approve bool) {
	callerID, userRole := callerFromRequest(r)
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var orgID sql.NullInt64
	var pendingJSON sql.NullString
	if err := db.QueryRow("SELECT organization_id, pending_edit_json FROM events WHERE id=?", id).Scan(&orgID, &pendingJSON); err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}
	if userRole != RoleAdmin {
		if !orgID.Valid || !isOrgMember(callerID, int(orgID.Int64)) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
	}
	if !pendingJSON.Valid || pendingJSON.String == "" {
		writeError(w, "no pending edit", http.StatusNotFound)
		return
	}

	if approve {
		var p pendingEditFields
		if err := json.Unmarshal([]byte(pendingJSON.String), &p); err != nil {
			writeError(w, "corrupt pending edit", http.StatusInternalServerError)
			return
		}
		tx, err := db.Begin()
		if err != nil {
			writeError(w, "db error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		updates := []string{}
		args := []any{}
		if p.Title != "" {
			updates = append(updates, "title=?")
			args = append(args, p.Title)
		}
		if p.Description != "" {
			updates = append(updates, "description=?")
			args = append(args, p.Description)
		}
		if p.URL != "" {
			updates = append(updates, "url=?")
			args = append(args, p.URL)
		}
		if p.Food != "" {
			updates = append(updates, "food=?")
			args = append(args, p.Food)
		}
		if p.Drink != "" {
			updates = append(updates, "drink=?")
			args = append(args, p.Drink)
		}
		if p.ContactName != "" {
			updates = append(updates, "contact_name=?")
			args = append(args, p.ContactName)
		}
		if p.ContactEmail != "" {
			updates = append(updates, "contact_email=?")
			args = append(args, p.ContactEmail)
		}
		if p.Pricing != nil {
			if b, err := json.Marshal(p.Pricing); err == nil {
				updates = append(updates, "pricing=?")
				args = append(args, string(b))
			}
		}
		if p.Location != nil {
			locID, err := ensureLocation(tx, *p.Location)
			if err == nil && locID > 0 {
				updates = append(updates, "location_id=?")
				args = append(args, locID)
			}
		}
		updates = append(updates, "pending_edit_json=NULL", "pending_edit_submitted_at=NULL")
		args = append(args, id)
		if _, err := tx.Exec("UPDATE events SET "+strings.Join(updates, ", ")+" WHERE id=?", args...); err != nil {
			writeError(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if len(p.Tags) > 0 {
			syncEventTags(tx, id, p.Tags)
		}
		if len(p.DanceIDs) > 0 {
			tx.Exec("DELETE FROM event_dances WHERE event_id = ?", id)
			for _, danceID := range p.DanceIDs {
				tx.Exec("INSERT OR IGNORE INTO event_dances (event_id, dance_id) VALUES (?, ?)", id, danceID)
			}
		}
		for _, name := range p.Musicians {
			if mid, err := findOrCreateMusicianID(tx, name); err == nil && mid > 0 {
				tx.Exec("INSERT OR IGNORE INTO event_musicians (event_id, musician_id) VALUES (?, ?)", id, mid)
			}
		}
		for _, name := range p.Instructors {
			if iid, err := findOrCreateInstructorID(tx, name); err == nil && iid > 0 {
				tx.Exec("INSERT OR IGNORE INTO event_instructors (event_id, instructor_id) VALUES (?, ?)", id, iid)
			}
		}
		for _, ttReq := range p.Timetable {
			insertEntry(tx, id, ttReq)
		}
		if err := tx.Commit(); err != nil {
			writeError(w, "db error", http.StatusInternalServerError)
			return
		}
	} else {
		db.Exec("UPDATE events SET pending_edit_json=NULL, pending_edit_submitted_at=NULL WHERE id=?", id)
	}

	w.WriteHeader(http.StatusNoContent)
}

// locationChanged returns true when the submitted location fields differ from
// the event's current location, so an unchanged prefilled location is not
// treated as a relocation request.
func locationChanged(q querier, eventID int, submitted EventLocationRequest) bool {
	var curLoc, curAddress, curZipcode, curTown, curCountry string
	q.QueryRow(`SELECT COALESCE(l.location,''), COALESCE(l.address,''), COALESCE(l.zipcode,''),
		COALESCE(l.town,''), COALESCE(l.country,'')
		FROM events e LEFT JOIN locations l ON l.id = e.location_id WHERE e.id=?`, eventID).
		Scan(&curLoc, &curAddress, &curZipcode, &curTown, &curCountry)
	return submitted.Location != curLoc ||
		submitted.Address != curAddress ||
		submitted.Zipcode != curZipcode ||
		submitted.Town != curTown ||
		submitted.Country != curCountry
}

// tagsChanged returns true when the submitted tag set differs from the stored one.
func tagsChanged(q querier, eventID int, submitted []string) bool {
	rows, err := q.Query("SELECT tag FROM event_tags WHERE event_id=? ORDER BY tag", eventID)
	if err != nil {
		return true
	}
	defer rows.Close()
	var current []string
	for rows.Next() {
		var t string
		rows.Scan(&t)
		current = append(current, t)
	}
	cp := append([]string(nil), submitted...)
	sort.Strings(cp)
	if len(cp) != len(current) {
		return true
	}
	for i := range cp {
		if cp[i] != current[i] {
			return true
		}
	}
	return false
}

// danceIDsChanged returns true when the submitted dance IDs differ from the stored ones.
func danceIDsChanged(q querier, eventID int, submitted []int) bool {
	rows, err := q.Query("SELECT dance_id FROM event_dances WHERE event_id=? ORDER BY dance_id", eventID)
	if err != nil {
		return true
	}
	defer rows.Close()
	var current []int
	for rows.Next() {
		var id int
		rows.Scan(&id)
		current = append(current, id)
	}
	cp := append([]int(nil), submitted...)
	sort.Ints(cp)
	if len(cp) != len(current) {
		return true
	}
	for i := range cp {
		if cp[i] != current[i] {
			return true
		}
	}
	return false
}

// musiciansChanged returns true when the submitted musician names differ from stored ones.
func musiciansChanged(q querier, eventID int, submitted []string) bool {
	rows, err := q.Query(`SELECT m.bandname FROM musicians m
		JOIN event_musicians em ON em.musician_id = m.id
		WHERE em.event_id = ? ORDER BY m.bandname`, eventID)
	if err != nil {
		return true
	}
	defer rows.Close()
	var current []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		current = append(current, name)
	}
	cp := append([]string(nil), submitted...)
	sort.Strings(cp)
	sort.Strings(current)
	if len(cp) != len(current) {
		return true
	}
	for i := range cp {
		if cp[i] != current[i] {
			return true
		}
	}
	return false
}

// instructorsChanged returns true when the submitted instructor names differ from stored ones.
func instructorsChanged(q querier, eventID int, submitted []string) bool {
	rows, err := q.Query(`SELECT i.name FROM instructors i
		JOIN event_instructors ei ON ei.instructor_id = i.id
		WHERE ei.event_id = ? ORDER BY i.name`, eventID)
	if err != nil {
		return true
	}
	defer rows.Close()
	var current []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		current = append(current, name)
	}
	cp := append([]string(nil), submitted...)
	sort.Strings(cp)
	sort.Strings(current)
	if len(cp) != len(current) {
		return true
	}
	for i := range cp {
		if cp[i] != current[i] {
			return true
		}
	}
	return false
}

// contactChanged returns true when the submitted contact differs from the stored one.
// Uses the same COALESCE fallback to org contact as the API read path.
func contactChanged(q querier, eventID int, name, email string) bool {
	var curName, curEmail string
	q.QueryRow(`SELECT COALESCE(NULLIF(e.contact_name,''), o.contact_name, ''),
		COALESCE(NULLIF(e.contact_email,''), o.contact_email, '')
		FROM events e LEFT JOIN organizations o ON e.organization_id = o.id
		WHERE e.id=?`, eventID).Scan(&curName, &curEmail)
	return name != curName || email != curEmail
}

// POST /api/v1/events/suggest/manage/{token}/image — public, token-gated.
// Accepts a multipart upload with field name "image" and stores it for the event.
func postSuggestManageImage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	eventID, err := suggestManageLookup(token)
	if err == sql.ErrNoRows {
		writeError(w, "token not found or expired", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}
	if err := r.ParseMultipartForm(config.Server.MaxBodyBytes); err != nil {
		writeError(w, "failed to parse multipart form", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		writeError(w, "missing or unreadable 'image' field", http.StatusBadRequest)
		return
	}
	defer file.Close()
	if err := saveImageFromReader(eventID, file); err != nil {
		if errors.Is(err, errNotImage) {
			writeError(w, "file is not an image", http.StatusUnsupportedMediaType)
		} else {
			writeInternalError(w, err)
		}
		return
	}
	w.WriteHeader(http.StatusCreated)
}
