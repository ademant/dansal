package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSmokeRenderLocationPage(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/location/4", nil)

	render := func(name string, data LocationPageData) {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			renderTemplate(rec, tmpls.location, tmplData(req, cfg, i18n, data.Location.Location, data))
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, body)
			}
			if strings.Contains(string(body), "template error") {
				t.Fatalf("template execution error, body tail: %s", body[max(0, len(body)-500):])
			}
			if !strings.Contains(string(body), "</html>") {
				t.Fatalf("truncated render (no closing </html>), body tail: %s", body[max(0, len(body)-300):])
			}
			// Syntax-only (node --check) — see admin_location_edit_test.go's
			// "with-coords" case comment for why this can't catch #1283's
			// actual runtime "L is not defined" bug on its own.
			checkInlineJS(t, string(body))
		})
	}

	render("plain-location", LocationPageData{
		Location: Location{ID: 4, Location: "Bürgerhaus Stollwerck", Address: "Dillenburger Str.", Town: "Köln"},
	})

	// #1283: only a location with coordinates renders loc-map's inline
	// script at all (data-lat/data-lng feed L.map(...)) — no existing case
	// here exercised that.
	lat, lon := 50.9375, 6.9603
	render("with-coords", LocationPageData{
		Location: Location{ID: 9, Location: "Bürgerhaus Stollwerck", Latitude: &lat, Longitude: &lon},
	})

	x, y := 0.42, 0.61
	render("building-with-siteplan-and-rooms", LocationPageData{
		Location: Location{ID: 4, Location: "Bürgerhaus Stollwerck", SitePlanURL: "/api/v1/location-images/4", Children: []Location{
			{ID: 55, Location: "Room A", PlanX: &x, PlanY: &y, FloorCondition: "parquet"},
			{ID: 56, Location: "Room B"},
		}},
	})

	buildingID := 4
	render("room-page", LocationPageData{
		Location: Location{ID: 55, Location: "Room A", ParentID: &buildingID},
	})

	// #1188: an explicit "no toilet" (false) renders its own warning badge,
	// distinct from a location that simply has no attributes recorded at all.
	render("open-air-no-toilet", LocationPageData{
		Location: Location{ID: 7, Location: "Elisenbrunnen", Town: "Aachen", Attributes: map[string]bool{"toilet": false}},
	})
	render("has-toilet", LocationPageData{
		Location: Location{ID: 8, Location: "Bürgerhaus Stollwerck", Attributes: map[string]bool{"toilet": true}},
	})
}

// TestLocationPageToiletBadge asserts the actual badge content (not just
// error-free rendering, unlike the smoke test above): explicit false shows
// the "no toilet" warning badge and not the "has toilet" one, and vice versa
// for explicit true (#1188).
func TestLocationPageToiletBadge(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/location/7", nil)

	render := func(attrs map[string]bool) string {
		rec := httptest.NewRecorder()
		data := LocationPageData{Location: Location{ID: 7, Location: "Elisenbrunnen", Attributes: attrs}}
		renderTemplate(rec, tmpls.location, tmplData(req, cfg, i18n, data.Location.Location, data))
		body, _ := io.ReadAll(rec.Body)
		return string(body)
	}

	t.Run("explicit no toilet", func(t *testing.T) {
		body := render(map[string]bool{"toilet": false})
		if !strings.Contains(body, "🚫🚻") {
			t.Error("expected the no-toilet warning badge (🚫🚻)")
		}
	})

	t.Run("explicit has toilet", func(t *testing.T) {
		body := render(map[string]bool{"toilet": true})
		if !strings.Contains(body, "🚻") {
			t.Error("expected the has-toilet badge (🚻)")
		}
		if strings.Contains(body, "🚫🚻") {
			t.Error("did not expect the no-toilet warning badge when toilet is explicitly true")
		}
	})

	t.Run("unset shows neither badge", func(t *testing.T) {
		body := render(nil)
		if strings.Contains(body, "🚻") {
			t.Error("did not expect any toilet badge when attribute is unset")
		}
	})
}

// TestLocationPageNoUpcomingNearbyHint covers #1436: a venue with no
// upcoming events shows the nearby-radius fallback link instead of an empty
// calendar, and #1437's recent-past-events teaser alongside it.
func TestLocationPageNoUpcomingNearbyHint(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/location/16", nil)
	req.Header.Set("Accept-Language", "en")

	data := LocationPageData{
		Location: Location{ID: 16, Location: "Tanzplatte Rhein"},
		PastEvents: []Event{
			{ID: 900, Title: "Sommerfest", StartTime: "2025-07-03T19:00:00Z"},
		},
		PastEventsTotal: 12,
		NearbyHint: &NearbyHint{
			Count: 4, RadiusKm: 100,
			SearchURL: "/search?from=2026-10-06&label=Tanzplatte+Rhein&lat=50.73&lng=7.1&radius=100&to=2027-01-06",
		},
	}
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.location, tmplData(req, cfg, i18n, data.Location.Location, data))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}

	for _, want := range []string{
		"No upcoming events here.",
		// html/template escapes "+" to its HTML entity in a URL attribute —
		// still decodes to a literal "+" in the browser, just not bare in markup.
		`href="/search?from=2026-10-06&amp;label=Tanzplatte&#43;Rhein&amp;lat=50.73&amp;lng=7.1&amp;radius=100&amp;to=2027-01-06"`,
		"4 event(s) within 100 km",
		"Sommerfest",
		"12 event(s) here",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("no-upcoming render missing %q, body tail: %s", want, body[max(0, len(body)-600):])
		}
	}
	// The empty calendar/empty-list fallback this replaces must be gone.
	if strings.Contains(string(body), "No events found for this organisation") {
		t.Error("should not fall back to the generic org_no_events message")
	}
}

// TestLocationPageUpcomingWithCollapsedPastEvents covers #1437's second
// case: upcoming events keep the calendar/list as before, with past events
// in a separate, collapsed (not auto-open) <details> section.
func TestLocationPageUpcomingWithCollapsedPastEvents(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/location/17", nil)
	req.Header.Set("Accept-Language", "en")

	data := LocationPageData{
		Location: Location{ID: 17, Location: "Bürgerhaus Stollwerck"},
		Events:   []Event{{ID: 500, Title: "Herbstball", StartTime: "2033-05-18T03:33:20Z"}},
		PastEvents: []Event{
			{ID: 499, Title: "Sommerfest", StartTime: "2025-07-03T19:00:00Z"},
		},
		PastEventsTotal: 3,
	}
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.location, tmplData(req, cfg, i18n, data.Location.Location, data))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}

	if !strings.Contains(string(body), `<details class="loc-past-events">`) {
		t.Error("expected a collapsed <details> past-events section")
	}
	if strings.Contains(string(body), `<details class="loc-past-events" open>`) {
		t.Error("past-events <details> must be collapsed by default")
	}
	for _, want := range []string{"Herbstball", "Sommerfest", "Past events (1)", "3 event(s) here"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("render missing %q, body tail: %s", want, body[max(0, len(body)-600):])
		}
	}
}
