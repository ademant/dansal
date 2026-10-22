package main

import (
	"database/sql"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The add-to-calendar dropdown (#1583): the location and agenda pages render
// their subscribe links with the site domain and the scope's feed path. A
// broken template or a wrong feed slug shows up here, not only in the e2e run.
func TestCalendarMenuLinks(t *testing.T) {
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

	render := func(t *testing.T, path string, tmpl *template.Template, title string, data any) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpl, tmplData(req, cfg, i18n, title, data))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		if strings.Contains(string(body), "template error") {
			t.Fatalf("template execution error: %s", body[max(0, len(body)-300):])
		}
		return string(body)
	}

	t.Run("location", func(t *testing.T) {
		body := render(t, "/location/7", tmpls.location, "Elisenbrunnen",
			LocationPageData{Location: Location{ID: 7, Location: "Elisenbrunnen"}})
		for _, want := range []string{
			`href="webcal://example.test/feed/location/elisenbrunnen/events.ics"`,
			`href="/feed/location/elisenbrunnen/events.ics"`,
			`href="/feed/location/elisenbrunnen/events.rss"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("location page missing %s", want)
			}
		}
	})

	t.Run("agenda", func(t *testing.T) {
		body := render(t, "/agenda", tmpls.agenda, "Agenda", struct{ Days []agendaDay }{})
		for _, want := range []string{
			`href="webcal://example.test/feed/events.ics"`,
			`href="/feed/events.ics"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("agenda page missing %s", want)
			}
		}
		if strings.Contains(body, ".ical") {
			t.Error("agenda page still links a .ical feed")
		}
	})
}
