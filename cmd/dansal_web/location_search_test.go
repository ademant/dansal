package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1414: venue picker on the suggest form.

func TestMatchLocations(t *testing.T) {
	parent := 1
	locs := []Location{
		{ID: 1, Location: "Karlsburg Durlach", Town: "Karlsruhe", Aliases: []string{"Karlsburg"}},
		{ID: 2, Location: "Kulturhaus Mitte", ShortName: "KuMi", Town: "Freiburg"},
		{ID: 3, Location: "Großer Saal", Town: "Karlsruhe", ParentID: &parent}, // room: skipped
		{ID: 4, Location: "Café Durlach", Town: "Karlsruhe"},
	}
	names := func(hits []LocationHit) []string {
		var out []string
		for _, h := range hits {
			out = append(out, h.Name)
		}
		return out
	}
	cases := []struct {
		q    string
		want []string
	}{
		{"karlsburg", []string{"Karlsburg Durlach"}},
		{"durlach", []string{"Café Durlach", "Karlsburg Durlach"}},
		{"cafe", []string{"Café Durlach"}},                           // diacritics-insensitive
		{"kumi", []string{"Kulturhaus Mitte"}},                       // short name
		{"karlsruhe", []string{"Café Durlach", "Karlsburg Durlach"}}, // town only, room skipped
		{"durlach karlsruhe", []string{"Café Durlach", "Karlsburg Durlach"}},
		{"k", nil}, // too short
	}
	for _, c := range cases {
		if got := names(matchLocations(c.q, locs)); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("matchLocations(%q) = %q, want %q", c.q, got, c.want)
		}
	}
	// Name matches rank before town-only matches.
	locs = append(locs, Location{ID: 5, Location: "Saal am Markt", Town: "Freiburg Kulturhaus"})
	if got := names(matchLocations("kulturhaus", locs)); len(got) != 2 || got[0] != "Kulturhaus Mitte" {
		t.Errorf("ranking: got %q, want Kulturhaus Mitte first", got)
	}
}

func TestNewVenueEventIDs(t *testing.T) {
	loc := 7
	old := 8
	locs := []Location{
		{ID: 7, CreatedAt: "2026-10-01 09:16:18"},
		{ID: 8, CreatedAt: "2025-01-01 10:00:00"},
	}
	events := []Event{
		{ID: 1, LocationID: &loc, CreatedAt: "2026-10-01 09:16:19"},                    // new venue with suggestion
		{ID: 2, LocationID: &old, CreatedAt: "2026-10-01 09:16:19"},                    // existing venue
		{ID: 3, LocationID: &loc, CreatedAt: "2026-10-01 09:16:19", IsPublished: true}, // already published
		{ID: 4, CreatedAt: "2026-10-01 09:16:19"},                                      // no venue
	}
	got := newVenueEventIDs(events, locs)
	if !got[1] || got[2] || got[3] || got[4] {
		t.Errorf("newVenueEventIDs = %v, want only event 1", got)
	}
}

func TestSuggestVenueStepRenders(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	req := httptest.NewRequest(http.MethodGet, "/events/suggest", nil)
	rec := httptest.NewRecorder()
	renderTemplate(rec, loadTemplates().suggestEvent, tmplData(req, &Config{Domain: "example.test"}, loadI18n(""), "test", SuggestPageData{}))
	body := rec.Body.String()
	checkInlineJS(t, body)
	for _, want := range []string{
		`id="sg-venue-picker"`, `id="sg-venue-chip"`, `id="sg-venue-new" class="sg-venue-new" hidden`,
		`name="location"`, `name="address"`, `name="zipcode"`, `name="town"`, `name="country"`,
		`/search/locations?q=`, `window.sgVenueValid`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("suggest page missing %s", want)
		}
	}
	// The old fallback that named a venue after its house number is gone.
	if strings.Contains(body, "a.building || a.house_number") {
		t.Error("venue name still falls back to the house number")
	}
	for _, raw := range []string{"sg_venue_", "sg_help_address"} {
		if strings.Contains(body, raw) {
			t.Errorf("untranslated %s* key in page", raw)
		}
	}
}

func TestAdminEventsNewVenueBadge(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)
	loc := 291
	req := withSessionUser(httptest.NewRequest(http.MethodGet, "/admin/events", nil), &SessionUser{ID: 1, Role: "admin"})
	rec := httptest.NewRecorder()
	renderTemplate(rec, loadTemplates().adminEvents, tmplData(req, &Config{Domain: "example.test"}, loadI18n(""), "test", AdminEventsData{
		Events:           []Event{{ID: 1007, Title: "Journée baroque", LocationID: &loc, EmailVerified: true}},
		NewVenueEventIDs: map[int]bool{1007: true},
	}))
	if !strings.Contains(rec.Body.String(), `class="badge-new-venue" href="/admin/locations/291/edit"`) {
		t.Error("admin events: missing new-venue badge linking to the venue")
	}
}
