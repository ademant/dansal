package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// TestGetCitiesIncludeInactive covers #1506: a town whose only location has
// no upcoming published event is omitted by default (the /cities directory
// and sitemap should not advertise it), but included — with event_count=0 —
// when ?include_inactive=true, which /city/{slug} uses to resolve towns with
// only past events instead of 404ing.
func TestGetCitiesIncludeInactive(t *testing.T) {
	setupDedupTestDB(t)

	locID := insertTestLocation(t, 50.73, 7.10)
	if _, err := db.Exec(`UPDATE locations SET town = 'Erding' WHERE id = ?`, locID); err != nil {
		t.Fatalf("set town: %v", err)
	}

	past := time.Now().Add(-48 * time.Hour).Unix()
	if _, _, _, err := insertEvent(db, EventInput{
		Title: "Past Ball", StartTime: past, EndTime: past + 3600,
		IsPublished: true, LocationID: int64(locID),
	}); err != nil {
		t.Fatalf("insert past event: %v", err)
	}
	db.Exec("UPDATE events SET email_verified = 1")

	// Default: town with no upcoming event is omitted.
	req := httptest.NewRequest("GET", "/api/v1/locations/cities", nil)
	w := httptest.NewRecorder()
	getCities(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var cities []CityInfo
	if err := json.Unmarshal(w.Body.Bytes(), &cities); err != nil {
		t.Fatalf("decode: %v, body=%s", err, w.Body.String())
	}
	for _, c := range cities {
		if c.Town == "Erding" {
			t.Fatalf("Erding present without include_inactive: %+v", c)
		}
	}

	// include_inactive=true: town is present with event_count=0.
	req = httptest.NewRequest("GET", "/api/v1/locations/cities?include_inactive=true", nil)
	w = httptest.NewRecorder()
	getCities(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	cities = nil
	if err := json.Unmarshal(w.Body.Bytes(), &cities); err != nil {
		t.Fatalf("decode: %v, body=%s", err, w.Body.String())
	}
	var found *CityInfo
	for i := range cities {
		if cities[i].Town == "Erding" {
			found = &cities[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("Erding missing with include_inactive=true")
	}
	if found.EventCount != 0 {
		t.Errorf("EventCount = %d, want 0 (past event doesn't count as upcoming)", found.EventCount)
	}
}
