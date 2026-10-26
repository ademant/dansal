package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFestivalsPhoneTabs covers #1674: the festivals page renders the Map,
// Calendar and Events phone tabs under the year selector, with Map first. A
// year with no festivals marks Calendar and Events as empty.
func TestFestivalsPhoneTabs(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/festivals?year=2026", nil)

	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.festivals, tmplData(req, cfg, i18n, "Festivals", FestivalsData{Year: 2026, PrevYear: 2025, NextYear: 2027}))
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if strings.Contains(html, "template error") {
		t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
	}
	for _, want := range []string{
		`class="fest-year-nav"`,
		`id="fest-tabs" data-mtab="map"`,
		`class="mtab is-empty" data-tab="map"`,
		`class="mtab is-empty" data-tab="calendar"`,
		`class="mtab is-empty" data-tab="events"`,
		`<div class="mtab-panel" data-tab="calendar">`,
		`<div class="mtab-panel" data-tab="events">`,
		`dansalMtabs(root`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("festivals page missing %s", want)
		}
	}
	if strings.Index(html, `class="fest-year-nav"`) > strings.Index(html, `id="fest-tabs"`) {
		t.Error("the year selector must sit above the tab bar")
	}
}
