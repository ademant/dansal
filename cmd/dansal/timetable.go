package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"

)

type TimetableEntry struct {
	ID           int    `json:"id"`
	EventID      int    `json:"event_id"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	Title        string `json:"title"`
	Description  string `json:"description,omitempty"`
	Room         string `json:"room,omitempty"`
	LocationID   *int   `json:"location_id,omitempty"`
	LocationName string `json:"location_name,omitempty"`
	MusicianID   *int   `json:"musician_id,omitempty"`
	MusicianName string `json:"musician_name,omitempty"`
	CreatedAt    string `json:"created_at"`
}

type TimetableEntryRequest struct {
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Room        string `json:"room"`
	LocationID  *int   `json:"location_id"`
	MusicianID  *int   `json:"musician_id"`
}

var timeSlotRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func validTimeSlot(s string) bool { return timeSlotRe.MatchString(s) }

func scanTimetableRow(s scanner) (TimetableEntry, error) {
	var e TimetableEntry
	var locID, musID sql.NullInt64
	if err := s.Scan(&e.ID, &e.EventID, &e.StartTime, &e.EndTime, &e.Title, &e.Description, &e.Room, &locID, &musID, &e.CreatedAt); err != nil {
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
	return e, nil
}

const timetableReturning = "RETURNING id, event_id, start_time, end_time, title, COALESCE(description,''), COALESCE(room,''), location_id, musician_id, created_at"

// fetchTimetable returns all entries for an event ordered by start_time,
// including the location and musician name via LEFT JOINs.
func fetchTimetable(eventID int) ([]TimetableEntry, error) {
	rows, err := db.Query(
		`SELECT t.id, t.event_id, t.start_time, t.end_time, t.title, COALESCE(t.description,''),
		        COALESCE(t.room,''), t.location_id, COALESCE(l.location,''), t.musician_id, COALESCE(m.name,''), t.created_at
		 FROM timetable_entries t
		 LEFT JOIN locations l ON t.location_id = l.id
		 LEFT JOIN musicians m ON t.musician_id = m.id
		 WHERE t.event_id = ? ORDER BY t.start_time, t.id`,
		eventID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []TimetableEntry{}
	for rows.Next() {
		var e TimetableEntry
		var locID, musID sql.NullInt64
		if err := rows.Scan(&e.ID, &e.EventID, &e.StartTime, &e.EndTime, &e.Title,
			&e.Description, &e.Room, &locID, &e.LocationName, &musID, &e.MusicianName, &e.CreatedAt); err != nil {
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
		writeError(w, err.Error(), http.StatusInternalServerError)
		return false
	}
	if userRole != RoleAdmin && (!orgID.Valid || !isOrgMember(callerID, int(orgID.Int64))) {
		writeError(w, "Forbidden: not authorized to edit this event", http.StatusForbidden)
		return false
	}
	return true
}

func readTimetableBody(r *http.Request) ([]TimetableEntryRequest, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var reqs []TimetableEntryRequest
	if json.Unmarshal(body, &reqs) != nil || len(reqs) == 0 || reqs[0].Title == "" {
		var single TimetableEntryRequest
		if err := json.Unmarshal(body, &single); err != nil {
			return nil, fmt.Errorf("invalid request body")
		}
		reqs = []TimetableEntryRequest{single}
	}
	return reqs, nil
}

func insertEntry(q querier, eventID int, req TimetableEntryRequest) (TimetableEntry, error) {
	var locIDArg, musIDArg any
	if req.LocationID != nil {
		locIDArg = *req.LocationID
	}
	if req.MusicianID != nil {
		musIDArg = *req.MusicianID
	}
	return scanTimetableRow(q.QueryRow(
		"INSERT INTO timetable_entries (event_id, start_time, end_time, title, description, room, location_id, musician_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?) "+timetableReturning,
		eventID, req.StartTime, req.EndTime, req.Title, req.Description, req.Room, locIDArg, musIDArg,
	))
}

// POST /api/v1/events/{id}/timetable — add one or more entries
func addTimetableEntries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	userRole := r.Header.Get("X-User-Role")
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	callerID, _ := strconv.Atoi(r.Header.Get("X-User-ID"))
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "Invalid event ID", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}

	reqs, err := readTimetableBody(r)
	if err != nil {
		status := http.StatusBadRequest
		if errors.As(err, new(*http.MaxBytesError)) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, err.Error(), status)
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
			writeError(w, err.Error(), http.StatusInternalServerError)
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

	userRole := r.Header.Get("X-User-Role")
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	callerID, _ := strconv.Atoi(r.Header.Get("X-User-ID"))
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "Invalid event ID", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		if errors.As(err, new(*http.MaxBytesError)) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, err.Error(), status)
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
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM timetable_entries WHERE event_id = ?", eventID); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	entries := make([]TimetableEntry, 0, len(reqs))
	for _, req := range reqs {
		e, err := insertEntry(tx, eventID, req)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		entries = append(entries, e)
	}

	if err := tx.Commit(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(entries)
}

