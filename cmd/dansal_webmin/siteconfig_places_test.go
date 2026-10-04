package main

import (
	"bytes"
	"database/sql"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ademant/dansal/internal/places"
)

// #1429: saving Site config stores the normalized country list and starts a
// background place sync; the re-import button forces one.
func TestSiteConfigPlaceCountries(t *testing.T) {
	db := openWebDB(filepath.Join(t.TempDir(), "web.db"))
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)

	type call struct {
		countries string
		force     bool
	}
	var calls []call
	old := startPlaceSync
	startPlaceSync = func(_ *Config, _ *sql.DB, c []string, force bool) {
		calls = append(calls, call{strings.Join(c, ","), force})
	}
	defer func() { startPlaceSync = old }()
	cfg := &Config{}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("place_countries", " de, xx1 at;DE ")
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/site-config", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	siteConfigSaveHandler(cfg, db)(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	if got := getSiteSetting(db, "place_countries"); got != "DE,AT" {
		t.Errorf("place_countries = %q, want DE,AT", got)
	}
	if len(calls) != 1 || calls[0] != (call{"DE,AT", false}) {
		t.Fatalf("sync calls after save = %+v", calls)
	}

	rec = httptest.NewRecorder()
	siteConfigPlacesImportHandler(cfg, db)(rec, httptest.NewRequest(http.MethodPost, "/site-config/places/import", nil))
	if len(calls) != 2 || calls[1] != (call{"DE,AT", true}) {
		t.Fatalf("sync calls after re-import = %+v", calls)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "Re-importing") {
		t.Errorf("redirect = %q", loc)
	}
}

// The Site config page renders the city-search card with its import status.
func TestSiteConfigRendersPlaceStatus(t *testing.T) {
	tmpls := loadTemplates()
	req := httptest.NewRequest(http.MethodGet, "/site-config", nil)
	d := tmplData(req, &Config{}, "Site configuration", siteConfigData{
		PlaceCountries: "DE,AT",
		PlaceImports: []places.ImportStatus{
			{Country: "AT", Status: "error", Error: "GET …/AT.zip: 404 Not Found"},
			{Country: "DE", Status: "ok", PlaceCount: 80848, ImportedAt: time.Date(2026, 10, 4, 21, 9, 0, 0, time.UTC)},
		},
	})
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.siteConfig, d)
	body := rec.Body.String()
	for _, want := range []string{
		`name="place_countries" value="DE,AT"`,
		"<td>80848</td>",
		"2026-10-04 21:09",
		"404 Not Found",
		`form="places-import-form"`,
		`id="places-import-form"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("site config page misses %q", want)
		}
	}
}
