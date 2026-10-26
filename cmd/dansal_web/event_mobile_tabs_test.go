package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEventPagePhoneTabs covers #1617: the event page renders the six phone
// tabs, the panels they switch, and marks a tab grayed out when its content is
// missing. A page without a description, timetable, venue or organiser shows
// those tabs as empty.
func TestEventPagePhoneTabs(t *testing.T) {
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
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.event, tmplData(req, cfg, i18n, "Test", EventData{Event: Event{ID: 1, Title: "Test"}}))
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if strings.Contains(html, "template error") {
		t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
	}
	for _, want := range []string{
		`id="evt-tabs" data-mtab="general"`,
		`data-tab="general" aria-controls="evt-tabs" aria-selected="true"`,
		`data-tab="details" aria-controls="evt-tabs" aria-selected="false" tabindex="-1"><span class="mtab-ico" aria-hidden="true">📝</span><span class="mtab-label">`,
		`data-tab="board" aria-controls="evt-tabs"`,
		`class="mtab is-empty" data-tab="timetable"`,
		`class="mtab is-empty" data-tab="location"`,
		`class="mtab is-empty" data-tab="org"`,
		`class="mtab" data-tab="board"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("event page missing %s", want)
		}
	}
	// #1670: the notice sits on the General tab, the lightbox and the phone watchlist link exist
	for _, want := range []string{
		`id="evt-lightbox"`,
		`class="evt-lightbox-close"`,
		`class="evt-wl-link evt-phone-only evt-panel" data-tab="general"`,
		`<p class="evt-disclaimer evt-panel" data-tab="general">`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("event page missing %s", want)
		}
	}
	if strings.Contains(html, `class="evt-disclaimer evt-panel" data-tab="details"`) {
		t.Error("the notice must not stay on the Details tab")
	}
	if strings.Contains(html, `class="mtab is-empty" data-tab="board"`) {
		t.Error("the board tab is never grayed out")
	}
}
