package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1589: the PWA endpoints. The service worker carries the build version in
// its cache name, the manifest makes the site installable with PNG icons, the
// CSP lets the same-origin worker script run, and the offline page renders.

func TestServiceWorkerCarriesVersion(t *testing.T) {
	rec := httptest.NewRecorder()
	swHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sw.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	if strings.Contains(body, "__VERSION__") {
		t.Error("version placeholder was not replaced")
	}
	if !strings.Contains(body, `var VERSION = "`+Version+`";`) {
		t.Errorf("worker does not carry version %q", Version)
	}
	if !strings.Contains(body, `"/offline"`) {
		t.Error("offline page is not precached")
	}
}

func TestManifestIsInstallable(t *testing.T) {
	rec := httptest.NewRecorder()
	manifestHandler(&Config{SiteName: "dansal"}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manifest.json", nil))
	var m struct {
		Scope           string   `json:"scope"`
		Display         string   `json:"display"`
		DisplayOverride []string `json:"display_override"`
		Icons           []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
		} `json:"icons"`
		Shortcuts []struct {
			URL string `json:"url"`
		} `json:"shortcuts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if m.Scope != "/" || m.Display != "standalone" || len(m.DisplayOverride) == 0 {
		t.Errorf("scope=%q display=%q override=%v", m.Scope, m.Display, m.DisplayOverride)
	}
	sizes := map[string]string{}
	for _, ic := range m.Icons {
		sizes[ic.Sizes] = ic.Src
	}
	if sizes["192x192"] != "/icon-192.png" || sizes["512x512"] != "/icon-512.png" {
		t.Errorf("PNG icons missing: %v", sizes)
	}
	if len(m.Shortcuts) != 3 {
		t.Errorf("shortcuts = %d, want 3", len(m.Shortcuts))
	}
}

func TestBaselineCSPAllowsWorkerScript(t *testing.T) {
	if !strings.Contains(baselineCSP("abc"), "worker-src 'self'") {
		t.Error("CSP lacks worker-src 'self': the service worker would be blocked")
	}
}

func TestOfflinePageRenders(t *testing.T) {
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

	rec := httptest.NewRecorder()
	offlineHandler(cfg, tmpls, i18n).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/offline", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if strings.Contains(html, "template error") {
		t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
	}
	for _, key := range []string{"pwa_offline_title", "pwa_offline_text", "pwa_offline_banner", "pwa_install_app"} {
		if strings.Contains(html, key) {
			t.Errorf("missing translation rendered as key %q", key)
		}
	}
}
