package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	setSiteSetting(db, "privacy_en", "# Existing privacy")
	setSiteSetting(db, "terms_de", "# Existing terms")
	setSiteSetting(db, "impressum_uk", "# Existing legal notice")

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
	if got := getSiteSetting(db, "privacy_en"); got != "# Existing privacy" {
		t.Errorf("privacy_en = %q, global site settings save should not change legal text", got)
	}
	if got := getSiteSetting(db, "terms_de"); got != "# Existing terms" {
		t.Errorf("terms_de = %q, global site settings save should not change legal text", got)
	}
	if got := getSiteSetting(db, "impressum_uk"); got != "# Existing legal notice" {
		t.Errorf("impressum_uk = %q, global site settings save should not change legal text", got)
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

func TestSiteConfigRendersLegalPageEditors(t *testing.T) {
	tmpls := loadTemplates()
	req := httptest.NewRequest(http.MethodGet, "/site-config", nil)
	d := tmplData(req, &Config{}, "Site configuration", siteConfigData{
		LegalPageLangs: []siteConfigLegalLanguage{{Code: "en", Name: "English"}, {Code: "uk", Name: "Українська"}},
		LegalLang:      "en",
		PrivacyText:    "# Privacy notice",
		TermsText:      "# Terms",
	})
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.siteConfig, d)
	body := rec.Body.String()
	for _, want := range []string{
		`name="page" value="privacy"`,
		`<option value="en" selected>English</option>`,
		`name="text"`,
		`# Privacy notice</textarea>`,
		`name="page" value="terms"`,
		`# Terms</textarea>`,
		`legal-text/export?page=privacy`,
		`legal-text/import?page=terms`,
		`name="file"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("site config page misses %q", want)
		}
	}
}

func TestSiteConfigLegalTextExportHandler(t *testing.T) {
	db := openWebDB(filepath.Join(t.TempDir(), "web.db"))
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	setSiteSetting(db, "privacy_en", "# Privacy")

	req := httptest.NewRequest(http.MethodGet, "/site-config/legal-text/export?page=privacy", nil)
	rec := httptest.NewRecorder()
	siteConfigLegalTextExportHandler(db)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="privacy.json"`) {
		t.Errorf("Content-Disposition = %q", got)
	}
	var got legalTextJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Format != legalTextJSONFormat || got.Version != 1 || got.Document != "privacy" {
		t.Errorf("export metadata = %+v", got)
	}
	if len(got.Languages) != len(legalPageLangs) || got.Languages["en"] != "# Privacy" || got.Languages["uk"] != "" {
		t.Errorf("export languages = %+v", got.Languages)
	}

	rec = httptest.NewRecorder()
	siteConfigLegalTextExportHandler(db)(rec, httptest.NewRequest(http.MethodGet, "/site-config/legal-text/export?page=unknown", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid document status = %d, want 400", rec.Code)
	}
}

func TestSiteConfigLegalTextImportHandler(t *testing.T) {
	db := openWebDB(filepath.Join(t.TempDir(), "web.db"))
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	setSiteSetting(db, "privacy_en", "Keep English")
	setSiteSetting(db, "privacy_de", "Clear German")

	importJSON := func(page, raw string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		if err := mw.WriteField("legal_lang", "uk"); err != nil {
			t.Fatal(err)
		}
		part, err := mw.CreateFormFile("file", "translations.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		if err := mw.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/site-config/legal-text/import?page="+page, &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		siteConfigLegalTextImportHandler(db)(rec, req)
		return rec
	}

	payload := legalTextJSON{
		Format:   legalTextJSONFormat,
		Version:  1,
		Document: "privacy",
		Languages: map[string]string{
			"en": "Updated English",
			"de": "",
			"uk": "Українська",
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	rec := importJSON("privacy", string(data))
	if rec.Code != http.StatusSeeOther || getSiteSetting(db, "privacy_en") != "Updated English" ||
		getSiteSetting(db, "privacy_de") != "" || getSiteSetting(db, "privacy_uk") != "Українська" {
		t.Fatalf("import status=%d en=%q de=%q uk=%q", rec.Code,
			getSiteSetting(db, "privacy_en"), getSiteSetting(db, "privacy_de"), getSiteSetting(db, "privacy_uk"))
	}
	if got := rec.Header().Get("Location"); !strings.Contains(got, "legal_lang=uk") {
		t.Errorf("redirect = %q, want selected language preserved", got)
	}

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"wrong document", `{"format":"dansal-legal-text","version":1,"document":"terms","languages":{"en":"wrong"}}`},
		{"unsupported language", `{"format":"dansal-legal-text","version":1,"document":"privacy","languages":{"xx":"wrong"}}`},
		{"unknown field", `{"format":"dansal-legal-text","version":1,"document":"privacy","languages":{"en":"wrong"},"extra":true}`},
		{"malformed JSON", `{"format":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := importJSON("privacy", tc.raw)
			if rec.Code != http.StatusSeeOther || getSiteSetting(db, "privacy_en") != "Updated English" {
				t.Errorf("invalid import status=%d privacy_en=%q", rec.Code, getSiteSetting(db, "privacy_en"))
			}
			if got := rec.Header().Get("Location"); !strings.Contains(got, "Error") {
				t.Errorf("invalid import redirect = %q, want error flash", got)
			}
		})
	}
}

func TestSiteConfigLegalTextSaveHandler(t *testing.T) {
	db := openWebDB(filepath.Join(t.TempDir(), "web.db"))
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	save := func(page, lang, text string) *httptest.ResponseRecorder {
		form := url.Values{"page": {page}, "lang": {lang}, "text": {text}}
		req := httptest.NewRequest(http.MethodPost, "/site-config/legal-text", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		siteConfigLegalTextSaveHandler(db)(rec, req)
		return rec
	}

	rec := save("privacy", "uk", "  # Конфіденційність  ")
	if rec.Code != http.StatusSeeOther || getSiteSetting(db, "privacy_uk") != "# Конфіденційність" {
		t.Fatalf("save response=%d value=%q", rec.Code, getSiteSetting(db, "privacy_uk"))
	}
	if got := rec.Header().Get("Location"); !strings.Contains(got, "legal_lang=uk") {
		t.Errorf("redirect = %q, want selected language preserved", got)
	}

	for _, tc := range []struct{ page, lang string }{
		{"unknown", "en"},
		{"terms", "xx"},
	} {
		rec = save(tc.page, tc.lang, "# Must not save")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("invalid page/lang (%q,%q) status = %d, want 400", tc.page, tc.lang, rec.Code)
		}
	}
}
