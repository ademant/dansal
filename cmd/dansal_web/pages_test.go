package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegalPageHandlerUsesWebminContentAndFileFallback(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	setSiteSetting(db, "privacy_en", "# Webmin privacy\n\nConfigured in webmin.")
	setSiteSetting(db, "privacy_fr", "# Confidentialité")
	oldSiteCfg := siteCfg
	siteCfg = newSiteSettingsCache(db)
	t.Cleanup(func() { siteCfg = oldSiteCfg })

	legalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(legalDir, "privacy.md"), []byte("# File privacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legalDir, "terms.md"), []byte("# File terms"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{LegalDir: legalDir}
	tmpls := loadTemplates()
	i18n := loadI18n("")
	handler := legalPageHandler(cfg, tmpls, i18n, "privacy", "nav_privacy")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/privacy?lang=en", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Webmin privacy") || strings.Contains(rec.Body.String(), "File privacy") {
		t.Fatalf("webmin page response = %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `href="/privacy"`) || !strings.Contains(rec.Body.String(), `href="/terms"`) {
		t.Error("footer should link to each configured legal page")
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/privacy", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Webmin privacy") {
		t.Fatalf("privacy default-language response = %d %q", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/privacy", nil)
	req.Header.Set("Accept-Language", "fr-CA,fr;q=0.9,en;q=0.8")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Confidentialité") {
		t.Fatalf("Accept-Language response = %d %q", rec.Code, rec.Body.String())
	}

	handler = legalPageHandler(cfg, tmpls, i18n, "terms", "nav_terms")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/terms?lang=en", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "File terms") {
		t.Fatalf("file fallback response = %d %q", rec.Code, rec.Body.String())
	}
}

func TestLegalMarkdownTextHTMLSanitizesContent(t *testing.T) {
	got := string(LegalMarkdownTextHTML("privacy", "# Notice\n\n[unsafe](javascript:alert(1))"))
	if !strings.Contains(got, "<h1>Notice</h1>") {
		t.Errorf("markdown heading was not rendered: %q", got)
	}
	if strings.Contains(got, "javascript:") {
		t.Errorf("dangerous link scheme survived sanitization: %q", got)
	}
}

// TestLegalMarkdownTextHTMLRendersGFMTables covers #1464: a GFM pipe table
// (the shape the privacy policy's data-category/cookie/transfer tables use,
// per #1438) must render as a real <table>, not survive as raw "| a | b |"
// text inside a <p> — which is what the bare (non-GFM) goldmark.Convert()
// used before this fix produced.
func TestLegalMarkdownTextHTMLRendersGFMTables(t *testing.T) {
	got := string(LegalMarkdownTextHTML("privacy", "| Category | Purpose |\n|---|---|\n| Account data | Login |\n"))
	for _, want := range []string{"<table>", "<th>Category</th>", "<td>Account data</td>"} {
		if !strings.Contains(got, want) {
			t.Errorf("GFM table did not render, missing %q in: %q", want, got)
		}
	}
	if strings.Contains(got, "|---|") {
		t.Errorf("raw pipe-table syntax leaked through unrendered: %q", got)
	}
}

// TestImpressumPrivacyTermsShowTheirOwnHeading covers #1464: impressum.html
// is shared by all three legal pages (legalPageHandler renders /privacy and
// /terms through it, impressumHandler renders /impressum) — each must show
// its own page's heading, not a hardcoded "Impressum" on all three.
func TestImpressumPrivacyTermsShowTheirOwnHeading(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	setSiteSetting(db, "impressum_en", "Legal notice body.")
	setSiteSetting(db, "privacy_en", "Privacy body.")
	setSiteSetting(db, "terms_en", "Terms body.")
	oldSiteCfg := siteCfg
	siteCfg = newSiteSettingsCache(db)
	t.Cleanup(func() { siteCfg = oldSiteCfg })

	cfg := &Config{}
	tmpls := loadTemplates()
	i18n := loadI18n("")

	render := func(h http.HandlerFunc, path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept-Language", "en")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	cases := []struct {
		path    string
		handler http.HandlerFunc
		wantH1  string
	}{
		{"/impressum", impressumHandler(cfg, tmpls, i18n), "<h1>Legal notice</h1>"},
		{"/privacy", legalPageHandler(cfg, tmpls, i18n, "privacy", "nav_privacy"), "<h1>Privacy policy</h1>"},
		{"/terms", legalPageHandler(cfg, tmpls, i18n, "terms", "nav_terms"), "<h1>Terms of use</h1>"},
	}
	for _, tc := range cases {
		body := render(tc.handler, tc.path)
		if !strings.Contains(body, tc.wantH1) {
			t.Errorf("%s: missing %q in rendered page", tc.path, tc.wantH1)
		}
	}
}
