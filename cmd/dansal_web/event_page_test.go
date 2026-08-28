package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSmokeRenderEventPage(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/events/1", nil)

	render := func(name string, ev Event) {
		t.Run(name, func(t *testing.T) {
			td := tmplData(req, cfg, i18n, ev.Title, EventData{Event: ev})
			rec := httptest.NewRecorder()
			renderTemplate(rec, tmpls.event, td)
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
		})
	}

	render("no-location", Event{ID: 1, Title: "No venue", StartTime: "2026-07-01T20:00:00"})

	buildingID := 10
	render("building-location", Event{
		ID: 2, Title: "At a building", StartTime: "2026-07-01T20:00:00",
		Location: &Location{ID: buildingID, Location: "Big Hall", Address: "Main St 1", Town: "Testville"},
	})

	roomParent := buildingID
	render("room-location", Event{
		ID: 3, Title: "In a room", StartTime: "2026-07-01T20:00:00",
		Location: &Location{ID: 11, Location: "Studio 2", ParentID: &roomParent, Address: "Main St 1", Town: "Testville"},
	})
}

// TestSmokeRenderEventPageTimetableHistory exercises the "Recent schedule
// changes" section (#1176) — both the logged-out (ChangedBy hidden) and
// logged-in (ChangedBy shown) paths, since that visibility split lives
// entirely in the template ({{if and $.User .ChangedBy}}).
func TestSmokeRenderEventPageTimetableHistory(t *testing.T) {
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

	ev := Event{ID: 4, Title: "Festival with history", StartTime: "2026-07-01T20:00:00"}
	history := []TimetableHistoryEntry{
		{ID: 2, ChangedAt: "2026-06-30T12:00:00", ChangedBy: "Alice"},
		{ID: 1, ChangedAt: "2026-06-29T09:00:00", ChangedBy: "Bob"},
	}

	render := func(name string, su *SessionUser) {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/events/4", nil)
			if su != nil {
				req = withSessionUser(req, su)
			}
			td := tmplData(req, cfg, i18n, ev.Title, EventData{Event: ev, TimetableHistory: history})
			rec := httptest.NewRecorder()
			renderTemplate(rec, tmpls.event, td)
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, body)
			}
			if strings.Contains(string(body), "template error") {
				t.Fatalf("template execution error, body tail: %s", body[max(0, len(body)-500):])
			}
			hasAttribution := strings.Contains(string(body), "Alice")
			if su != nil && !hasAttribution {
				t.Fatal("expected ChangedBy to be shown to a logged-in viewer")
			}
			if su == nil && hasAttribution {
				t.Fatal("expected ChangedBy to be hidden from an anonymous viewer")
			}
		})
	}

	render("anonymous", nil)
	render("logged-in", &SessionUser{ID: 1, Role: "admin"})
}

// TestSmokeRenderEventPageStarredTimetable exercises the starring markup
// (#1177) for both the single-room list layout and the multi-room grid
// layout — the two branches that each needed their own data-entry-id /
// star-button addition.
func TestSmokeRenderEventPageStarredTimetable(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/events/5", nil)

	render := func(name string, ev Event) {
		t.Run(name, func(t *testing.T) {
			td := tmplData(req, cfg, i18n, ev.Title, EventData{Event: ev})
			rec := httptest.NewRecorder()
			renderTemplate(rec, tmpls.event, td)
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, body)
			}
			if strings.Contains(string(body), "template error") {
				t.Fatalf("template execution error, body tail: %s", body[max(0, len(body)-500):])
			}
			if !strings.Contains(string(body), `data-entry-id="10"`) {
				t.Fatal("expected a data-entry-id attribute on the timetable entry")
			}
			if !strings.Contains(string(body), "tt-star-btn") {
				t.Fatal("expected a star toggle button on the timetable entry")
			}
			// #1179's "Now/Next" indicator container.
			if !strings.Contains(string(body), `id="tt-nextup"`) {
				t.Fatal("expected the Now/Next indicator container")
			}
		})
	}

	render("single-room", Event{
		ID: 5, Title: "Single room", StartTime: "2026-07-01T20:00:00", EndTime: "2026-07-01T23:00:00",
		Timetable: []TimetableEntry{{ID: 10, StartTime: "20:00", EndTime: "21:00", Title: "Opening bal"}},
	})

	loc1, loc2 := 1, 2
	render("multi-room", Event{
		ID: 5, Title: "Multi room", StartTime: "2026-07-01T20:00:00", EndTime: "2026-07-01T23:00:00",
		Timetable: []TimetableEntry{
			{ID: 10, StartTime: "20:00", EndTime: "21:00", Title: "Opening bal", LocationID: &loc1, LocationName: "Main hall"},
			{ID: 12, StartTime: "20:00", EndTime: "21:30", Title: "Workshop", EntryType: "workshop", LocationID: &loc2, LocationName: "Side room"},
		},
	})
}
