package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSanitizeNominatimLang covers the allowlist guarding what ends up in
// the outbound accept-language query param sent to Nominatim.
func TestSanitizeNominatimLang(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"de", "de"},
		{"en", "en"},
		{"ca,fr,it", "ca,fr,it"},
		{"de-DE", "de-DE"},
		{"", ""},
		{"de; DROP TABLE", ""},
		{"de\r\nX-Injected: 1", ""},
	}
	for _, c := range cases {
		if got := sanitizeNominatimLang(c.in); got != c.want {
			t.Errorf("sanitizeNominatimLang(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestClampGeocodeLimit covers the result-count clamp shared by the
// Nominatim and MusicBrainz search proxies.
func TestClampGeocodeLimit(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "5"},
		{"garbage", "5"},
		{"0", "5"},
		{"-1", "5"},
		{"1", "1"},
		{"8", "8"},
		{"10", "10"},
		{"999", "10"},
	}
	for _, c := range cases {
		if got := clampGeocodeLimit(c.in); got != c.want {
			t.Errorf("clampGeocodeLimit(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNominatimGeocodeSearchHandler covers #1313: the proxy forwards the
// query, sets a compliant User-Agent (impossible for the browser's own
// fetch() to do), and passes Nominatim's response straight through, with no
// login required (the public board-post/suggest-event forms need this).
func TestNominatimGeocodeSearchHandler(t *testing.T) {
	oldBase := nominatimBaseURL
	t.Cleanup(func() { nominatimBaseURL = oldBase })

	var gotUA, gotPath, gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"display_name":"Testville","lat":"1.0","lon":"2.0"}]`))
	}))
	defer upstream.Close()
	nominatimBaseURL = upstream.URL

	geocodeThrottle = newSubmissionThrottle(100, time.Minute)
	cfg := &Config{Domain: "example.test"}
	handler := nominatimGeocodeSearchHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/search/geocode/search?q=Testville&limit=3&lang=de", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if gotUA == "" || gotUA == "Go-http-client/1.1" {
		t.Errorf("User-Agent not set to a compliant value, got %q", gotUA)
	}
	if gotPath != "/search" {
		t.Errorf("upstream path = %q, want /search", gotPath)
	}
	if !strings.Contains(gotQuery, "q=Testville") || !strings.Contains(gotQuery, "limit=3") || !strings.Contains(gotQuery, "accept-language=de") || !strings.Contains(gotQuery, "addressdetails=1") {
		t.Errorf("upstream query = %q, missing expected params", gotQuery)
	}
	if !strings.Contains(rec.Body.String(), "Testville") {
		t.Errorf("response body not passed through: %s", rec.Body.String())
	}
}

// TestNominatimGeocodeSearchHandlerRequiresQuery covers the missing-q case.
func TestNominatimGeocodeSearchHandlerRequiresQuery(t *testing.T) {
	geocodeThrottle = newSubmissionThrottle(100, time.Minute)
	cfg := &Config{Domain: "example.test"}
	handler := nominatimGeocodeSearchHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/search/geocode/search", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestNominatimGeocodeReverseHandler covers the reverse-geocode proxy: it
// requires a login (every current caller is an admin-only page, unlike the
// public search proxy) and, once authenticated, proxies through correctly.
func TestNominatimGeocodeReverseHandler(t *testing.T) {
	oldBase := nominatimBaseURL
	t.Cleanup(func() { nominatimBaseURL = oldBase })

	var gotPath, gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"display_name":"Testville","address":{"city":"Testville"}}`))
	}))
	defer upstream.Close()
	nominatimBaseURL = upstream.URL

	geocodeThrottle = newSubmissionThrottle(100, time.Minute)
	cfg := &Config{Domain: "example.test"}
	handler := nominatimGeocodeReverseHandler(cfg)

	t.Run("without login: rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/search/geocode/reverse?lat=48.1&lon=-1.6", nil)
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("expected non-200 without a session, got 200")
		}
	})

	t.Run("with login: proxies through", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/search/geocode/reverse?lat=48.1&lon=-1.6", nil)
		req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
		rec := httptest.NewRecorder()
		handler(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if gotPath != "/reverse" {
			t.Errorf("upstream path = %q, want /reverse", gotPath)
		}
		if !strings.Contains(gotQuery, "lat=48.1") || !strings.Contains(gotQuery, "lon=-1.6") {
			t.Errorf("upstream query = %q, missing lat/lon", gotQuery)
		}
		if !strings.Contains(rec.Body.String(), "Testville") {
			t.Errorf("response body not passed through: %s", rec.Body.String())
		}
	})
}

func TestNominatimGeocodeReverseHandlerRequiresCoords(t *testing.T) {
	geocodeThrottle = newSubmissionThrottle(100, time.Minute)
	cfg := &Config{Domain: "example.test"}
	handler := nominatimGeocodeReverseHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/search/geocode/reverse?lat=48.1", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
