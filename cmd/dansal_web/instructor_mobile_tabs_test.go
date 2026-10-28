package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInstructorPhoneTabs covers #1699: the instructor page renders the three
// phone tabs (Info, Calendar, Events). Calendar and Events are marked empty when
// there are no upcoming or listed events, and the Events tab keeps its
// include-past form open after reload.
func TestInstructorPhoneTabs(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/instructors/4", nil)

	render := func(data InstructorPageData) string {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.instructor, tmplData(req, cfg, i18n, data.Instructor.Name, data))
		body, _ := io.ReadAll(rec.Body)
		html := string(body)
		if strings.Contains(html, "template error") {
			t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
		}
		return html
	}

	t.Run("no events", func(t *testing.T) {
		html := render(InstructorPageData{Instructor: Instructor{ID: 4, Name: "Anna Test"}, HasPast: true})
		for _, want := range []string{
			`id="instr-tabs" data-mtab="info"`,
			`data-tab="info" aria-controls="instr-tabs" aria-selected="true"`,
			`class="mtab is-empty" data-tab="calendar"`,
			`class="mtab is-empty" data-tab="events"`,
			`<div class="mtab-panel" data-tab="calendar">`,
			`<div class="mtab-panel" data-tab="events">`,
			`<input type="hidden" name="tab" value="events">`,
			`dansalMtabs(root`,
		} {
			if !strings.Contains(html, want) {
				t.Errorf("instructor page missing %s", want)
			}
		}
	})

	t.Run("with events", func(t *testing.T) {
		html := render(InstructorPageData{
			Instructor:     Instructor{ID: 4, Name: "Anna Test"},
			Events:         []Event{{ID: 1, Title: "Workshop", StartTime: "2033-05-18T03:33:20Z"}},
			UpcomingEvents: []Event{{ID: 1, Title: "Workshop", StartTime: "2033-05-18T03:33:20Z"}},
		})
		for _, want := range []string{
			`class="mtab" data-tab="calendar"`,
			`class="mtab" data-tab="events"`,
			`window.mmap = map;`,
			`startIndex: dansalFirstEventMonthIndex(calEvents, months)`,
		} {
			if !strings.Contains(html, want) {
				t.Errorf("instructor page with events missing %s", want)
			}
		}
	})
}
