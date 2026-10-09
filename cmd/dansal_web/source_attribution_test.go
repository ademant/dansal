package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSmokeRenderEventSourceAttribution renders event.html with and without
// Event.SourceAttribution/SourceLicence/SourceTermsURL set (#1485, compliance
// G9 phase-67): the public import-credit block must be separate from the
// {{if $.User}} admin edit-link block (it must render for an anonymous
// request, i.e. no SessionUser on req), fall back to the org name when
// attribution is empty, and stay absent for a manually-created event.
func TestSmokeRenderEventSourceAttribution(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/events/1", nil) // anonymous: no SessionUser attached

	render := func(data EventData) string {
		rec := httptest.NewRecorder()
		td := tmplData(req, cfg, i18n, data.Event.Title, data)
		renderTemplate(rec, tmpls.event, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		if strings.Contains(string(body), "template error") {
			t.Fatalf("template execution error, body tail: %s", body[max(0, len(body)-500):])
		}
		return string(body)
	}

	t.Run("fetched event with attribution and terms_url", func(t *testing.T) {
		body := render(EventData{Event: Event{
			ID: 1, Title: "Imported Ball", StartTime: "2026-09-11T18:00:00",
			FetchSourceID: 7, SourceAttribution: "Example Verein", SourceLicence: "CC-BY-4.0", SourceTermsURL: "https://example.test/terms",
		}})
		if !strings.Contains(body, "Example Verein") {
			t.Error("expected the attribution text to render for an anonymous visitor")
		}
		if !strings.Contains(body, "CC-BY-4.0") {
			t.Error("expected the licence text to render")
		}
		if !strings.Contains(body, `href="https://example.test/terms"`) {
			t.Error("expected the terms_url link to render")
		}
	})

	t.Run("fetched event, no attribution, falls back to org", func(t *testing.T) {
		body := render(EventData{
			Event: Event{ID: 2, Title: "Imported Ball 2", StartTime: "2026-09-11T18:00:00", FetchSourceID: 7},
			Org:   &Organization{ID: 3, Name: "Fallback Org", Website: "https://fallback.test"},
		})
		if !strings.Contains(body, "Fallback Org") {
			t.Error("expected the org name as the attribution fallback")
		}
		if !strings.Contains(body, `href="https://fallback.test"`) {
			t.Error("expected the org website as the terms_url fallback")
		}
	})

	t.Run("manual event: no credit block at all", func(t *testing.T) {
		body := render(EventData{Event: Event{ID: 3, Title: "Manual Ball", StartTime: "2026-09-11T18:00:00"}})
		if strings.Contains(body, "Example Verein") || strings.Contains(body, "Fallback Org") {
			t.Error("did not expect any import credit for a manually-created event")
		}
	})
}
