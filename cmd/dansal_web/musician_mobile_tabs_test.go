package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMusicianPhoneTabs covers #1673: the musician page renders the four phone
// tabs. Meta is marked empty when there are no links, gallery or media, and the
// Events tab keeps its include-past form on the Events tab after reload.
func TestMusicianPhoneTabs(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/musicians/7", nil)

	rec := httptest.NewRecorder()
	data := MusicianPageData{Musician: Musician{ID: 7, Bandname: "Test Band"}, Slug: "test-band", HasPast: true}
	renderTemplate(rec, tmpls.musician, tmplData(req, cfg, i18n, "Test Band", data))
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if strings.Contains(html, "template error") {
		t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
	}
	for _, want := range []string{
		`id="mus-tabs" data-mtab="info"`,
		`data-tab="info" aria-controls="mus-tabs" aria-selected="true"`,
		`class="mtab is-empty" data-tab="calendar"`,
		`class="mtab is-empty" data-tab="events"`,
		`class="mtab is-empty" data-tab="meta"`,
		`<div class="mtab-panel" data-tab="meta">`,
		`<input type="hidden" name="tab" value="events">`,
		`dansalMtabs(root`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("musician page missing %s", want)
		}
	}
}
