package main

import (
	"database/sql"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"testing"
)

// TestInlineJSSyntax renders index.html and event.html with data that
// exercises their map-init script branches, extracts every <script> block,
// and syntax-checks each with `node --check`. A pure template-parse test
// wouldn't catch a JS syntax error since html/template treats script bodies
// as opaque text — this caught a real bug once already: `defer` on an inline
// script without `src` has no effect (it still runs synchronously at its
// parse position), so wrapping the app-logic script in `defer` alone (#1130)
// left it running before the deferred external Leaflet <script src> tags had
// executed, silently breaking the map. Fixed by wrapping the script body in
// a DOMContentLoaded listener instead, which is what this test guards.
func TestInlineJSSyntax(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}

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
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	scriptRe := regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)
	checkJS := func(t *testing.T, body string) {
		for i, m := range scriptRe.FindAllStringSubmatch(body, -1) {
			attrs, src := m[1], m[2]
			if src == "" {
				continue
			}
			// Skip non-JS payloads (JSON-LD structured data, the events-geo
			// JSON data island) — only classic/module script bodies are JS.
			if regexp.MustCompile(`type\s*=\s*["'](application/ld\+json|application/json)["']`).MatchString(attrs) {
				continue
			}
			f, err := os.CreateTemp("", "smoke*.js")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(f.Name())
			f.WriteString(src)
			f.Close()
			out, err := exec.Command("node", "--check", f.Name()).CombinedOutput()
			if err != nil {
				t.Errorf("script block %d: node --check failed: %v\n%s\n--- source ---\n%s", i, err, out, src)
			}
		}
	}

	t.Run("index", func(t *testing.T) {
		lat, lng := 48.1, 11.5
		events := []Event{{
			ID: 1, Title: "Test Ball", StartTime: "2026-07-01T20:00:00",
			Location: &Location{ID: 1, Location: "Hall", Latitude: &lat, Longitude: &lng},
		}}
		td := tmplData(req, cfg, i18n, "Events", IndexData{Events: events, HolidayDates: template.JS("[]")})
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.index, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
		checkJS(t, string(body))
	})

	t.Run("event-with-location", func(t *testing.T) {
		lat, lng := 48.1, 11.5
		ev := Event{
			ID: 1, Title: "Test event", StartTime: "2026-07-01T20:00:00",
			Location: &Location{ID: 1, Location: "Hall", Latitude: &lat, Longitude: &lng},
		}
		td := tmplData(req, cfg, i18n, ev.Title, EventData{Event: ev})
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.event, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
		checkJS(t, string(body))
	})

	// #1177: the starring/my-timetable/export script only renders inside
	// the {{if .Timetable}} section, so it needs its own case — neither
	// "index" nor "event-with-location" above has a timetable.
	t.Run("event-with-timetable", func(t *testing.T) {
		loc1, loc2 := 1, 2
		ev := Event{
			ID: 1, Title: "Test festival", StartTime: "2026-07-01T20:00:00", EndTime: "2026-07-01T23:00:00",
			Timetable: []TimetableEntry{
				{ID: 10, StartTime: "20:00", EndTime: "21:00", Title: "Opening bal", LocationID: &loc1, LocationName: "Main hall"},
				{ID: 12, StartTime: "20:00", EndTime: "21:30", Title: "Workshop", EntryType: "workshop", LocationID: &loc2, LocationName: "Side room"},
			},
		}
		td := tmplData(req, cfg, i18n, ev.Title, EventData{Event: ev})
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.event, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
		checkJS(t, string(body))
	})

	t.Run("embed-timetable", func(t *testing.T) {
		ev := Event{
			ID: 1, Title: "Test festival", StartTime: "2026-07-01T20:00:00", EndTime: "2026-07-01T23:00:00",
			Timetable: []TimetableEntry{{ID: 10, StartTime: "20:00", EndTime: "21:00", Title: "Opening bal"}},
		}
		rec := httptest.NewRecorder()
		renderEmbed(rec, tmpls.embedTimetable, map[string]any{
			"Lang": "en", "Nonce": "test-nonce", "Event": ev, "Strings": i18n.Strings("en"),
			"BaseURL": "https://example.test", "SiteName": "dansal",
		})
		body, _ := io.ReadAll(rec.Body)
		checkJS(t, string(body))
	})
}
