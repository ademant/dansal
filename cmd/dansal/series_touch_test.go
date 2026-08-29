package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestUpdateSeriesDescriptionsTouchesChangedAt covers #1191: bumping an
// event's description through the series bulk-descriptions endpoint must
// also stamp changed_at/changed_by, same as every other event-mutating
// path, so ETag/Atom-feed/pull-sync consumers see the update.
func TestUpdateSeriesDescriptionsTouchesChangedAt(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")

	res, err := db.Exec(`INSERT INTO event_series (slug, title) VALUES ('elisenbrunnen', 'Balfolk am Elisenbrunnen')`)
	if err != nil {
		t.Fatalf("insert series: %v", err)
	}
	seriesID64, _ := res.LastInsertId()
	seriesID := int(seriesID64)

	eventID, _, _, err := insertEvent(db, EventInput{
		Title: "Balfolk am Elisenbrunnen", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := db.Exec("UPDATE events SET series_id=?, changed_at=0 WHERE id=?", seriesID, eventID); err != nil {
		t.Fatalf("attach event to series: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"updates": []map[string]any{{"event_id": eventID, "description": "Updated description"}},
	})
	req := adminReq("POST", "/api/v1/series/"+strconv.Itoa(seriesID)+"/descriptions", body)
	req.SetPathValue("id", strconv.Itoa(seriesID))
	w := httptest.NewRecorder()
	updateSeriesDescriptions(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}

	var description string
	var changedAt int64
	var changedBy string
	db.QueryRow("SELECT description, changed_at, changed_by FROM events WHERE id=?", eventID).Scan(&description, &changedAt, &changedBy)
	if description != "Updated description" {
		t.Errorf("description = %q, want %q", description, "Updated description")
	}
	if changedAt == 0 {
		t.Error("changed_at was not bumped")
	}
	if changedBy == "" {
		t.Error("changed_by was not set")
	}
}

// TestAssignSeriesEventsTouchesChangedAt covers #1191: attaching an event to
// a series must also stamp changed_at/changed_by.
func TestAssignSeriesEventsTouchesChangedAt(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")

	res, err := db.Exec(`INSERT INTO event_series (slug, title) VALUES ('elisenbrunnen', 'Balfolk am Elisenbrunnen')`)
	if err != nil {
		t.Fatalf("insert series: %v", err)
	}
	seriesID64, _ := res.LastInsertId()
	seriesID := int(seriesID64)

	eventID, _, _, err := insertEvent(db, EventInput{
		Title: "One-off event", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true,
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	db.Exec("UPDATE events SET changed_at=0 WHERE id=?", eventID)

	body, _ := json.Marshal(map[string]any{"ids": []int{eventID}})
	req := httptest.NewRequest("POST", "/api/v1/series/"+strconv.Itoa(seriesID)+"/assign-events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", "admin")
	req.SetPathValue("id", strconv.Itoa(seriesID))
	w := httptest.NewRecorder()
	assignSeriesEvents(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}

	var gotSeriesID int
	var changedAt int64
	var changedBy string
	db.QueryRow("SELECT series_id, changed_at, changed_by FROM events WHERE id=?", eventID).Scan(&gotSeriesID, &changedAt, &changedBy)
	if gotSeriesID != seriesID {
		t.Errorf("series_id = %d, want %d", gotSeriesID, seriesID)
	}
	if changedAt == 0 {
		t.Error("changed_at was not bumped")
	}
	if changedBy == "" {
		t.Error("changed_by was not set")
	}
}
