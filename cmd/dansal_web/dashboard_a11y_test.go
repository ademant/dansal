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

// #1881 / #1883: the dashboard drops the quick-link row, puts a dansal-doc link
// at the right of each section, and gives each organisation "+ Location".
// The links appear only when doc_url is configured, with no trailing slash.
func TestDashboardDocLinks(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	render := func(docURL string) string {
		req := withSessionUser(httptest.NewRequest(http.MethodGet, "/dashboard", nil), &SessionUser{ID: 1, Role: "admin"})
		rec := httptest.NewRecorder()
		renderTemplate(rec, loadTemplates().dashboard, tmplData(req, &Config{Domain: "example.test", DocURL: docURL}, loadI18n(""), "test", DashboardData{
			UserOrgs: []Organization{{ID: 5, Name: "Verein", ActorName: "verein"}},
		}))
		return rec.Body.String()
	}

	with := render("https://doc.example.test/")
	if got := strings.Count(with, `href="https://doc.example.test/Benutzer-Veranstaltungen"`); got < 3 {
		t.Errorf("doc links in the three sections: got %d, want at least 3", got)
	}
	if strings.Contains(with, `class="dash-quicklinks"`) || strings.Contains(with, `href="/admin/instructors" class="btn-sm"`) {
		t.Error("the quick-link row should be gone")
	}
	if !strings.Contains(with, `href="/admin/locations/new?org_id=5"`) {
		t.Error("organisation row lacks the + Location link")
	}

	without := render("")
	if strings.Contains(without, "https://doc.example.test") || strings.Contains(without, `class="dash-sec-doc"`) {
		t.Error("no doc link should be shown when doc_url is empty")
	}
}
