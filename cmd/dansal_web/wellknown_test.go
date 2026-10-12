package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSecurityTxtHandler verifies the security.txt handler priority, 404
// fallback, stable Expires, and Policy emission (#1477).
func TestSecurityTxtHandler(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)

	setSetting := func(k, v string) {
		db.Exec(`INSERT OR REPLACE INTO site_settings(key,value) VALUES(?,?)`, k, v)
	}

	cfg := &Config{
		Domain:          "example.test",
		SecurityContact: "mailto:fallback@example.test",
		SecurityPolicy:  "https://example.test/policy-yaml",
	}

	t.Run("404 when no contact in DB or cfg", func(t *testing.T) {
		siteCfg = newSiteSettingsCache(db)
		handler := securityTxtHandler(&Config{})
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/.well-known/security.txt", nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("got %d, want 404", w.Code)
		}
	})

	t.Run("web.yaml fallback when DB empty", func(t *testing.T) {
		siteCfg = newSiteSettingsCache(db)
		handler := securityTxtHandler(cfg)
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/.well-known/security.txt", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "Contact: mailto:fallback@example.test") {
			t.Errorf("body missing fallback contact: %q", body)
		}
		if !strings.Contains(body, "Policy: https://example.test/policy-yaml") {
			t.Errorf("body missing fallback policy: %q", body)
		}
	})

	t.Run("DB takes precedence over web.yaml", func(t *testing.T) {
		setSetting("security_contact", "mailto:db@example.test")
		setSetting("security_policy", "https://example.test/policy-db")
		setSetting("security_expires", "2027-06-01T00:00:00Z")
		siteCfg = newSiteSettingsCache(db)
		handler := securityTxtHandler(cfg)
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/.well-known/security.txt", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "Contact: mailto:db@example.test") {
			t.Errorf("DB contact not used: %q", body)
		}
		if !strings.Contains(body, "Policy: https://example.test/policy-db") {
			t.Errorf("DB policy not used: %q", body)
		}
		if !strings.Contains(body, "Expires: 2027-06-01T00:00:00Z") {
			t.Errorf("stable Expires not used: %q", body)
		}
	})

	t.Run("404 when DB contact empty even with web.yaml policy set", func(t *testing.T) {
		db.Exec(`DELETE FROM site_settings`)
		siteCfg = newSiteSettingsCache(db)
		handler := securityTxtHandler(&Config{SecurityPolicy: "https://example.test/policy-only"})
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/.well-known/security.txt", nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("got %d, want 404 (no contact)", w.Code)
		}
	})
}
