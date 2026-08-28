package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// newTimetableRequest builds a request carrying the same X-User-ID/X-User-Role
// headers the auth() middleware would set after validating a bearer token —
// callerFromRequest reads these directly (see helpers.go), so handler-level
// tests can set them without a real token.
func newTimetableRequest(method, path string, body []byte, eventID int, callerID int, role string) *httptest.ResponseRecorder {
	var r *httptest.ResponseRecorder
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("X-User-ID", strconv.Itoa(callerID))
	req.Header.Set("X-User-Role", role)
	req.SetPathValue("id", strconv.Itoa(eventID))
	r = httptest.NewRecorder()

	switch method + " " + trimTimetableSuffix(path) {
	case "POST /timetable":
		addTimetableEntries(r, req)
	case "PUT /timetable":
		replaceTimetable(r, req)
	case "DELETE /timetable":
		deleteTimetable(r, req)
	case "GET /timetable/history":
		getTimetableHistory(r, req)
	}
	return r
}

func trimTimetableSuffix(path string) string {
	if len(path) >= len("/timetable/history") && path[len(path)-len("/timetable/history"):] == "/timetable/history" {
		return "/timetable/history"
	}
	return "/timetable"
}

// TestTimetableHistoryJournal verifies #1176's core contract: every
// addTimetableEntries/replaceTimetable/deleteTimetable call appends one
// journal row holding the resulting full timetable snapshot, newest first,
// and that a custom (non-default-palette) entry_type round-trips instead of
// silently being coerced to 'bal' by a stale CHECK constraint (#1174 fix).
func TestTimetableHistoryJournal(t *testing.T) {
	setupDedupTestDB(t)

	id, _, _, err := insertEvent(db, EventInput{
		Title: "Festival", StartTime: 2000000000, EndTime: 2000100000, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insertEvent: %v", err)
	}

	// PUT replaces the whole timetable — including a custom entry_type not
	// in the old hardcoded 8-slug list.
	putBody, _ := json.Marshal([]TimetableEntryRequest{
		{StartTime: "10:00", EndTime: "11:00", Title: "Keynote", EntryType: "keynote"},
	})
	rec := newTimetableRequest("PUT", "/api/v1/events/1/timetable", putBody, id, 1, RoleAdmin)
	if rec.Code != 200 {
		t.Fatalf("PUT timetable: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var putEntries []TimetableEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &putEntries); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if len(putEntries) != 1 || putEntries[0].EntryType != "keynote" {
		t.Fatalf("expected custom entry_type 'keynote' to round-trip, got %+v", putEntries)
	}

	// POST appends one more entry.
	postBody, _ := json.Marshal(TimetableEntryRequest{StartTime: "12:00", EndTime: "13:00", Title: "Lunch", EntryType: "break"})
	rec = newTimetableRequest("POST", "/api/v1/events/1/timetable", postBody, id, 1, RoleAdmin)
	if rec.Code != 201 {
		t.Fatalf("POST timetable: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// DELETE clears it.
	rec = newTimetableRequest("DELETE", "/api/v1/events/1/timetable", nil, id, 1, RoleAdmin)
	if rec.Code != 204 {
		t.Fatalf("DELETE timetable: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// History should now have 3 rows, newest (the empty delete) first.
	rec = newTimetableRequest("GET", "/api/v1/events/1/timetable/history", nil, id, 1, RoleAdmin)
	if rec.Code != 200 {
		t.Fatalf("GET history: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var history []TimetableHistoryEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 journal rows, got %d: %+v", len(history), history)
	}
	if len(history[0].Snapshot) != 0 {
		t.Fatalf("expected newest entry (after DELETE) to have an empty snapshot, got %+v", history[0].Snapshot)
	}
	if len(history[1].Snapshot) != 2 {
		t.Fatalf("expected second-newest entry (after POST) to have 2 entries, got %+v", history[1].Snapshot)
	}
	if len(history[2].Snapshot) != 1 || history[2].Snapshot[0].EntryType != "keynote" {
		t.Fatalf("expected oldest entry (after PUT) to have 1 custom-type entry, got %+v", history[2].Snapshot)
	}
	if history[0].ChangedBy == "" {
		t.Fatal("expected changed_by to be set")
	}

	// Anonymous callers may read history for a published event...
	rec = newTimetableRequest("GET", "/api/v1/events/1/timetable/history", nil, id, 0, "")
	if rec.Code != 200 {
		t.Fatalf("anonymous GET history on published event: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// ...but not for an unpublished one.
	unpubID, _, _, err := insertEvent(db, EventInput{
		Title: "Draft event", StartTime: 2000000000, EndTime: 2000100000, IsPublished: false,
	})
	if err != nil {
		t.Fatalf("insertEvent (unpublished): %v", err)
	}
	rec = newTimetableRequest("GET", "/api/v1/events/1/timetable/history", nil, unpubID, 0, "")
	if rec.Code != 404 {
		t.Fatalf("anonymous GET history on unpublished event: expected 404, got status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestMigrateDropsTimetableEntryTypeCheck simulates an existing DB still on
// the legacy timetable_entries.entry_type CHECK constraint (the exact state
// #1174 shipped into without noticing the constraint would silently coerce
// any custom track slug back to 'bal') and verifies migrateDB() rebuilds the
// table without the constraint, preserving existing rows.
func TestMigrateDropsTimetableEntryTypeCheck(t *testing.T) {
	old := db
	t.Cleanup(func() { db = old })

	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	db = conn

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	// Roll the fresh table back to the legacy constrained shape (as if this
	// DB predates #1174) and seed one pre-existing row.
	if _, err := db.Exec(`DROP TABLE timetable_entries`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE timetable_entries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id INTEGER NOT NULL,
		start_time TEXT NOT NULL,
		end_time TEXT NOT NULL,
		title TEXT NOT NULL,
		description TEXT,
		room TEXT,
		location_id INTEGER,
		musician_id INTEGER,
		instructor_id INTEGER,
		entry_type TEXT NOT NULL DEFAULT 'bal' CHECK(entry_type IN ('bal', 'workshop', 'break', 'session', 'dance-workshop', 'musician-workshop')),
		entry_date TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events (id, title, start_time, end_time) VALUES (1, 'Legacy event', 1000, 2000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO timetable_entries (event_id, start_time, end_time, title, entry_type) VALUES (1, '10:00', '11:00', 'Old entry', 'workshop')`); err != nil {
		t.Fatal(err)
	}

	migrateDB()

	var schema string
	db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='timetable_entries'").Scan(&schema)
	if schema == "" {
		t.Fatal("timetable_entries table missing after migrateDB")
	}
	if strings.Contains(schema, "CHECK(entry_type IN") {
		t.Fatalf("expected entry_type CHECK constraint to be dropped, schema: %s", schema)
	}

	var title, entryType string
	if err := db.QueryRow("SELECT title, entry_type FROM timetable_entries WHERE event_id=1").Scan(&title, &entryType); err != nil {
		t.Fatalf("pre-existing row lost after migration: %v", err)
	}
	if title != "Old entry" || entryType != "workshop" {
		t.Fatalf("pre-existing row corrupted: title=%q entry_type=%q", title, entryType)
	}

	// A custom (non-legacy-list) entry_type must now be insertable directly.
	if _, err := db.Exec(`INSERT INTO timetable_entries (event_id, start_time, end_time, title, entry_type) VALUES (1, '12:00', '13:00', 'Keynote', 'keynote')`); err != nil {
		t.Fatalf("custom entry_type still rejected after migration: %v", err)
	}
}
