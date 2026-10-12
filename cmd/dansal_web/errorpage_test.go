package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHTMLErrorContentNegotiation covers #1512: htmlError/forbidden must
// render the styled HTML error page for a normal browser navigation
// (Accept: text/html) and fall back to a plain/JSON error body otherwise,
// always with the right status code either way.
func TestHTMLErrorContentNegotiation(t *testing.T) {
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
	setErrorPageDeps(cfg, tmpls, i18n)

	t.Run("browser navigation gets the styled HTML page", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/admin/whatever?lang=en", nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		rec := httptest.NewRecorder()

		htmlError(rec, req, http.StatusForbidden)

		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q, want text/html prefix", ct)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Forbidden") {
			t.Errorf("body missing translated 403 title, got: %s", body)
		}
		if !strings.Contains(body, `href="/"`) {
			t.Errorf("body missing back-home link, got: %s", body)
		}
	})

	t.Run("API/fetch caller gets a plain-text fallback, not HTML", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/admin/whatever", nil)
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()

		htmlError(rec, req, http.StatusNotFound)

		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
		if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q, want non-HTML", ct)
		}
	})

	t.Run("forbidden() negotiates the same way", func(t *testing.T) {
		htmlReq := httptest.NewRequest("GET", "/admin/x", nil)
		htmlReq.Header.Set("Accept", "text/html")
		htmlRec := httptest.NewRecorder()
		forbidden(htmlRec, htmlReq)
		if htmlRec.Code != http.StatusForbidden {
			t.Errorf("html case: status = %d, want 403", htmlRec.Code)
		}
		if ct := htmlRec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("html case: Content-Type = %q, want text/html prefix", ct)
		}

		jsonReq := httptest.NewRequest("GET", "/admin/x", nil)
		jsonReq.Header.Set("Accept", "application/json")
		jsonRec := httptest.NewRecorder()
		forbidden(jsonRec, jsonReq)
		if jsonRec.Code != http.StatusForbidden {
			t.Errorf("json case: status = %d, want 403", jsonRec.Code)
		}
		if ct := jsonRec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("json case: Content-Type = %q, want application/json", ct)
		}
		if !strings.Contains(jsonRec.Body.String(), `"error"`) {
			t.Errorf("json case: body missing error field, got: %s", jsonRec.Body.String())
		}
	})
}
