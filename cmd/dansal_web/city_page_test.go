package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCityPageNoUpcomingNearbyHint covers #1506: a town with no upcoming
// events renders 200 with city_no_upcoming + the #1436 nearby-radius hint,
// plus its recent past events shown directly (not behind the "show past
// events" button), instead of the search_no_results dead end.
func TestCityPageNoUpcomingNearbyHint(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/city/erding", nil)
	req.Header.Set("Accept-Language", "en")

	lat, lon := 48.3, 11.9
	data := CityData{
		City: City{Town: "Erding", Slug: "erding", Latitude: &lat, Longitude: &lon},
		PastEvents: []Event{
			{ID: 900, Title: "Sommerfest", StartTime: "2025-07-03T19:00:00Z"},
		},
		PastEventsTotal: 7,
		NearbyHint: &NearbyHint{
			Count: 4, RadiusKm: 100,
			SearchURL: "/search?from=2026-10-06&label=Erding&lat=48.3&lng=11.9&radius=100&to=2027-01-06",
		},
	}
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.city, tmplData(req, cfg, i18n, "Balfolk in Erding", data))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}

	for _, want := range []string{
		"No upcoming events in Erding.",
		"4 event(s) within 100 km",
		"Sommerfest",
		"7 event(s) here",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("no-upcoming render missing %q, body tail: %s", want, body[max(0, len(body)-600):])
		}
	}
	// The dead-end fallback this replaces must be gone — "search_no_results"
	// was never defined in i18n.yaml, so it rendered as the bare key name.
	if strings.Contains(string(body), "search_no_results") {
		t.Error("should not fall back to the undefined search_no_results key")
	}
	// Past events must render directly, not behind the on-demand button.
	if strings.Contains(string(body), `id="city-past-btn"`) {
		t.Error("should not show the on-demand past-events button when there are no upcoming events")
	}
}

// TestCityPageUpcomingKeepsPastEventsButton covers the unchanged case: a
// town with upcoming events keeps the existing event list and the on-demand
// "show past events" button (#1506 only changes the no-upcoming-events path).
func TestCityPageUpcomingKeepsPastEventsButton(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/city/koeln", nil)
	req.Header.Set("Accept-Language", "en")

	data := CityData{
		City:   City{Town: "Köln", Slug: "koeln"},
		Events: []Event{{ID: 500, Title: "Herbstball", StartTime: "2033-05-18T03:33:20Z"}},
	}
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.city, tmplData(req, cfg, i18n, "Balfolk in Köln", data))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}
	if !strings.Contains(string(body), `id="city-past-btn"`) {
		t.Error("expected the on-demand past-events button when there are upcoming events")
	}
	if !strings.Contains(string(body), "Herbstball") {
		t.Error("expected the upcoming event to render")
	}
}
