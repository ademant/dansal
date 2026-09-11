package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSmokeRenderAdminDances covers #1290's new inline description edit form
// added to the previously add+delete-only /admin/dances page.
func TestSmokeRenderAdminDances(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/admin/dances", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.adminDances, tmplData(req, cfg, i18n, "test", AdminDancesData{
		Dances: []Dance{
			{ID: 1, Name: "An Dro", Description: "A Breton chain dance in 8-count phrases."},
			{ID: 2, Name: "Scottish"},
		},
	}))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}
	if strings.Contains(string(body), "template error") {
		t.Fatalf("template execution error, body: %s", body)
	}
	if !strings.Contains(string(body), `action="/admin/dances/1/edit"`) {
		t.Fatalf("expected a per-row edit form, body: %s", body)
	}
	if !strings.Contains(string(body), "A Breton chain dance in 8-count phrases.") {
		t.Fatalf("expected the existing description in the textarea, body: %s", body)
	}
	checkInlineJS(t, string(body))
}
