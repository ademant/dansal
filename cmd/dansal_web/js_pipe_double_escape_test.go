package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1408: a redundant {{. | js}} pipe inside any <script> block double-escapes,
// since html/template's own contextual JS escaper already runs regardless —
// #1406 fixed this for JSON-LD blocks; this covers the remaining
// non-JSON-LD occurrences (bare JS expressions and developer-quoted string
// literals). A syntax-only check (node --check, as checkInlineJS does)
// can't catch this: doubled backslashes are still syntactically valid JS,
// just the wrong runtime value once parsed.
func TestNoDoubleEscapedJSPipe(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	const special = `L'Auberge & <Fils>`

	t.Run("admin_timetable.html: _topLocName (bare JS expression context)", func(t *testing.T) {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.adminTimetable, tmplData(req, cfg, i18n, "test", TimetablePageData{
			Event:           Event{ID: 1, Title: "Test", StartTime: "2026-09-15T18:00:00Z", EndTime: "2026-09-16T02:00:00Z"},
			TopLocationName: special,
		}))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body tail=%s", rec.Code, body[max(0, len(body)-500):])
		}
		checkInlineJS(t, string(body))
		want := `var _topLocName = "L'Auberge \u0026 \u003cFils\u003e";`
		if !strings.Contains(string(body), want) {
			t.Errorf("expected single-escaped %q in body, not found (double-escaping regression?)", want)
		}
		if strings.Contains(string(body), `\\`) {
			t.Errorf("body contains a doubled backslash — double-escaping regression")
		}
	})

	t.Run("embed_timetable.html: TT_NEXTUP_* (developer-quoted string context)", func(t *testing.T) {
		strs := I18nStrings{"tt_nextup_now": special}
		rec := httptest.NewRecorder()
		renderEmbed(rec, tmpls.embedTimetable, map[string]any{
			"Lang":    "en",
			"Nonce":   "test-nonce",
			"Event":   Event{ID: 1, Title: "Test", StartTime: "2026-09-15T18:00:00Z", EndTime: "2026-09-16T02:00:00Z", IsPublished: true, Timetable: []TimetableEntry{{StartTime: "18:00", EndTime: "19:00", Title: "Bal", EntryType: "bal"}}},
			"Strings": strs,
			"BaseURL": "https://" + cfg.Domain,
		})
		body, _ := io.ReadAll(rec.Body)
		checkInlineJS(t, string(body))
		want := `var TT_NEXTUP_NOW = 'L\u0027Auberge \u0026 \u003cFils\u003e';`
		if !strings.Contains(string(body), want) {
			t.Errorf("expected single-escaped %q in body, not found (double-escaping regression?)", want)
		}
		if strings.Contains(string(body), `\\`) {
			t.Errorf("body contains a doubled backslash — double-escaping regression")
		}
	})

	t.Run("admin_location_edit.html: safeGoBack redirect (developer-quoted string context)", func(t *testing.T) {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.adminLocationEdit, tmplData(req, cfg, i18n, "test", AdminLocationEditData{
			From: special,
		}))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body tail=%s", rec.Code, body[max(0, len(body)-500):])
		}
		checkInlineJS(t, string(body))
		want := `location.href='L\u0027Auberge \u0026 \u003cFils\u003e';`
		if !strings.Contains(string(body), want) {
			t.Errorf("expected single-escaped %q in body, not found (double-escaping regression?)", want)
		}
		if strings.Contains(string(body), `\\`) {
			t.Errorf("body contains a doubled backslash — double-escaping regression")
		}
	})
}
