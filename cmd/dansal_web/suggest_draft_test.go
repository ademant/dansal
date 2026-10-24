package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSuggestDraftBannerOnlyOnPlainSuggestion covers #1649: the restore banner
// and its script are on the plain new-suggestion page, and not on the pages
// that carry their own prefill (the "suggest another" return, an error
// re-render, the import and manage pages).
func TestSuggestDraftBannerOnlyOnPlainSuggestion(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/events/suggest", nil)

	render := func(data SuggestPageData) string {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.suggestEvent, tmplData(req, cfg, i18n, "test", data))
		body, _ := io.ReadAll(rec.Body)
		html := string(body)
		if strings.Contains(html, "template error") {
			t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
		}
		return html
	}

	plain := render(SuggestPageData{})
	for _, want := range []string{
		`id="suggest-draft-banner"`,
		`data-fn="suggestDraftRestore"`,
		`data-fn="suggestDraftDiscard"`,
		`id="suggest-draft-found"`,
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain suggestion page missing %s", want)
		}
	}

	next := render(SuggestPageData{IsNextSuggestion: true})
	if strings.Contains(next, `id="suggest-draft-banner"`) {
		t.Error("the suggest-another page has its own prefill and must not offer a draft")
	}
}
