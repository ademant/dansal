package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSuggestManagePageHasStepNav covers #1769: a manage link must render the
// step bar and progress arrows (the only way to reach steps 4 and 6 after
// #1666 removed Next/Back), while the create-only import/manual switch stays
// off the manage page.
func TestSuggestManagePageHasStepNav(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/events/suggest/manage/tok", nil)
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.suggestEvent, tmplData(req, cfg, i18n, "test", SuggestPageData{
		ManageToken: "tok",
	}))
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if strings.Contains(html, "template error") {
		t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
	}
	if !strings.Contains(html, `id="wiz-mtabs"`) {
		t.Error("manage page has no phone step bar")
	}
	if n := strings.Count(html, `role="tab" class="mtab" data-step="`); n != 6 {
		t.Errorf("manage page step bar has %d tabs, want 6", n)
	}
	if !strings.Contains(html, `id="wiz-arrows"`) {
		t.Error("manage page has no progress arrows")
	}
	if strings.Contains(html, `class="suggest-tabs"`) {
		t.Error("manage page must not show the create-only manual/import switch")
	}
}
