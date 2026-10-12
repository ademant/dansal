package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLazyFormTokens verifies that rendering the event page and board page
// does not consume any form tokens from the cap (#1486): bots and crawlers
// that page-view these high-traffic URLs should not fill oneTimeTokens.
func TestLazyFormTokens(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	before := outstandingTokens.Load()

	tmpls := loadTemplates()
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test"}

	t.Run("event page issues no tokens", func(t *testing.T) {
		data := EventData{
			Event:    Event{ID: 42, Title: "Test Ball", StartTime: "2026-10-15T20:00:00Z", Tags: []string{"bal-folk"}},
			GeoToken: newFormToken(),
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/events/42", nil)
		renderTemplate(rec, tmpls.event, tmplData(req, cfg, i18n, "Test Ball", data))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
		// _form_token inputs must render with empty value="" (no server-side issue)
		parts := strings.Split(string(body), `name="_form_token" value="`)
		for _, part := range parts[1:] { // parts[0] is content before first match
			if !strings.HasPrefix(part, `"`) {
				end := strings.IndexByte(part, '"')
				if end < 0 {
					end = len(part)
				}
				t.Errorf("non-empty _form_token in event page render: value=%q", part[:end])
			}
		}
	})

	t.Run("board page issues no tokens", func(t *testing.T) {
		data := BoardData{
			Posts: []ContactPost{
				{ID: 1, EventID: 42, Nickname: "Alice", Message: "test",
					Type: "ride_offer", CreatedAt: "2026-10-15T20:00:00Z"},
			},
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/events/42/board", nil)
		renderTemplate(rec, tmpls.board, tmplData(req, cfg, i18n, "Board", data))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
		parts := strings.Split(string(body), `name="_form_token" value="`)
		for _, part := range parts[1:] {
			if !strings.HasPrefix(part, `"`) {
				end := strings.IndexByte(part, '"')
				if end < 0 {
					end = len(part)
				}
				t.Errorf("non-empty _form_token in board page render: value=%q", part[:end])
			}
		}
	})

	after := outstandingTokens.Load()
	if after != before {
		t.Errorf("outstandingTokens changed: before=%d after=%d — render issued %d token(s)",
			before, after, after-before)
	}
}
