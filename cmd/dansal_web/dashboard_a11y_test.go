package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1420: dashboard WCAG fixes — the preset select has an accessible name and
// the duplicate hint uses the theme's danger tokens (≥ 4.5:1, dark mode).
func TestDashboardA11y(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	req := withSessionUser(httptest.NewRequest(http.MethodGet, "/dashboard", nil), &SessionUser{ID: 1, Role: "admin"})
	rec := httptest.NewRecorder()
	renderTemplate(rec, loadTemplates().dashboard, tmplData(req, &Config{Domain: "example.test"}, loadI18n(""), "test", DashboardData{
		UnpinnedTemplates: []EventTemplate{{ID: 3, Name: "Bal hebdo"}},
	}))
	body := rec.Body.String()
	if !strings.Contains(body, `id="dash-preset-select" aria-label="`) || strings.Contains(body, "dashboard_preset_select_label") {
		t.Error("preset select has no translated aria-label")
	}
	if strings.Contains(body, "color:#c0392b") || !strings.Contains(body, ".dash-hint-danger{background:var(--badge-danger-bg") {
		t.Error("duplicate hint should use the --badge-danger-* tokens")
	}
}
