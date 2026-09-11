package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

var reWebsiteJSONLD = regexp.MustCompile(`(?s)<script type="application/ld\+json">(\{"@context".*?"@type":"WebSite".*?)</script>`)

// TestSiteWideWebsiteJSONLDSameAs covers #1296 (sameAs from webmin's
// same_as site setting, as a sibling of "audience" — not nested inside it —
// omitted entirely when unconfigured) and #1299 (description reusing the
// site-wide meta_description i18n key) for the site-wide WebSite JSON-LD
// block in base.html, present on every page.
func TestSiteWideWebsiteJSONLDSameAs(t *testing.T) {
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

	render := func(t *testing.T) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.index, tmplData(req, cfg, i18n, "Events", IndexData{}))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		m := reWebsiteJSONLD.FindStringSubmatch(string(body))
		if m == nil {
			t.Fatalf("WebSite JSON-LD block not found")
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
			t.Fatalf("invalid JSON in WebSite block: %v\nblock: %s", err, m[1])
		}
		return v
	}

	t.Run("no same_as configured: sameAs omitted entirely", func(t *testing.T) {
		v := render(t)
		if _, ok := v["sameAs"]; ok {
			t.Errorf("sameAs should be absent when nothing is configured, got %v", v["sameAs"])
		}
	})

	t.Run("same_as configured: sameAs on the WebSite object, not nested in audience", func(t *testing.T) {
		setSiteSetting(db, "same_as", "https://github.com/example\nhttps://mas.to/@example\n\n")
		siteCfg = newSiteSettingsCache(db)
		v := render(t)
		sa, ok := v["sameAs"].([]any)
		if !ok || len(sa) != 2 {
			t.Fatalf("expected a 2-element sameAs array, got %v", v["sameAs"])
		}
		if sa[0] != "https://github.com/example" || sa[1] != "https://mas.to/@example" {
			t.Errorf("sameAs = %v, want [https://github.com/example https://mas.to/@example]", sa)
		}
		aud, ok := v["audience"].(map[string]any)
		if !ok {
			t.Fatalf("expected an audience object, got %v", v["audience"])
		}
		if _, ok := aud["sameAs"]; ok {
			t.Errorf("sameAs must be a sibling of audience on the WebSite object, not nested inside it")
		}
	})

	// #1299: description reuses the existing site-wide meta_description i18n
	// key — not the homepage-only, webmin-editable home_intro text — since
	// the WebSite node describes the site itself and must read the same
	// regardless of which page it happens to render on.
	t.Run("description is the site-wide meta_description, present unconditionally", func(t *testing.T) {
		v := render(t)
		desc, ok := v["description"].(string)
		if !ok || desc == "" {
			t.Fatalf("expected a non-empty description, got %v", v["description"])
		}
		if desc != i18n.Strings("de").T("meta_description") {
			t.Errorf("description = %q, want the meta_description i18n string %q", desc, i18n.Strings("de").T("meta_description"))
		}
	})

	// Debug aid: "version" (AppVersion, git describe's output — includes the
	// short commit hash) is on the WebSite JSON-LD, deliberately not the
	// meta description, so it never shows up in a search result snippet.
	t.Run("version is present (debug aid, deliberately not in meta description)", func(t *testing.T) {
		v := render(t)
		if v["version"] != Version {
			t.Errorf("version = %v, want %q", v["version"], Version)
		}
	})
}
