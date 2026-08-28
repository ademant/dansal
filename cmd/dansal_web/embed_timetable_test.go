package main

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSmokeRenderEmbedTimetable exercises embed_timetable.html (#1175) for
// the three shapes timetableDays/timetableGrid can hand it: no timetable at
// all, a single day with a single room (list layout), and a single day with
// two rooms (grid layout) — the grid path is the one most likely to break on
// a stray {{end}} since it's the deepest-nested branch.
func TestSmokeRenderEmbedTimetable(t *testing.T) {
	tmpls := loadTemplates()
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test"}
	strs := i18n.Strings("en")

	render := func(name string, ev Event) {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			renderEmbed(rec, tmpls.embedTimetable, map[string]any{
				"Lang":     "en",
				"Nonce":    "test-nonce",
				"Event":    ev,
				"Strings":  strs,
				"BaseURL":  "https://" + cfg.Domain,
				"SiteName": "dansal",
			})
			body, _ := io.ReadAll(rec.Body)
			if strings.Contains(string(body), "template error") {
				t.Fatalf("template execution error, body: %s", body)
			}
			if !strings.Contains(string(body), "</html>") {
				t.Fatalf("truncated render (no closing </html>), body: %s", body)
			}
			// #1179's "Now/Next" indicator container only renders when
			// there's a timetable at all.
			hasNextup := strings.Contains(string(body), `id="tt-nextup"`)
			if len(ev.Timetable) > 0 && !hasNextup {
				t.Fatal("expected the Now/Next indicator container")
			}
			if len(ev.Timetable) == 0 && hasNextup {
				t.Fatal("did not expect the Now/Next indicator container without a timetable")
			}
		})
	}

	base := Event{ID: 1, Title: "Test Festival", StartTime: "2026-09-15T18:00:00Z", EndTime: "2026-09-16T02:00:00Z", IsPublished: true}

	render("no-timetable", base)

	single := base
	single.Timetable = []TimetableEntry{
		{StartTime: "18:00", EndTime: "19:00", Title: "Opening bal", EntryType: "bal"},
	}
	render("single-room", single)

	multi := base
	multi.Timetable = []TimetableEntry{
		{StartTime: "18:00", EndTime: "19:00", Title: "Opening bal", EntryType: "bal", LocationID: intPtr(1), LocationName: "Main hall"},
		{StartTime: "18:00", EndTime: "19:30", Title: "Beginners workshop", EntryType: "workshop", LocationID: intPtr(2), LocationName: "Side room"},
	}
	render("multi-room", multi)
}
