package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// realPNGFixture returns a tiny, real, decodable PNG (unlike the other
// tests' placeholder "fake-png-bytes" strings), so AVIF conversion actually
// has something to decode.
func realPNGFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 30), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture png: %v", err)
	}
	return buf.Bytes()
}

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

// TestTileTokenHandler covers #1287: GET /tiles/token is public (no auth
// required, unlike the tile proxy itself) and simply hands back the same
// token an external integration would otherwise have to get from an admin
// reading site_settings directly.
func TestTileTokenHandler(t *testing.T) {
	token := setupTileAuthTest(t)

	req := httptest.NewRequest(http.MethodGet, "/tiles/token", nil)
	w := httptest.NewRecorder()
	tileTokenHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("could not decode response body %q: %v", w.Body.String(), err)
	}
	if body.Token != token {
		t.Fatalf("token = %q, want the instance's actual tile token %q", body.Token, token)
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

// TestTileProxyServesAVIFToAVIFCapableClient covers #1327: a request whose
// Accept header advertises image/avif gets the AVIF-encoded tile (smaller,
// correct Content-Type), and both representations end up cached on disk so
// either kind of client is served from cache next time without re-hitting
// upstream.
func TestTileProxyServesAVIFToAVIFCapableClient(t *testing.T) {
	token := setupTileAuthTest(t)
	pngFixture := realPNGFixture(t)
	upstreamHits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngFixture)
	}))
	defer ts.Close()
	scheme := withTestTileUpstream(t, ts)

	cfg := &Config{TileCacheDir: t.TempDir()}
	h := tileProxyHandler(cfg, nil)

	req := httptest.NewRequest("GET", fmt.Sprintf("/tiles/%s/5/10/20.png?t=%s", scheme, token), nil)
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,*/*;q=0.8")
	req.SetPathValue("scheme", scheme)
	req.SetPathValue("z", "5")
	req.SetPathValue("x", "10")
	req.SetPathValue("yfile", "20.png")
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body len=%d)", w.Code, w.Body.Len())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/avif" {
		t.Errorf("Content-Type = %q, want image/avif", ct)
	}
	if w.Body.Len() == 0 || bytes.Equal(w.Body.Bytes(), pngFixture) {
		t.Errorf("body looks like it wasn't AVIF-encoded (len=%d)", w.Body.Len())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstreamHits = %d, want 1", upstreamHits)
	}

	pngPath := filepath.Join(cfg.TileCacheDir, scheme, "5", "10", "20.png")
	avifPath := filepath.Join(cfg.TileCacheDir, scheme, "5", "10", "20.avif")
	if _, err := os.Stat(pngPath); err != nil {
		t.Errorf("expected PNG also cached at %s: %v", pngPath, err)
	}
	if _, err := os.Stat(avifPath); err != nil {
		t.Errorf("expected AVIF cached at %s: %v", avifPath, err)
	}

	// A client that does NOT advertise AVIF support must still get the PNG,
	// served from the cache written above (no new upstream hit).
	req2 := httptest.NewRequest("GET", fmt.Sprintf("/tiles/%s/5/10/20.png?t=%s", scheme, token), nil)
	req2.Header.Set("Accept", "image/png,image/*,*/*;q=0.8")
	req2.SetPathValue("scheme", scheme)
	req2.SetPathValue("z", "5")
	req2.SetPathValue("x", "10")
	req2.SetPathValue("yfile", "20.png")
	w2 := httptest.NewRecorder()
	h(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("non-AVIF client: status = %d, want 200", w2.Code)
	}
	if ct := w2.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("non-AVIF client: Content-Type = %q, want image/png", ct)
	}
	if !bytes.Equal(w2.Body.Bytes(), pngFixture) {
		t.Errorf("non-AVIF client: body does not match original PNG fixture")
	}
	if upstreamHits != 1 {
		t.Fatalf("upstreamHits after cached non-AVIF request = %d, want still 1", upstreamHits)
	}

	// A repeat AVIF-capable request must also be served from cache.
	req3 := httptest.NewRequest("GET", fmt.Sprintf("/tiles/%s/5/10/20.png?t=%s", scheme, token), nil)
	req3.Header.Set("Accept", "image/avif,*/*;q=0.8")
	req3.SetPathValue("scheme", scheme)
	req3.SetPathValue("z", "5")
	req3.SetPathValue("x", "10")
	req3.SetPathValue("yfile", "20.png")
	w3 := httptest.NewRecorder()
	h(w3, req3)
	if w3.Code != http.StatusOK || w3.Header().Get("Content-Type") != "image/avif" {
		t.Fatalf("repeat AVIF request: status=%d contentType=%q", w3.Code, w3.Header().Get("Content-Type"))
	}
	if upstreamHits != 1 {
		t.Fatalf("upstreamHits after cached AVIF request = %d, want still 1", upstreamHits)
	}
}

// TestBackfillTileAVIF covers the `dansal-web --backfill-tile-avif` one-shot
// mode (#1327): a pre-existing cache of PNG-only tiles gets a .avif sibling
// for each one, purely locally (no upstream involved at all -- unlike the
// rest of this file's tests, this one doesn't even spin up an httptest
// server), is idempotent on a second run, and leaves an already-decodable
// non-PNG file alone (counted as failed, not silently skipped or corrupting
// anything).
func TestBackfillTileAVIF(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "osm", "5", "10")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "20.png"), realPNGFixture(t), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "21.png"), realPNGFixture(t), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "22.png"), []byte("not a real png"), 0644); err != nil {
		t.Fatal(err)
	}

	converted, skipped, failed := backfillTileAVIF(dir)
	if converted != 2 || skipped != 0 || failed != 1 {
		t.Fatalf("first run: converted=%d skipped=%d failed=%d, want 2/0/1", converted, skipped, failed)
	}
	if _, err := os.Stat(filepath.Join(sub, "20.avif")); err != nil {
		t.Errorf("expected 20.avif: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sub, "21.avif")); err != nil {
		t.Errorf("expected 21.avif: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sub, "22.avif")); err == nil {
		t.Errorf("22.avif should not exist (source wasn't a decodable image)")
	}

	// Re-running must skip everything already converted (idempotent) and
	// still report the same failure for the undecodable file, not retry it
	// silently forever.
	converted2, skipped2, failed2 := backfillTileAVIF(dir)
	if converted2 != 0 || skipped2 != 2 || failed2 != 1 {
		t.Fatalf("second run: converted=%d skipped=%d failed=%d, want 0/2/1", converted2, skipped2, failed2)
	}
}
