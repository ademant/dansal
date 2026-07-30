package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSmokeHreflangTags verifies that <link rel="alternate" hreflang> tags are
// emitted only on pages that opt in via TemplateData.Hreflang (navigational
// pages whose content is entirely i18n.yaml-driven), and never on pages like
// the event page where the actual content is organizer-entered and not
// translated.
func TestSmokeHreflangTags(t *testing.T) {
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

	t.Run("Hreflang=true emits alternate tags", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/organizations", nil)
		rec := httptest.NewRecorder()
		td := tmplData(req, cfg, i18n, "test", OrgsListData{})
		td.Hreflang = true
		renderTemplate(rec, tmpls.orgs, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		out := string(body)
		if !strings.Contains(out, `hreflang="de"`) {
			t.Fatalf("expected hreflang=\"de\" tag, got none. tail: %s", out[max(0, len(out)-500):])
		}
		if !strings.Contains(out, `hreflang="x-default"`) {
			t.Fatalf("expected hreflang=\"x-default\" tag, got none")
		}
		if !strings.Contains(out, `?lang=de`) {
			t.Fatalf("expected ?lang= param in alternate href")
		}
	})

	t.Run("Hreflang=false omits alternate tags", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/organizations", nil)
		rec := httptest.NewRecorder()
		td := tmplData(req, cfg, i18n, "test", OrgsListData{})
		renderTemplate(rec, tmpls.orgs, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		if strings.Contains(string(body), "hreflang=") {
			t.Fatalf("did not expect hreflang tags when Hreflang=false")
		}
	})
}
