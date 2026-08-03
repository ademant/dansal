package main

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestIsReschedule(t *testing.T) {
	const day = int64(24 * 60 * 60)
	cases := []struct {
		name                       string
		oldStart, newStart         int64
		wasPublished, wasCancelled bool
		want                       bool
	}{
		{"unpublished draft edited", 1000, 1000 + 4*3600, false, false, false},
		{"cancelled event edited", 1000, 1000 + 4*3600, true, true, false},
		{"no prior start recorded", 0, 4 * 3600, true, false, false},
		{"unchanged start", 1000, 1000, true, false, false},
		{"minor 1h correction", 1000, 1000 + 3600, true, false, false},
		{"genuine 4h reschedule", 1000, 1000 + 4*3600, true, false, true},
		{"genuine reschedule to earlier date", 10 * day, 10*day - 2*day, true, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isReschedule(c.oldStart, c.newStart, c.wasPublished, c.wasCancelled)
			if got != c.want {
				t.Errorf("isReschedule(%d, %d, %v, %v) = %v, want %v", c.oldStart, c.newStart, c.wasPublished, c.wasCancelled, got, c.want)
			}
		})
	}
}

// TestInsertEventRecordsReschedule verifies the #927 previous_start_time
// column is set when a re-imported (same uid) published, non-cancelled event
// shifts start_time by >= the reschedule threshold, and left untouched by an
// ordinary re-import that only nudges the time slightly.
func TestInsertEventRecordsReschedule(t *testing.T) {
	old := db
	defer func() { db = old }()

	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db = conn

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()
	if berlinLoc == nil {
		berlinLoc, _ = time.LoadLocation("Europe/Berlin")
	}

	const origStart = int64(1_800_000_000)
	id, _, outcome, err := insertEvent(db, "Fest Noz", "desc", origStart, origStart+3600, 0,
		false, false, false, false, "", "", true, nil,
		"reschedule-test-uid", "", "", 0, nil, 0, "", "", "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if outcome != outcomeNew {
		t.Fatalf("outcome = %s, want new", outcome)
	}

	var prev sql.NullInt64
	db.QueryRow("SELECT previous_start_time FROM events WHERE id=?", id).Scan(&prev)
	if prev.Valid {
		t.Fatalf("previous_start_time should be unset after initial insert, got %v", prev.Int64)
	}

	// Minor 1h correction: should NOT set previous_start_time.
	minorShift := origStart + 3600
	_, _, outcome, err = insertEvent(db, "Fest Noz", "desc", minorShift, minorShift+3600, 0,
		false, false, false, false, "", "", true, nil,
		"reschedule-test-uid", "", "", 0, nil, 0, "", "", "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("minor update: %v", err)
	}
	if outcome != outcomeUpdated {
		t.Fatalf("outcome = %s, want updated", outcome)
	}
	db.QueryRow("SELECT previous_start_time FROM events WHERE id=?", id).Scan(&prev)
	if prev.Valid {
		t.Fatalf("previous_start_time should stay unset after a minor time correction, got %v", prev.Int64)
	}

	// Genuine reschedule: shift by 5h from the (already updated) minorShift time.
	newStart := minorShift + 5*3600
	_, _, outcome, err = insertEvent(db, "Fest Noz", "desc", newStart, newStart+3600, 0,
		false, false, false, false, "", "", true, nil,
		"reschedule-test-uid", "", "", 0, nil, 0, "", "", "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("reschedule update: %v", err)
	}
	if outcome != outcomeUpdated {
		t.Fatalf("outcome = %s, want updated", outcome)
	}
	db.QueryRow("SELECT previous_start_time FROM events WHERE id=?", id).Scan(&prev)
	if !prev.Valid || prev.Int64 != minorShift {
		t.Fatalf("previous_start_time = %v, want %d", prev, minorShift)
	}

	event, err := fetchEventByID(db, id)
	if err != nil {
		t.Fatalf("fetchEventByID: %v", err)
	}
	if event.PreviousStartTime == "" {
		t.Fatalf("Event.PreviousStartTime not populated: %+v", event)
	}
}
