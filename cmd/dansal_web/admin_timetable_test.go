package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAdminTimetablePageResolvesBuildingPastListLimit covers #1541:
// GetLocations caps its result at apiListLimit (1000) rows, so a building
// whose own location row sits past that cap is missing from the all-
// locations list the page used to resolve building names from — the super-
// header (and the room picker's buildingName) silently went blank. The
// building must now be resolved by a direct GET /api/v1/locations/{id}
// fallback instead.
func TestAdminTimetablePageResolvesBuildingPastListLimit(t *testing.T) {
	const buildingID = 123456 // stands in for "beyond row 1000"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/events/99", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Event{ID: 99, Title: "Test Event", LocationID: intPtr(buildingID)})
	})
	// Simulates the capped whole-table list: the target building is absent,
	// standing in for "past row 1000" without actually seeding 1000 rows.
	mux.HandleFunc("GET /api/v1/locations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Location{{ID: 1, Location: "Other Hall"}})
	})
	mux.HandleFunc("GET /api/v1/locations/123456", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Location{ID: buildingID, Location: "Beyond Row 1000 Hall", ShortName: "B1000"})
	})
	mux.HandleFunc("GET /api/v1/musicians", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Musician{})
	})
	mux.HandleFunc("GET /api/v1/instructors", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Instructor{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	tmpls := loadTemplates()
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test"}
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	handler := adminTimetablePageHandler(cfg, tmpls, client, i18n)

	req := httptest.NewRequest(http.MethodGet, "/admin/events/99/timetable", nil)
	req.SetPathValue("id", "99")
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "B1000") {
		t.Errorf("page is missing the past-row-1000 building's short name; body=%s", body)
	}
}
