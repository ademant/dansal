package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1612: an event flagged "no live music" shows the recorded-music notice where
// the musicians would be, and its JSON-LD has no performer.
func TestEventPageNoLiveMusic(t *testing.T) {
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

	render := func(ev Event) string {
		t.Helper()
		td := tmplData(req, cfg, i18n, ev.Title, EventData{Event: ev})
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.event, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK || strings.Contains(string(body), "template error") {
			t.Fatalf("status=%d body tail: %s", rec.Code, body[max(0, len(body)-300):])
		}
		return string(body)
	}

	recorded := render(Event{ID: 1, Title: "Recorded evening", StartTime: "2026-07-01T20:00:00", NoLiveMusic: true})
	if !strings.Contains(recorded, i18n.Strings("de").T("evt_no_live_music_display")) {
		t.Error("a flagged event without musicians must show the recorded-music notice")
	}
	if strings.Contains(recorded, `"performer"`) {
		t.Error("a flagged event must not carry a performer in its JSON-LD")
	}

	live := render(Event{
		ID: 2, Title: "Live evening", StartTime: "2026-07-01T20:00:00",
		Musicians: []Musician{{ID: 7, Bandname: "Band Seven"}},
	})
	if strings.Contains(live, i18n.Strings("de").T("evt_no_live_music_display")) {
		t.Error("an event with musicians must not show the recorded-music notice")
	}
	if !strings.Contains(live, `"performer"`) {
		t.Error("an event with musicians should keep its performer in the JSON-LD")
	}
}
