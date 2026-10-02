package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// #1427: resolving flagged duplicate pairs.

func TestPairCollision(t *testing.T) {
	base := dupEvent{ID: 1, Title: "Bal de Testville", Start: 1_800_000_000, LocationID: 7}
	feed := dupEvent{ID: 1, Title: "Bal de Testville", Start: 1_800_000_000, SourceID: 3}
	cases := []struct {
		name    string
		a, b    dupEvent
		want    bool
		reasons []string
	}{
		{"same venue, 30 min apart", base, dupEvent{Title: "Bal et Atelier", Start: base.Start + 1800, LocationID: 7}, true, []string{"time", "venue"}},
		{"other room", base, dupEvent{Title: "Bal et Atelier", Start: base.Start + 1800, LocationID: 8}, false, []string{"time"}},
		{"3h apart", base, dupEvent{Title: "Bal et Atelier", Start: base.Start + 3*3600, LocationID: 7}, false, nil},
		{"same title, no venue", base, dupEvent{Title: "Bal de Testville", Start: base.Start - 600}, true, []string{"time", "title"}},
		{"same feed, similar title", feed, dupEvent{Title: "ABGESAGT – Bal de Testville", Start: feed.Start, SourceID: 3}, true, []string{"time", "similar_title"}},
		{"other feed, similar title", feed, dupEvent{Title: "ABGESAGT – Bal de Testville", Start: feed.Start, SourceID: 4}, false, []string{"time"}},
	}
	for _, c := range cases {
		got, reasons := pairCollision(c.a, c.b)
		if got != c.want || !slices.Equal(reasons, c.reasons) {
			t.Errorf("%s: pairCollision = %v %v, want %v %v", c.name, got, reasons, c.want, c.reasons)
		}
	}
}

func TestDuplicateResolveFlow(t *testing.T) {
	old := db
	defer func() { db = old }()
	conn, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "calendar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db = conn
	if err := createTables(); err != nil {
		t.Fatal(err)
	}
	migrateDB()
	if instanceTimezone == nil {
		instanceTimezone, _ = time.LoadLocation("Europe/Berlin")
	}
	start := time.Now().Add(72 * time.Hour).Unix()
	db.Exec(`INSERT INTO locations (id, location) VALUES (1, 'Salle Testville'), (2, 'Grande Salle')`)
	db.Exec(`INSERT INTO events (id, title, start_time, end_time, location_id) VALUES (10, 'Bal', ?, ?, 1), (11, 'Bal et Atelier', ?, ?, 1), (12, 'Concert', ?, ?, 1)`,
		start, start+3600, start+1800, start+5400, start+600, start+3000)
	flagDuplicateReview(db, 11, 10, "Bal et Atelier")

	call := func(method, path string, body any, h http.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.SetPathValue("id", "11")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", "1")
		req.Header.Set("X-User-Role", RoleAdmin)
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	flagged := func(id int) bool {
		var n int
		db.QueryRow("SELECT COALESCE(needs_duplicate_review,0) FROM events WHERE id=?", id).Scan(&n)
		return n == 1
	}

	// Check: collides with partner 10 (time + venue), and with 12 as "other".
	var res DuplicateCheck
	rec := call(http.MethodGet, "/api/v1/events/11/duplicate-check", nil, duplicateCheckHandler)
	json.Unmarshal(rec.Body.Bytes(), &res)
	if !res.Flagged || res.PartnerID != 10 || !res.Collides || len(res.Others) != 1 || res.Others[0].ID != 12 {
		t.Fatalf("check = %+v", res)
	}
	// "Cleaned" refused while still colliding.
	if rec := call(http.MethodPost, "/x", map[string]string{"mode": "resolved"}, duplicateResolveHandler); rec.Code != http.StatusConflict {
		t.Fatalf("resolved while colliding: %d", rec.Code)
	}
	// Moving 11 into another room resolves the pair automatically.
	db.Exec("UPDATE events SET location_id = 2 WHERE id = 11")
	recheckDuplicatePair(db, 11)
	if flagged(10) || flagged(11) {
		t.Error("pair still flagged after moving one event to another room")
	}
	// Accept clears a colliding pair unconditionally.
	db.Exec("UPDATE events SET location_id = 1 WHERE id = 11")
	flagDuplicateReview(db, 11, 10, "Bal et Atelier")
	if rec := call(http.MethodPost, "/x", map[string]string{"mode": "accept"}, duplicateResolveHandler); rec.Code != http.StatusNoContent {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	if flagged(10) || flagged(11) {
		t.Error("pair still flagged after accept")
	}
	// Non-admins are refused.
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.SetPathValue("id", "11")
	req.Header.Set("X-User-Role", "publisher")
	rec = httptest.NewRecorder()
	duplicateCheckHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("publisher: %d, want 403", rec.Code)
	}
}
