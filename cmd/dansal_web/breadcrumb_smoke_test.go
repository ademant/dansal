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

var reJSONLDBlocks = regexp.MustCompile(`(?s)<script type="application/ld\+json">\s*(.*?)\s*</script>`)

// TestSmokeBreadcrumbJSONLD renders the event/location/org pages and checks
// that every application/ld+json block (including the new BreadcrumbList) is
// syntactically valid JSON, and that a BreadcrumbList block is present.
func TestSmokeBreadcrumbJSONLD(t *testing.T) {
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

	checkBlocks := func(t *testing.T, body string) {
		matches := reJSONLDBlocks.FindAllStringSubmatch(body, -1)
		if len(matches) == 0 {
			t.Fatalf("no ld+json blocks found")
		}
		sawBreadcrumb := false
		for _, m := range matches {
			var v map[string]any
			if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
				t.Fatalf("invalid JSON in ld+json block: %v\nblock: %s", err, m[1])
			}
			if v["@type"] == "BreadcrumbList" {
				sawBreadcrumb = true
				items, ok := v["itemListElement"].([]any)
				if !ok || len(items) < 2 {
					t.Fatalf("expected itemListElement with >=2 entries, got %v", v["itemListElement"])
				}
			}
		}
		if !sawBreadcrumb {
			t.Fatalf("no BreadcrumbList block found")
		}
	}

	t.Run("event page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/events/1", nil)
		rec := httptest.NewRecorder()
		orgSlug := "test-org"
		td := tmplData(req, cfg, i18n, "test", EventData{
			Event: Event{
				ID:        1,
				Title:     "Fest Noz",
				StartTime: "2026-08-01T20:00:00Z",
				EndTime:   "2026-08-02T01:00:00Z",
				Location:  &Location{ID: 5, Location: "Salle des Fêtes"},
			},
			Org:     &Organization{ID: 2, Name: "Test Org"},
			OrgSlug: orgSlug,
		})
		renderTemplate(rec, tmpls.event, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		checkBlocks(t, string(body))
	})

	t.Run("location page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/location/5", nil)
		rec := httptest.NewRecorder()
		td := tmplData(req, cfg, i18n, "test", LocationPageData{
			Location: Location{ID: 5, Location: "Salle des Fêtes"},
		})
		renderTemplate(rec, tmpls.location, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		checkBlocks(t, string(body))
	})

	t.Run("org page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/org/test-org", nil)
		rec := httptest.NewRecorder()
		td := tmplData(req, cfg, i18n, "test", OrgData{
			Org:  Organization{ID: 2, Name: "Test Org"},
			Slug: "test-org",
		})
		renderTemplate(rec, tmpls.org, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		checkBlocks(t, string(body))
	})
}
