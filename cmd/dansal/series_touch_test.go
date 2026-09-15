package main

import (
	"bytes"
	"database/sql"
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

// TestUpdateSeriesDescriptionsSkipsOthers covers #1316: batching the
// per-update series-membership check into one WHERE id IN (...) query must
// not change which updates are accepted — an event belonging to a
// *different* series, and a nonexistent event id, are both still silently
// skipped (no error), while the matching event is still updated.
func TestUpdateSeriesDescriptionsSkipsOthers(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")

	res, _ := db.Exec(`INSERT INTO event_series (slug, title) VALUES ('own-series', 'Own Series')`)
	seriesID64, _ := res.LastInsertId()
	seriesID := int(seriesID64)
	res, _ = db.Exec(`INSERT INTO event_series (slug, title) VALUES ('other-series', 'Other Series')`)
	otherSeriesID64, _ := res.LastInsertId()
	otherSeriesID := int(otherSeriesID64)

	ownEventID, _, _, err := insertEvent(db, EventInput{Title: "Own", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true})
	if err != nil {
		t.Fatalf("insert own event: %v", err)
	}
	db.Exec("UPDATE events SET series_id=? WHERE id=?", seriesID, ownEventID)

	otherEventID, _, _, err := insertEvent(db, EventInput{Title: "Other", StartTime: 2000010000, EndTime: 2000013600, IsPublished: true})
	if err != nil {
		t.Fatalf("insert other event: %v", err)
	}
	db.Exec("UPDATE events SET series_id=? WHERE id=?", otherSeriesID, otherEventID)

	const missingEventID = 999999
	body, _ := json.Marshal(map[string]any{
		"updates": []map[string]any{
			{"event_id": ownEventID, "description": "updated"},
			{"event_id": otherEventID, "description": "should not apply"},
			{"event_id": missingEventID, "description": "should not error"},
		},
	})
	req := adminReq("POST", "/api/v1/series/"+strconv.Itoa(seriesID)+"/descriptions", body)
	req.SetPathValue("id", strconv.Itoa(seriesID))
	w := httptest.NewRecorder()
	updateSeriesDescriptions(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}

	var ownDesc, otherDesc string
	db.QueryRow("SELECT description FROM events WHERE id=?", ownEventID).Scan(&ownDesc)
	db.QueryRow("SELECT description FROM events WHERE id=?", otherEventID).Scan(&otherDesc)
	if ownDesc != "updated" {
		t.Errorf("own event description = %q, want %q", ownDesc, "updated")
	}
	if otherDesc == "should not apply" {
		t.Error("an update for an event in a different series was applied")
	}
}

// TestAssignSeriesEventsSkipsOrgMismatchAndMissing covers #1316: batching
// the per-id org-compatibility check must not change which events are
// assigned — an event whose organization_id conflicts with the series' own,
// and a nonexistent event id, are both still silently skipped.
func TestAssignSeriesEventsSkipsOrgMismatchAndMissing(t *testing.T) {
	setupDedupTestDB(t)
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")
	db.Exec("INSERT INTO organizations (id, name) VALUES (1, 'Series Org'), (2, 'Other Org')")

	res, _ := db.Exec(`INSERT INTO event_series (slug, title, organization_id) VALUES ('org-series', 'Org Series', 1)`)
	seriesID64, _ := res.LastInsertId()
	seriesID := int(seriesID64)

	orgID1 := 1
	compatibleEventID, _, _, err := insertEvent(db, EventInput{Title: "Compatible", StartTime: 2000000000, EndTime: 2000003600, IsPublished: true, OrganizationID: &orgID1})
	if err != nil {
		t.Fatalf("insert compatible event: %v", err)
	}
	orgID2 := 2
	mismatchEventID, _, _, err := insertEvent(db, EventInput{Title: "Mismatched org", StartTime: 2000010000, EndTime: 2000013600, IsPublished: true, OrganizationID: &orgID2})
	if err != nil {
		t.Fatalf("insert mismatched event: %v", err)
	}

	const missingEventID = 999999
	body, _ := json.Marshal(map[string]any{"ids": []int{compatibleEventID, mismatchEventID, missingEventID}})
	req := adminReq("POST", "/api/v1/series/"+strconv.Itoa(seriesID)+"/assign-events", body)
	req.SetPathValue("id", strconv.Itoa(seriesID))
	w := httptest.NewRecorder()
	assignSeriesEvents(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}

	var compatibleSeriesID sql.NullInt64
	var mismatchSeriesID sql.NullInt64
	db.QueryRow("SELECT series_id FROM events WHERE id=?", compatibleEventID).Scan(&compatibleSeriesID)
	db.QueryRow("SELECT series_id FROM events WHERE id=?", mismatchEventID).Scan(&mismatchSeriesID)
	if !compatibleSeriesID.Valid || int(compatibleSeriesID.Int64) != seriesID {
		t.Errorf("compatible event series_id = %v, want %d", compatibleSeriesID, seriesID)
	}
	if mismatchSeriesID.Valid {
		t.Errorf("mismatched-org event was assigned to the series (series_id=%v), want left alone", mismatchSeriesID)
	}
}
