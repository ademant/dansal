package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// newReverseCacheHarness wires nominatimGeocodeReverseHandler to a fresh web DB
// and a Nominatim stub that always answers status/body. The returned counter
// is how many times the stub was actually reached (#1549).
func newReverseCacheHarness(t *testing.T, status int, body string) (http.HandlerFunc, *atomic.Int32) {
	t.Helper()
	oldBase := nominatimBaseURL
	t.Cleanup(func() { nominatimBaseURL = oldBase })

	calls := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)
	nominatimBaseURL = upstream.URL

	geocodeThrottle = newSubmissionThrottle(100, time.Minute)
	db := initDB(filepath.Join(t.TempDir(), "web.db"))
	return nominatimGeocodeReverseHandler(&Config{Domain: "example.test"}, db), calls
}

func reverseRequest(t *testing.T, handler http.HandlerFunc, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/search/geocode/reverse?"+query, nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestReverseGeocodeKey(t *testing.T) {
	cases := map[string]string{
		"48.123449": "48.1234",
		"48.123451": "48.1235",
		"-1.6":      "-1.6000",
		"-0.00001":  "0.0000",
		"0":         "0.0000",
	}
	for in, want := range cases {
		if got := reverseGeocodeKey(in); got != want {
			t.Errorf("reverseGeocodeKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// Coordinates that round to the same 4-decimal key share one upstream call.
func TestNominatimGeocodeReverseHandlerServesRepeatFromCache(t *testing.T) {
	handler, calls := newReverseCacheHarness(t, http.StatusOK, `{"display_name":"Testville"}`)

	first := reverseRequest(t, handler, "lat=48.12341&lon=-1.60001")
	second := reverseRequest(t, handler, "lat=48.12344&lon=-1.60004")

	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1 (second lookup should hit the cache)", calls.Load())
	}
	if first.Body.String() != second.Body.String() {
		t.Errorf("cached body %q differs from upstream body %q", second.Body.String(), first.Body.String())
	}
}

// accept-language changes display_name, so a different lang is a different key.
func TestNominatimGeocodeReverseHandlerCacheIsKeyedByLang(t *testing.T) {
	handler, calls := newReverseCacheHarness(t, http.StatusOK, `{"display_name":"Testville"}`)

	reverseRequest(t, handler, "lat=48.1&lon=-1.6")
	reverseRequest(t, handler, "lat=48.1&lon=-1.6&lang=de")
	reverseRequest(t, handler, "lat=48.1&lon=-1.6&lang=de")

	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2 (one per distinct lang, then cached)", calls.Load())
	}
}

// Errors and empty responses must not be cached: the next request still goes upstream.
func TestNominatimGeocodeReverseHandlerDoesNotCacheFailures(t *testing.T) {
	handler, calls := newReverseCacheHarness(t, http.StatusInternalServerError, `oops`)

	if rec := reverseRequest(t, handler, "lat=48.1&lon=-1.6"); rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 on upstream failure", rec.Code)
	}
	reverseRequest(t, handler, "lat=48.1&lon=-1.6")

	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2 (failures must not be cached)", calls.Load())
	}
}

// The TTL is measured from fetched_at: a row older than maxAge is treated as a miss.
func TestReverseGeocodeCacheTTL(t *testing.T) {
	db := initDB(filepath.Join(t.TempDir(), "web.db"))
	if err := setReverseGeocodeCache(db, "48.1000", "-1.6000", "", []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}

	if _, ok := getReverseGeocodeCache(db, "48.1000", "-1.6000", "", geocodeReverseCacheTTL); !ok {
		t.Fatal("fresh row should be a cache hit")
	}
	if _, ok := getReverseGeocodeCache(db, "48.1000", "-1.6000", "de", geocodeReverseCacheTTL); ok {
		t.Error("a different lang must not hit the cache")
	}

	old := time.Now().Add(-(geocodeReverseCacheTTL + time.Hour)).Unix()
	if _, err := db.Exec("UPDATE geocode_reverse_cache SET fetched_at = ?", old); err != nil {
		t.Fatal(err)
	}
	if _, ok := getReverseGeocodeCache(db, "48.1000", "-1.6000", "", geocodeReverseCacheTTL); ok {
		t.Error("row older than the TTL should be a miss")
	}
}
