package main

import (
	"database/sql"
	"encoding/json"
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

// TestWellKnownExtras covers the static well-known endpoints from #1836.
func TestWellKnownExtras(t *testing.T) {
	cfg := &Config{Domain: "example.test"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /security.txt", redirectHandler("/.well-known/security.txt", http.StatusMovedPermanently))
	mux.HandleFunc("GET /.well-known/change-password", redirectHandler("/settings", http.StatusFound))
	mux.HandleFunc("GET /.well-known/passkey-endpoints", passkeyEndpointsHandler(cfg))
	mux.HandleFunc("GET /.well-known/traffic-advice", trafficAdviceHandler())
	mux.HandleFunc("GET /{name}", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	for _, tc := range []struct {
		path, loc string
		code      int
	}{
		{"/security.txt", "/.well-known/security.txt", http.StatusMovedPermanently},
		{"/.well-known/change-password", "/settings", http.StatusFound},
	} {
		w := get(tc.path)
		if w.Code != tc.code || w.Header().Get("Location") != tc.loc {
			t.Errorf("%s: got %d Location=%q, want %d %q", tc.path, w.Code, w.Header().Get("Location"), tc.code, tc.loc)
		}
	}

	w := get("/.well-known/passkey-endpoints")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("passkey-endpoints: got %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	var pk map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &pk); err != nil {
		t.Fatalf("passkey-endpoints: invalid JSON: %v", err)
	}
	if pk["enroll"] != "https://example.test/settings" || pk["manage"] != "https://example.test/settings" {
		t.Errorf("passkey-endpoints: got %v", pk)
	}

	w = get("/.well-known/traffic-advice")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/trafficadvice+json" {
		t.Fatalf("traffic-advice: got %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	var ta []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &ta); err != nil || len(ta) != 1 || ta[0]["user_agent"] != "prefetch-proxy" || ta[0]["fraction"] != 1.0 {
		t.Errorf("traffic-advice: got %s (err %v)", w.Body.String(), err)
	}
}
