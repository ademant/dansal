package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// TestGroupFoldedFestivals covers the wp-dansal-derived folding rule (#1144):
// a location with an edition in the selected year keeps it; a location with
// none falls back to its single most recent edition from any year.
func TestGroupFoldedFestivals(t *testing.T) {
	events := []Event{
		{ID: 1, LocationID: intPtr(10), StartTime: "2026-07-10T10:00:00Z"}, // venue 10, in-year
		{ID: 2, LocationID: intPtr(10), StartTime: "2025-07-10T10:00:00Z"}, // venue 10, past (superseded)
		{ID: 3, LocationID: intPtr(20), StartTime: "2024-08-01T10:00:00Z"}, // venue 20, only past edition
		{ID: 4, LocationID: intPtr(20), StartTime: "2023-08-01T10:00:00Z"}, // venue 20, older past edition
		{ID: 5, LocationID: intPtr(30), StartTime: "2026-09-01T10:00:00Z"}, // venue 30, in-year
	}

	yearEvents, folded := groupFoldedFestivals(events, 2026)

	if len(yearEvents) != 2 {
		t.Fatalf("expected 2 year events, got %d: %+v", len(yearEvents), yearEvents)
	}
	gotIDs := map[int]bool{}
	for _, e := range yearEvents {
		gotIDs[e.ID] = true
	}
	if !gotIDs[1] || !gotIDs[5] {
		t.Errorf("expected year events to include IDs 1 and 5, got %+v", yearEvents)
	}

	if len(folded) != 1 {
		t.Fatalf("expected 1 folded event (venue 20's latest), got %d: %+v", len(folded), folded)
	}
	if folded[0].ID != 3 {
		t.Errorf("expected folded event to be the most recent edition (ID 3), got ID %d", folded[0].ID)
	}
}

// TestGroupFoldedFestivalsSkipsMissingLocation ensures events with no
// location_id (which the map/calendar can't place anyway) don't panic and
// are simply excluded from both buckets.
func TestGroupFoldedFestivalsSkipsMissingLocation(t *testing.T) {
	events := []Event{
		{ID: 1, LocationID: nil, StartTime: "2026-07-10T10:00:00Z"},
	}
	yearEvents, folded := groupFoldedFestivals(events, 2026)
	if len(yearEvents) != 0 || len(folded) != 0 {
		t.Errorf("expected both buckets empty, got yearEvents=%+v folded=%+v", yearEvents, folded)
	}
}

// TestFestivalsFoldedTableNotFaded covers #1426: the "not yet scheduled"
// table used opacity:.75, which pushed its links and type badges below WCAG
// AA contrast. It must render without any opacity on the folded table.
func TestFestivalsFoldedTableNotFaded(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	req := httptest.NewRequest(http.MethodGet, "/festivals", nil)
	rec := httptest.NewRecorder()
	renderTemplate(rec, loadTemplates().festivals, tmplData(req, &Config{Domain: "example.test"}, loadI18n(""), "test", FestivalsData{
		Year:         2026,
		FoldedEvents: []Event{{ID: 2745, Title: "Winneweh", StartTime: "2025-07-30T00:00:00+02:00", Tags: []string{"festival", "bal-folk"}}},
	}))
	body := rec.Body.String()
	if !strings.Contains(body, `class="event-table fest-folded"`) || !strings.Contains(body, "Winneweh") {
		t.Fatal("folded festivals table not rendered")
	}
	if regexp.MustCompile(`fest-folded[^{]*\{[^}]*opacity`).MatchString(body) {
		t.Error("folded festivals table is faded with opacity again — breaks WCAG contrast (#1426)")
	}
}
