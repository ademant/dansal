package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// TestSuggestHelpButtons covers #1418: every ⓘ button on the suggest form
// points at an existing, translated help text, and the page still has valid
// inline JS.
func TestSuggestHelpButtons(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	req := httptest.NewRequest(http.MethodGet, "/events/suggest", nil)
	rec := httptest.NewRecorder()
	data := SuggestPageData{HintSMTP: true, CanUploadImageNow: true, Dances: []Dance{{ID: 1, Name: "Bourrée"}}}
	renderTemplate(rec, loadTemplates().suggestEvent, tmplData(req, &Config{Domain: "example.test"}, loadI18n(""), "test", data))
	body := rec.Body.String()
	checkInlineJS(t, body)

	if strings.Contains(body, "sg_help_") {
		t.Error("untranslated sg_help_* key in suggest page")
	}
	btns := regexp.MustCompile(`class="help-btn"[^>]*aria-controls="(sg-help-[a-z_]+)"`).FindAllStringSubmatch(body, -1)
	if len(btns) < 24 {
		t.Errorf("expected ≥24 help buttons, got %d", len(btns))
	}
	for _, m := range btns {
		if !strings.Contains(body, `id="`+m[1]+`" class="field-help" hidden>`) {
			t.Errorf("help button %s has no matching hidden help text", m[1])
		}
	}

	// The manage-link page renders the same form with a different image block.
	rec = httptest.NewRecorder()
	renderTemplate(rec, loadTemplates().suggestEvent, tmplData(req, &Config{Domain: "example.test"}, loadI18n(""), "test", SuggestPageData{ManageToken: "tok"}))
	if !strings.Contains(rec.Body.String(), `aria-controls="sg-help-image"`) {
		t.Error("manage page missing image help button")
	}
}
