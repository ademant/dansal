package main

import (
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestGetEventsMaintenancePerformerAndSourceFilters covers #1584: the
// maintenance view filters musicians and instructors on the event-level lists
// only (event_musician_id / event_instructor_id, any of the given ids), while
// the legacy musician_id keeps matching timetable performers too. has_source
// selects events with any feed source.
func TestGetEventsMaintenancePerformerAndSourceFilters(t *testing.T) {
	setupDedupTestDB(t)

	db.Exec("INSERT INTO musicians (id, bandname) VALUES (1, 'Band 1'), (2, 'Band 2'), (3, 'Band 3')")
	db.Exec("INSERT INTO instructors (id, name) VALUES (1, 'Teacher 1'), (2, 'Teacher 2')")

	// Three events a day apart, so dedup never merges them.
	var ids []int
	for i := 0; i < 3; i++ {
		start := int64(2000000000 + i*86400)
		id, _, _, err := insertEvent(db, EventInput{
			Title: "Event " + strconv.Itoa(i), StartTime: start, EndTime: start + 3600,
			IsPublished: true,
		})
		if err != nil {
			t.Fatalf("insert event %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	// Event 0: musician 1 on the event list. Event 1: musician 2 on the event
	// list and instructor 2. Event 2: musician 3 only in a timetable entry.
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO event_musicians (event_id, musician_id) VALUES (?, 1), (?, 2)", []any{ids[0], ids[1]}},
		{"INSERT INTO event_instructors (event_id, instructor_id) VALUES (?, 2)", []any{ids[1]}},
		{"INSERT INTO timetable_entries (event_id, start_time, end_time, title, musician_id) VALUES (?, '2033-05-18T19:00:00Z', '2033-05-18T20:00:00Z', 'Set', 3)", []any{ids[2]}},
		{"UPDATE events SET source = 'feed' WHERE id = ?", []any{ids[0]}},
	} {
		if _, err := db.Exec(stmt.sql, stmt.args...); err != nil {
			t.Fatalf("setup %q: %v", stmt.sql, err)
		}
	}

	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{"one event-level musician", "?event_musician_id=1", 1},
		{"comma-separated musicians are OR", "?event_musician_id=1,2", 2},
		{"repeated musicians are OR", "?event_musician_id=1&event_musician_id=2", 2},
		{"timetable-only musician does not match", "?event_musician_id=3", 0},
		{"legacy musician_id still matches timetable", "?musician_id=3", 1},
		{"event-level instructor", "?event_instructor_id=2", 1},
		{"has_source picks sourced events", "?has_source=1", 1},
		{"has_source off returns all", "", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/events"+tc.query, nil)
			w := httptest.NewRecorder()
			getEvents(w, req)
			if w.Code != 200 {
				t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
			}
			want := strconv.Itoa(tc.want)
			if got := w.Header().Get("X-Total-Count"); got != want {
				t.Errorf("X-Total-Count = %q, want %q (body=%s)", got, want, w.Body.String())
			}
		})
	}
}
