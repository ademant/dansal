package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOrgPhoneTabs covers #1672: the org page renders the six phone tabs, and
// a tab with no content is marked empty. An org with no events, musicians,
// series or event locations shows those tabs as empty.
func TestOrgPhoneTabs(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/org/test-org", nil)

	rec := httptest.NewRecorder()
	data := OrgData{Org: Organization{ID: 2, Name: "Test Org"}, Slug: "test-org"}
	renderTemplate(rec, tmpls.org, tmplData(req, cfg, i18n, "test", data))
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if strings.Contains(html, "template error") {
		t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
	}
	for _, want := range []string{
		`id="org-tabs" data-mtab="info"`,
		`data-tab="info" aria-controls="org-tabs" aria-selected="true"`,
		`class="mtab is-empty" data-tab="calendar"`,
		`class="mtab is-empty" data-tab="events"`,
		`class="mtab is-empty" data-tab="musicians"`,
		`class="mtab is-empty" data-tab="series"`,
		`class="mtab is-empty" data-tab="map"`,
		`<div class="mtab-panel" data-tab="musicians">`,
		`<div class="mtab-panel" data-tab="series">`,
		`dansalMtabs(root`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("org page missing %s", want)
		}
	}
}

// TestOrgCalendarOpensOnFirstEventMonth covers #1697: the org calendar is the
// shared month calendar, opening on the first month with an event, and its tab
// is grey when the org has no upcoming event.
func TestOrgCalendarOpensOnFirstEventMonth(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/org/test-org", nil)

	render := func(data OrgData) string {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.org, tmplData(req, cfg, i18n, "test", data))
		body, _ := io.ReadAll(rec.Body)
		html := string(body)
		if strings.Contains(html, "template error") {
			t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
		}
		return html
	}

	withEvents := render(OrgData{
		Org:            Organization{ID: 2, Name: "Test Org"},
		Slug:           "test-org",
		UpcomingEvents: []Event{{ID: 1, Title: "Ball", StartTime: "2033-05-18T03:33:20Z"}},
	})
	if !strings.Contains(withEvents, `startIndex: dansalFirstEventMonthIndex(calEvents, months)`) {
		t.Error("org calendar must open on the first month with an event")
	}
	if strings.Contains(withEvents, `class="mtab is-empty" data-tab="calendar"`) {
		t.Error("org with upcoming events must not grey out its calendar tab")
	}

	noEvents := render(OrgData{Org: Organization{ID: 2, Name: "Test Org"}, Slug: "test-org"})
	if !strings.Contains(noEvents, `class="mtab is-empty" data-tab="calendar"`) {
		t.Error("org without upcoming events must grey out its calendar tab")
	}
}
