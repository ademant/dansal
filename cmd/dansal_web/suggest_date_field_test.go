package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSuggestDateFieldHonorsDateFormat covers #1411: the suggest wizard's
// free-text date field (#sg-date-text) must show a placeholder matching the
// site's configured date notation (webmin's date_format) and emit valid JS
// for the new parse/validate logic, in both configurations.
func TestSuggestDateFieldHonorsDateFormat(t *testing.T) {
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

	render := func(t *testing.T) string {
		t.Helper()
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.suggestEvent, tmplData(req, cfg, i18n, "test", SuggestPageData{}))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body tail=%s", rec.Code, body[max(0, len(body)-500):])
		}
		checkInlineJS(t, string(body))
		return string(body)
	}

	t.Run("default (locale-based): ISO placeholder", func(t *testing.T) {
		body := render(t)
		if !strings.Contains(body, `placeholder="YYYY-MM-DD"`) {
			t.Error("expected ISO placeholder when date_format is unset")
		}
		if !strings.Contains(body, `var SG_DATE_FORMAT = 'iso';`) {
			t.Error("expected SG_DATE_FORMAT = 'iso'")
		}
	})

	t.Run("de: DD.MM.YYYY placeholder", func(t *testing.T) {
		setSiteSetting(db, "date_format", "de")
		siteCfg = newSiteSettingsCache(db)
		body := render(t)
		if !strings.Contains(body, `placeholder="DD.MM.YYYY"`) {
			t.Error("expected DD.MM.YYYY placeholder when date_format is \"de\"")
		}
		if !strings.Contains(body, `var SG_DATE_FORMAT = 'de';`) {
			t.Error("expected SG_DATE_FORMAT = 'de'")
		}
	})
}
