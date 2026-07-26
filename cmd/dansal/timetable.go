package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
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
		        COALESCE(t.room,''), COALESCE(t.entry_type,'bal'), COALESCE(t.entry_date,''), t.location_id, COALESCE(l.location,''),
		        t.musician_id, COALESCE(m.bandname,''), t.instructor_id, COALESCE(i.name,''), t.created_at
		 FROM timetable_entries t
		 LEFT JOIN locations l ON t.location_id = l.id
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
		var locID, musID, insID sql.NullInt64
		if err := rows.Scan(&e.ID, &e.EventID, &e.StartTime, &e.EndTime, &e.Title,
			&e.Description, &e.Room, &e.EntryType, &e.EntryDate, &locID, &e.LocationName, &musID, &e.MusicianName, &insID, &e.InstructorName, &e.CreatedAt); err != nil {
			return nil, err
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
	if userRole != RoleAdmin && (!orgID.Valid || !isOrgMember(callerID, int(orgID.Int64))) {
		writeError(w, "Forbidden: not authorized to edit this event", http.StatusForbidden)
		return false
	}
	return true
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
	entryType := req.EntryType
	if entryType != "workshop" && entryType != "break" {
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
	eventID, err := strconv.Atoi(r.PathValue("id"))
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
	eventID, err := strconv.Atoi(r.PathValue("id"))
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

	if err := tx.Commit(); err != nil {
		writeInternalError(w, err)
		return
	}

	json.NewEncoder(w).Encode(entries)
}
