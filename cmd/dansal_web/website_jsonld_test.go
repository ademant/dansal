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

// TestSiteWideWebsiteJSONLDSameAs covers #1296: the site-wide WebSite
// JSON-LD block in base.html (present on every page) gains a "sameAs" array
// from webmin's same_as site setting, as a sibling of "audience" on the
// WebSite object itself — not nested inside "audience", and omitted
// entirely when nothing is configured (matching this file's own established
// "no empty array" convention for optional JSON-LD properties elsewhere).
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
}
