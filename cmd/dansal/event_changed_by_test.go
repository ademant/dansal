package main

import (
	"database/sql"
	"testing"
)

// TestInsertEventChangedBy covers #1865: a manual insert is attributed to its
// creator (display name + changed_by_id), a feed import to "fetch", and an
// insert with no known creator to "admin".
func TestInsertEventChangedBy(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (7, 'org@example.test', 'Org Person', 'user')")
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (8, 'noname@example.test', '', 'user')")
	db.Exec("INSERT INTO fetch_sources (id, url, type) VALUES (1, 'http://example.com/feed.ics', 'ical')")

	creator, noName := 7, 8
	cases := []struct {
		name   string
		in     EventInput
		wantBy string
		wantID sql.NullInt64
	}{
		{"creator with display name", EventInput{Title: "A", CreatedByID: &creator}, "Org Person", sql.NullInt64{Int64: 7, Valid: true}},
		{"creator without display name", EventInput{Title: "B", CreatedByID: &noName}, "noname", sql.NullInt64{Int64: 8, Valid: true}},
		{"feed import wins over creator", EventInput{Title: "C", FetchSourceID: 1, CreatedByID: &creator}, "fetch", sql.NullInt64{}},
		{"no creator", EventInput{Title: "D"}, "admin", sql.NullInt64{}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := int64(2000000000 + i*86400)
			c.in.StartTime, c.in.EndTime, c.in.IsPublished = start, start+3600, true
			id, _, _, err := insertEvent(db, c.in)
			if err != nil {
				t.Fatalf("insertEvent: %v", err)
			}
			var by string
			var byID sql.NullInt64
			if err := db.QueryRow("SELECT changed_by, changed_by_id FROM events WHERE id = ?", id).Scan(&by, &byID); err != nil {
				t.Fatalf("select: %v", err)
			}
			if by != c.wantBy || byID != c.wantID {
				t.Fatalf("changed_by=%q changed_by_id=%v, want %q %v", by, byID, c.wantBy, c.wantID)
			}
		})
	}
}
