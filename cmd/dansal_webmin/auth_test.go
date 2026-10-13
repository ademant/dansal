package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractCN(t *testing.T) {
	cases := []struct {
		name string
		dn   string
		want string
	}{
		{"simple", "CN=alice,O=dansal", "alice"},
		{"cn not first", "O=dansal,CN=Bob Smith,OU=dev", "Bob Smith"},
		{"lowercase prefix", "o=dansal,cn=carol", "carol"},
		{"spaces around parts", "OU=dev,  CN=alice  ,O=dansal", "alice"},
		{"bare cn", "CN=alice", "alice"},
		{"first cn wins", "CN=first,CN=second", "first"},
		{"no cn", "O=dansal,OU=dev", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractCN(c.dn); got != c.want {
				t.Fatalf("extractCN(%q) = %q, want %q", c.dn, got, c.want)
			}
		})
	}
}

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8090", true},
		{"127.0.0.1", true},
		{"[::1]:8090", true},
		{"localhost:8090", false},
		{"192.168.1.5:8090", false},
		{"10.0.0.1:80", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopback(c.addr); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestRequestHostname(t *testing.T) {
	cases := []struct{ host, want string }{
		{"webmin.example.com", "webmin.example.com"},
		{"webmin.example.com:8090", "webmin.example.com"},
		{"localhost:18090", "localhost"},
		{"127.0.0.1:8090", "127.0.0.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Host = c.host
		if got := requestHostname(r); got != c.want {
			t.Errorf("requestHostname(Host=%q) = %q, want %q", c.host, got, c.want)
		}
	}
}

// TestCheckOrigin covers #1536: the same same-origin CSRF check dansal_web's
// checkAdminOrigin performs, ported here since webmin has no equivalent
// cfg.Domain to compare against — only the request's own Host header.
func TestCheckOrigin(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		origin  string
		referer string
		want    bool
	}{
		{"no headers: allowed", "webmin.example.com", "", "", true},
		{"matching Origin: allowed", "webmin.example.com", "https://webmin.example.com", "", true},
		{"matching Origin with port: allowed", "webmin.example.com:8090", "https://webmin.example.com", "", true},
		{"mismatched Origin: rejected", "webmin.example.com", "https://evil.test", "", false},
		{"Origin takes priority over mismatched Referer", "webmin.example.com", "https://webmin.example.com", "https://evil.test", true},
		{"no Origin, matching Referer: allowed", "webmin.example.com", "", "https://webmin.example.com/site-config", true},
		{"no Origin, mismatched Referer: rejected", "webmin.example.com", "", "https://evil.test/x", false},
		{"malformed Origin: rejected", "webmin.example.com", "://not a url", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/site-config", nil)
			r.Host = c.host
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.referer != "" {
				r.Header.Set("Referer", c.referer)
			}
			if got := checkOrigin(r); got != c.want {
				t.Errorf("checkOrigin() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestRequireLogin_BlocksCrossSiteWrite covers #1536 end-to-end: a POST
// (write) with a mismatched Origin must be rejected with 403 before the
// wrapped handler ever runs, regardless of session state. A GET with the
// same mismatched Origin must NOT be blocked — dansal_web's equivalent check
// only ever wraps write routes, and webmin's requireLogin also protects
// plain read pages, which must stay reachable.
func TestRequireLogin_BlocksCrossSiteWrite(t *testing.T) {
	cfg := &Config{}
	var called bool
	handler := requireLogin(cfg, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	t.Run("cross-site POST rejected, handler not called", func(t *testing.T) {
		called = false
		r := httptest.NewRequest(http.MethodPost, "/site-config", nil)
		r.Host = "webmin.example.com"
		r.Header.Set("Origin", "https://evil.test")
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
		if called {
			t.Error("handler must not run for a cross-site write")
		}
	})

	t.Run("cross-site GET not blocked by the origin check", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/site-config", nil)
		r.Host = "webmin.example.com"
		r.Header.Set("Origin", "https://evil.test")
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code == http.StatusForbidden {
			t.Error("a GET must not be rejected by the write-only origin check")
		}
	})
}

func TestSafeNextPath(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", "/"},
		{"/", "/"},
		{"/users", "/users"},
		{"/users/alice@example.com/sessions", "/users/alice@example.com/sessions"},
		{"/maintenance?x=1", "/maintenance?x=1"},
		{"//evil.example.com", "/"},
		{"/\\evil", "/"},
		{"https://evil.example.com", "/"},
		{"http://evil.example.com/p", "/"},
		{"javascript:alert(1)", "/"},
		{"/a\rb", "/"},
		{"/a\tb", "/"},
		{"/a\nb", "/"},
		{"/a\\b", "/"},
	}
	for _, c := range cases {
		if got := safeNextPath(c.raw); got != c.want {
			t.Errorf("safeNextPath(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}
