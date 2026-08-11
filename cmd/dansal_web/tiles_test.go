package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// withTestTileUpstream registers a temporary scheme in tileUpstreams pointing
// at ts (a local httptest.Server) and removes it on cleanup, so tests never
// hit the real internet.
func withTestTileUpstream(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	scheme := "test"
	tileUpstreams[scheme] = ts.URL + "/%d/%d/%d.png"
	t.Cleanup(func() { delete(tileUpstreams, scheme) })
	return scheme
}

// TestTileProxyFetchesAndCaches asserts a cache-miss request fetches from
// the upstream, serves the bytes, and writes them to disk; a second request
// for the same tile is served from disk without hitting upstream again (#1079).
func TestTileProxyFetchesAndCaches(t *testing.T) {
	upstreamHits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		if ua := r.Header.Get("User-Agent"); ua != tileUserAgent {
			t.Errorf("upstream request User-Agent = %q, want %q", ua, tileUserAgent)
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("fake-png-bytes"))
	}))
	defer ts.Close()
	scheme := withTestTileUpstream(t, ts)

	cfg := &Config{TileCacheDir: t.TempDir()}
	h := tileProxyHandler(cfg)

	req := httptest.NewRequest("GET", fmt.Sprintf("/tiles/%s/5/10/20.png", scheme), nil)
	req.SetPathValue("scheme", scheme)
	req.SetPathValue("z", "5")
	req.SetPathValue("x", "10")
	req.SetPathValue("yfile", "20.png")
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if w.Body.String() != "fake-png-bytes" {
		t.Fatalf("body = %q, want %q", w.Body.String(), "fake-png-bytes")
	}
	if upstreamHits != 1 {
		t.Fatalf("upstreamHits = %d, want 1", upstreamHits)
	}
	cachePath := filepath.Join(cfg.TileCacheDir, scheme, "5", "10", "20.png")
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("expected cache file at %s: %v", cachePath, err)
	}

	// Second request: same tile, must be served from disk (no new upstream hit).
	req2 := httptest.NewRequest("GET", fmt.Sprintf("/tiles/%s/5/10/20.png", scheme), nil)
	req2.SetPathValue("scheme", scheme)
	req2.SetPathValue("z", "5")
	req2.SetPathValue("x", "10")
	req2.SetPathValue("yfile", "20.png")
	w2 := httptest.NewRecorder()
	h(w2, req2)
	if w2.Code != http.StatusOK || w2.Body.String() != "fake-png-bytes" {
		t.Fatalf("cached response mismatch: status=%d body=%q", w2.Code, w2.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstreamHits after cached request = %d, want still 1", upstreamHits)
	}
}

// TestTileProxyRejectsUnknownScheme asserts a scheme outside tileUpstreams 404s.
func TestTileProxyRejectsUnknownScheme(t *testing.T) {
	cfg := &Config{TileCacheDir: t.TempDir()}
	h := tileProxyHandler(cfg)
	req := httptest.NewRequest("GET", "/tiles/evil/5/10/20.png", nil)
	req.SetPathValue("scheme", "evil")
	req.SetPathValue("z", "5")
	req.SetPathValue("x", "10")
	req.SetPathValue("yfile", "20.png")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// TestTileProxyRejectsOutOfRangeCoordinates asserts x/y beyond 2^z at the
// given zoom is rejected instead of being forwarded upstream.
func TestTileProxyRejectsOutOfRangeCoordinates(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("upstream should not be hit for out-of-range coordinates, got %s", r.URL)
	}))
	defer ts.Close()
	scheme := withTestTileUpstream(t, ts)

	cfg := &Config{TileCacheDir: t.TempDir()}
	h := tileProxyHandler(cfg)
	// z=2 → max valid coordinate is 3 (2^2-1); 99 is out of range.
	req := httptest.NewRequest("GET", fmt.Sprintf("/tiles/%s/2/99/1.png", scheme), nil)
	req.SetPathValue("scheme", scheme)
	req.SetPathValue("z", "2")
	req.SetPathValue("x", "99")
	req.SetPathValue("yfile", "1.png")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// TestTileProxyEmptyCacheDirDefaultsToDBPathSubdir asserts an empty
// TileCacheDir falls back to a "tiles" subdir next to DBPath (not ImagesDir
// — see the TileCacheDir field comment for why: under systemd's
// ProtectSystem=strict + StateDirectory=dansal-web/%i hardening, only
// /var/lib/dansal-web/<instance> — where DBPath lives — is writable;
// ImagesDir commonly defaults to the non-instance-namespaced, read-only
// /var/lib/dansal-web).
func TestTileProxyEmptyCacheDirDefaultsToDBPathSubdir(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("x"))
	}))
	defer ts.Close()
	scheme := withTestTileUpstream(t, ts)

	stateDir := t.TempDir()
	cfg := &Config{
		DBPath:    filepath.Join(stateDir, "web.db"),
		ImagesDir: "/var/lib/dansal-web", // deliberately NOT writable in this test to prove it's unused
	}
	h := tileProxyHandler(cfg)
	req := httptest.NewRequest("GET", fmt.Sprintf("/tiles/%s/1/0/0.png", scheme), nil)
	req.SetPathValue("scheme", scheme)
	req.SetPathValue("z", "1")
	req.SetPathValue("x", "0")
	req.SetPathValue("yfile", "0.png")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "tiles", scheme, "1", "0", "0.png")); err != nil {
		t.Fatalf("expected cache file under <dir of DBPath>/tiles: %v", err)
	}
}
