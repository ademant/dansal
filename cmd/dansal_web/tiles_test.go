package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// setupTileAuthTest wires up a real siteCfg backed by an in-memory web.db
// (#1269) and returns the instance's public tile token, so tests exercising
// tile-serving logic (cache/upstream/coordinate validation — not auth
// itself) can append it as every real caller now must. t.Cleanup restores
// the previous package-level siteCfg so this doesn't leak into other test
// files sharing the same test binary.
func setupTileAuthTest(t *testing.T) string {
	t.Helper()
	old := siteCfg
	t.Cleanup(func() { siteCfg = old })
	db := initDB(":memory:")
	t.Cleanup(func() { db.Close() })
	siteCfg = newSiteSettingsCache(db)
	return getOrCreateTileToken(db)
}

// TestTileProxyFetchesAndCaches asserts a cache-miss request fetches from
// the upstream, serves the bytes, and writes them to disk; a second request
// for the same tile is served from disk without hitting upstream again (#1079).
func TestTileProxyFetchesAndCaches(t *testing.T) {
	token := setupTileAuthTest(t)
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
	h := tileProxyHandler(cfg, nil)

	req := httptest.NewRequest("GET", fmt.Sprintf("/tiles/%s/5/10/20.png?t=%s", scheme, token), nil)
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
	req2 := httptest.NewRequest("GET", fmt.Sprintf("/tiles/%s/5/10/20.png?t=%s", scheme, token), nil)
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

// TestTileProxyRefetchesStaleCache asserts a cached tile older than
// tileCacheMaxAge (#1169) is treated as a miss and re-fetched from upstream,
// refreshing the cache file's mtime.
func TestTileProxyRefetchesStaleCache(t *testing.T) {
	token := setupTileAuthTest(t)
	upstreamHits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("fresh-png-bytes"))
	}))
	defer ts.Close()
	scheme := withTestTileUpstream(t, ts)

	cfg := &Config{TileCacheDir: t.TempDir()}
	h := tileProxyHandler(cfg, nil)

	cachePath := filepath.Join(cfg.TileCacheDir, scheme, "5", "10", "20.png")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("stale-png-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	staleTime := time.Now().Add(-tileCacheMaxAge - time.Hour)
	if err := os.Chtimes(cachePath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", fmt.Sprintf("/tiles/%s/5/10/20.png?t=%s", scheme, token), nil)
	req.SetPathValue("scheme", scheme)
	req.SetPathValue("z", "5")
	req.SetPathValue("x", "10")
	req.SetPathValue("yfile", "20.png")
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK || w.Body.String() != "fresh-png-bytes" {
		t.Fatalf("status=%d body=%q, want 200 fresh-png-bytes", w.Code, w.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstreamHits = %d, want 1 (stale cache should trigger a re-fetch)", upstreamHits)
	}
	fi, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(fi.ModTime()) > time.Minute {
		t.Fatalf("cache file mtime not refreshed: %v", fi.ModTime())
	}
}

// TestTileProxyRejectsUnknownScheme asserts a scheme outside tileUpstreams 404s.
func TestTileProxyRejectsUnknownScheme(t *testing.T) {
	token := setupTileAuthTest(t)
	cfg := &Config{TileCacheDir: t.TempDir()}
	h := tileProxyHandler(cfg, nil)
	req := httptest.NewRequest("GET", "/tiles/evil/5/10/20.png?t="+token, nil)
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
	token := setupTileAuthTest(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("upstream should not be hit for out-of-range coordinates, got %s", r.URL)
	}))
	defer ts.Close()
	scheme := withTestTileUpstream(t, ts)

	cfg := &Config{TileCacheDir: t.TempDir()}
	h := tileProxyHandler(cfg, nil)
	// z=2 → max valid coordinate is 3 (2^2-1); 99 is out of range.
	req := httptest.NewRequest("GET", fmt.Sprintf("/tiles/%s/2/99/1.png?t=%s", scheme, token), nil)
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
	token := setupTileAuthTest(t)
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
	h := tileProxyHandler(cfg, nil)
	req := httptest.NewRequest("GET", fmt.Sprintf("/tiles/%s/1/0/0.png?t=%s", scheme, token), nil)
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

// tileTestRequest builds a minimal tile request with the given auth (empty
// token/bearer means "send neither").
func tileTestRequest(scheme, token, bearer string) *http.Request {
	url := fmt.Sprintf("/tiles/%s/5/10/20.png", scheme)
	if token != "" {
		url += "?t=" + token
	}
	req := httptest.NewRequest("GET", url, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.SetPathValue("scheme", scheme)
	req.SetPathValue("z", "5")
	req.SetPathValue("x", "10")
	req.SetPathValue("yfile", "20.png")
	return req
}

// TestTileProxyRejectsNoAuth is #1269's actual point: previously-open tile
// requests with neither the public token nor a bearer key must now be
// rejected before ever reaching the upstream.
func TestTileProxyRejectsNoAuth(t *testing.T) {
	setupTileAuthTest(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be hit for an unauthenticated request")
	}))
	defer ts.Close()
	scheme := withTestTileUpstream(t, ts)

	cfg := &Config{TileCacheDir: t.TempDir()}
	h := tileProxyHandler(cfg, nil)
	w := httptest.NewRecorder()
	h(w, tileTestRequest(scheme, "", ""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// TestTileProxyRejectsWrongToken asserts a token that doesn't match the
// instance's own is rejected the same as no token at all — not merely
// "any non-empty value accepted".
func TestTileProxyRejectsWrongToken(t *testing.T) {
	setupTileAuthTest(t)
	cfg := &Config{TileCacheDir: t.TempDir()}
	h := tileProxyHandler(cfg, nil)
	w := httptest.NewRecorder()
	h(w, tileTestRequest("osm", "not-the-real-token", ""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// TestTileProxyBearerAPIKey covers the "wp-dansal" path (#1269): a caller
// presenting a real dansal API key via Authorization: Bearer is authorized
// even without the public token, validated (and cached) against dansal's
// own GET /api/v1/apikeys.
func TestTileProxyBearerAPIKey(t *testing.T) {
	setupTileAuthTest(t)
	apikeysHits := 0
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apikeysHits++
		if r.Header.Get("Authorization") != "Bearer good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer apiSrv.Close()
	client := &DansalClient{BaseURL: apiSrv.URL, HTTP: http.DefaultClient}

	tileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tile-bytes"))
	}))
	defer tileSrv.Close()
	scheme := withTestTileUpstream(t, tileSrv)

	cfg := &Config{TileCacheDir: t.TempDir()}
	h := tileProxyHandler(cfg, client)

	w := httptest.NewRecorder()
	h(w, tileTestRequest(scheme, "", "good-key"))
	if w.Code != http.StatusOK {
		t.Fatalf("valid bearer key: status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	h(w2, tileTestRequest(scheme, "", "wrong-key"))
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer key: status = %d, want 401", w2.Code)
	}

	if apikeysHits != 2 {
		t.Fatalf("expected 2 validation calls to the API server (one per distinct key), got %d", apikeysHits)
	}

	// A second request with the same already-validated key must be served
	// from the cache — no additional call to the API server.
	w3 := httptest.NewRecorder()
	h(w3, tileTestRequest(scheme, "", "good-key"))
	if w3.Code != http.StatusOK {
		t.Fatalf("repeat valid bearer key: status = %d, want 200", w3.Code)
	}
	if apikeysHits != 2 {
		t.Fatalf("expected the second identical key to be served from cache (still 2 calls), got %d", apikeysHits)
	}
}
