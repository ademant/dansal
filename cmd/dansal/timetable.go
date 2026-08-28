package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"time"
)

type TimetableEntry struct {
	ID             int    `json:"id"`
	EventID        int    `json:"event_id"`
	StartTime      string `json:"start_time"`
	EndTime        string `json:"end_time"`
	Title          string `json:"title"`
	Description    string `json:"description,omitempty"`
	Room           string `json:"room,omitempty"`
	EntryType      string `json:"entry_type,omitempty"`
	EntryDate      string `json:"entry_date,omitempty"`
	LocationID     *int   `json:"location_id,omitempty"`
	LocationName   string `json:"location_name,omitempty"`
	MusicianID     *int   `json:"musician_id,omitempty"`
	MusicianName   string `json:"musician_name,omitempty"`
	InstructorID   *int   `json:"instructor_id,omitempty"`
	InstructorName string `json:"instructor_name,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type TimetableEntryRequest struct {
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Room         string `json:"room"`
	EntryType    string `json:"entry_type"`
	EntryDate    string `json:"entry_date"`
	LocationID   *int   `json:"location_id"`
	MusicianID   *int   `json:"musician_id"`
	InstructorID *int   `json:"instructor_id"`
}

// TimetableHistoryEntry is one journal entry (#1176): a full snapshot of an
// event's timetable as it stood right after one save
// (addTimetableEntries/replaceTimetable/deleteTimetable), append-only — no
// draft state, no diffing at write time. ChangedBy follows the same
// public-timestamp/private-attribution split events.ChangedAt/ChangedBy
// already uses (see event.html's "Last update" sidebar block): the API
// always returns it, dansal_web decides whether to display it based on
// whether the viewer is logged in.
type TimetableHistoryEntry struct {
	ID        int              `json:"id"`
	EventID   int              `json:"event_id"`
	ChangedAt string           `json:"changed_at"`
	ChangedBy string           `json:"changed_by,omitempty"`
	Snapshot  []TimetableEntry `json:"snapshot"`
}

// timetableHistoryLimit caps how many journal rows a single GET returns —
// this is a "recent changes" list, not a full audit export.
const timetableHistoryLimit = 20

// recordTimetableHistory appends one journal row capturing the timetable's
// full state right after a save. Takes a querier so it can participate in
// replaceTimetable's transaction (the snapshot there is the tx's own result,
// not a separate re-read) as well as run standalone after
// addTimetableEntries/deleteTimetable. Best-effort: a journal write failure
// is logged, not surfaced as a failure of the save itself.
func recordTimetableHistory(q querier, eventID, callerID int, snapshot []TimetableEntry) {
	if snapshot == nil {
		snapshot = []TimetableEntry{}
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	if _, err := q.Exec(
		"INSERT INTO timetable_history (event_id, changed_at, changed_by, snapshot) VALUES (?, ?, ?, ?)",
		eventID, time.Now().UTC().Unix(), resolveDisplayName(callerID), string(b),
	); err != nil {
		log.Printf("recordTimetableHistory: event %d: %v", eventID, err)
	}
}

var timeSlotRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func validTimeSlot(s string) bool { return timeSlotRe.MatchString(s) }

func scanTimetableRow(s scanner) (TimetableEntry, error) {
	var e TimetableEntry
	var locID, musID, insID sql.NullInt64
	if err := s.Scan(&e.ID, &e.EventID, &e.StartTime, &e.EndTime, &e.Title, &e.Description, &e.Room, &e.EntryType, &e.EntryDate, &locID, &musID, &insID, &e.CreatedAt); err != nil {
		return TimetableEntry{}, err
	}
	if locID.Valid {
		v := int(locID.Int64)
		e.LocationID = &v
	}
	if musID.Valid {
		v := int(musID.Int64)
		e.MusicianID = &v
	}
	if insID.Valid {
		v := int(insID.Int64)
		e.InstructorID = &v
	}
	return e, nil
}

const timetableReturning = "RETURNING id, event_id, start_time, end_time, title, COALESCE(description,''), COALESCE(room,''), COALESCE(entry_type,'bal'), COALESCE(entry_date,''), location_id, musician_id, instructor_id, created_at"

// fetchTimetable returns all entries for an event ordered by start_time,
// including the location, musician, and instructor names via LEFT JOINs.
func fetchTimetable(eventID int) ([]TimetableEntry, error) {
	rows, err := db.Query(
		`SELECT t.id, t.event_id, t.start_time, t.end_time, t.title, COALESCE(t.description,''),
		        COALESCE(t.room,''), COALESCE(t.entry_type,'bal'), COALESCE(t.entry_date,''), t.location_id,
		        COALESCE(l.location,''), COALESCE(l.short_name,''), l.parent_id, COALESCE(pl.location,''), COALESCE(pl.short_name,''),
		        t.musician_id, COALESCE(m.bandname,''), t.instructor_id, COALESCE(i.name,''), t.created_at
		 FROM timetable_entries t
		 LEFT JOIN locations l ON t.location_id = l.id
		 LEFT JOIN locations pl ON l.parent_id = pl.id
		 LEFT JOIN musicians m ON t.musician_id = m.id
		 LEFT JOIN instructors i ON t.instructor_id = i.id
		 WHERE t.event_id = ? ORDER BY COALESCE(NULLIF(t.entry_date,''), '0000-00-00'), t.start_time, t.id`,
		eventID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []TimetableEntry{}
	for rows.Next() {
		var e TimetableEntry
		var locID, musID, insID, parentID sql.NullInt64
		var locName, locShortName, parentName, parentShortName string
		if err := rows.Scan(&e.ID, &e.EventID, &e.StartTime, &e.EndTime, &e.Title,
			&e.Description, &e.Room, &e.EntryType, &e.EntryDate, &locID, &locName, &locShortName, &parentID, &parentName, &parentShortName,
			&musID, &e.MusicianName, &insID, &e.InstructorName, &e.CreatedAt); err != nil {
			return nil, err
		}
		if locID.Valid {
			v := int(locID.Int64)
			e.LocationID = &v
			e.LocationName = locShortName
			if e.LocationName == "" {
				e.LocationName = locName
			}
			if parentID.Valid {
				bname := parentShortName
				if bname == "" {
					bname = parentName
				}
				if bname != "" {
					e.LocationName += " — " + bname
				}
			}
		}
		if musID.Valid {
			v := int(musID.Int64)
			e.MusicianID = &v
		}
		if insID.Valid {
			v := int(insID.Int64)
			e.InstructorID = &v
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func validateTimetableRequests(reqs []TimetableEntryRequest) error {
	for _, req := range reqs {
		if req.Title == "" {
			return fmt.Errorf("title is required")
		}
		if !validTimeSlot(req.StartTime) || !validTimeSlot(req.EndTime) {
			return fmt.Errorf("invalid time %q–%q; use HH:MM", req.StartTime, req.EndTime)
		}
		if req.EntryDate != "" && !dateRe.MatchString(req.EntryDate) {
			return fmt.Errorf("invalid entry_date %q; use YYYY-MM-DD", req.EntryDate)
		}
	}
	return nil
}

// timetableAuthCheck verifies the event exists and the caller may edit it.
// timetableAuthCheck delegates to the shared requireEventOrg (#1007) — admin
// unrestricted, publisher/user must belong to the event's org. There's no
// target org for a timetable edit, so targetOrgID/requireTarget are the
// "no target to validate" values.
func timetableAuthCheck(w http.ResponseWriter, userRole string, callerID, eventID int) bool {
	var orgID sql.NullInt64
	err := db.QueryRow("SELECT organization_id FROM events WHERE id = ?", eventID).Scan(&orgID)
	if err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return false
	}
	if err != nil {
		writeInternalError(w, err)
		return false
	}
	return requireEventOrg(w, userRole, callerID, orgID, nil, false)
}

func readTimetableBody(w http.ResponseWriter, r *http.Request) (reqs []TimetableEntryRequest, ok bool) {
	body, ok := readBodyOrError(w, r)
	if !ok {
		return nil, false
	}
	if json.Unmarshal(body, &reqs) != nil || len(reqs) == 0 || reqs[0].Title == "" {
		var single TimetableEntryRequest
		if err := json.Unmarshal(body, &single); err != nil {
			writeError(w, "invalid request body", http.StatusBadRequest)
			return nil, false
		}
		reqs = []TimetableEntryRequest{single}
	}
	return reqs, true
}

func insertEntry(q querier, eventID int, req TimetableEntryRequest) (TimetableEntry, error) {
	var locIDArg, musIDArg, insIDArg any
	if req.LocationID != nil {
		locIDArg = *req.LocationID
	}
	if req.MusicianID != nil {
		musIDArg = *req.MusicianID
	}
	if req.InstructorID != nil {
		insIDArg = *req.InstructorID
	}
	// entry_type is free text (#1174: the track palette is per-event, not a
	// fixed vocabulary) — only an empty value falls back to a default.
	entryType := req.EntryType
	if entryType == "" {
		entryType = "bal"
	}
	var entryDateArg any
	if req.EntryDate != "" {
		entryDateArg = req.EntryDate
	}
	return scanTimetableRow(q.QueryRow(
		"INSERT INTO timetable_entries (event_id, start_time, end_time, title, description, room, entry_type, entry_date, location_id, musician_id, instructor_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "+timetableReturning,
		eventID, req.StartTime, req.EndTime, req.Title, req.Description, req.Room, entryType, entryDateArg, locIDArg, musIDArg, insIDArg,
	))
}

// POST /api/v1/events/{id}/timetable — add one or more entries
func addTimetableEntries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "Invalid event ID", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}

	reqs, ok := readTimetableBody(w, r)
	if !ok {
		return
	}
	if err := validateTimetableRequests(reqs); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	entries := make([]TimetableEntry, 0, len(reqs))
	for _, req := range reqs {
		e, err := insertEntry(db, eventID, req)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		entries = append(entries, e)
	}

	// #1176: journal the resulting full timetable, not just the entries this
	// call added — addTimetableEntries appends to whatever already existed.
	if full, err := fetchTimetable(eventID); err == nil {
		recordTimetableHistory(db, eventID, callerID, full)
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(entries)
}

// PUT /api/v1/events/{id}/timetable — replace entire timetable atomically
func replaceTimetable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "Invalid event ID", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}

	body, ok := readBodyOrError(w, r)
	if !ok {
		return
	}
	// PUT always expects an array; send [] to clear the timetable.
	var reqs []TimetableEntryRequest
	if err := json.Unmarshal(body, &reqs); err != nil {
		writeError(w, "Invalid request body: expected JSON array", http.StatusBadRequest)
		return
	}
	if err := validateTimetableRequests(reqs); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM timetable_entries WHERE event_id = ?", eventID); err != nil {
		writeInternalError(w, err)
		return
	}

	entries := make([]TimetableEntry, 0, len(reqs))
	for _, req := range reqs {
		e, err := insertEntry(tx, eventID, req)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		entries = append(entries, e)
	}

	// #1176: journal within the same transaction — entries is already the
	// full resulting timetable, no re-read needed.
	recordTimetableHistory(tx, eventID, callerID, entries)

	if err := tx.Commit(); err != nil {
		writeInternalError(w, err)
		return
	}

	json.NewEncoder(w).Encode(entries)
}

// DELETE /api/v1/events/{id}/timetable — remove all entries for an event
func deleteTimetable(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "Invalid event ID", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	if _, err := db.Exec("DELETE FROM timetable_entries WHERE event_id = ?", eventID); err != nil {
		writeInternalError(w, err)
		return
	}
	// #1176: journal the now-empty timetable.
	recordTimetableHistory(db, eventID, callerID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/events/{id}/timetable/history — recent timetable-journal
// entries (#1176), newest first. Same visibility rule as the timetable
// itself: unauthenticated callers only see history for published,
// email-verified events.
func getTimetableHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	_, userRole := callerFromRequest(r)
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "Invalid event ID", http.StatusBadRequest)
		return
	}

	var isPublished, emailVerified int
	err = db.QueryRow("SELECT is_published, email_verified FROM events WHERE id = ?", eventID).Scan(&isPublished, &emailVerified)
	if err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}
	if userRole == "" && (isPublished == 0 || emailVerified == 0) {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	}

	rows, err := db.Query(
		"SELECT id, event_id, changed_at, changed_by, snapshot FROM timetable_history WHERE event_id = ? ORDER BY changed_at DESC, id DESC LIMIT ?",
		eventID, timetableHistoryLimit,
	)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()

	history := []TimetableHistoryEntry{}
	for rows.Next() {
		var h TimetableHistoryEntry
		var changedAtEpoch int64
		var snapshotJSON string
		if err := rows.Scan(&h.ID, &h.EventID, &changedAtEpoch, &h.ChangedBy, &snapshotJSON); err != nil {
			writeInternalError(w, err)
			return
		}
		h.ChangedAt = epochToLocal(changedAtEpoch)
		json.Unmarshal([]byte(snapshotJSON), &h.Snapshot)
		history = append(history, h)
	}
	json.NewEncoder(w).Encode(history)
}
