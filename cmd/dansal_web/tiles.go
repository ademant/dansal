package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/avif"
)

// tileUpstreams maps the client-supplied scheme segment to a printf template
// for the real upstream tile server. Upstream URL templates in Leaflet
// traditionally rotate across {a,b,c} subdomains to let browsers open more
// parallel connections; since dansal_web itself does the fetching now (the
// browser only ever talks to dansal_web), a single static subdomain is fine.
//
// #1169: there used to be a second "dark" entry pointing at CARTO's
// dark_all basemap for dark-mode maps. CARTO now requires a registered API
// key for that basemap and serves an "API key required" overlay without
// one, so dark mode was switched to darken these same OSM tiles with a CSS
// filter instead (base.html's .leaflet-tile-pane rule) — no second
// upstream needed any more.
var tileUpstreams = map[string]string{
	"osm": "https://a.tile.openstreetmap.org/%d/%d/%d.png",
}

// tileUserAgent identifies dansal_web to upstream tile servers, as required
// by OSM's tile usage policy (https://operations.osmfoundation.org/policies/tiles/).
const tileUserAgent = "dansal-web-tile-proxy/1.0 (+https://github.com/ademant/dansal)"

// tileHTTPClient is a package-level var so tests can leave it untouched
// (only tileUpstreams needs swapping to point at a test server).
var tileHTTPClient = &http.Client{Timeout: 10 * time.Second}

// tileCacheMaxBytes caps a single upstream tile response — real tiles are a
// few KB to ~100KB; this just guards against a misbehaving upstream.
const tileCacheMaxBytes = 2 << 20 // 2MB

// tileCacheMaxAge is how long a cached tile is served before being treated
// as a cache miss and re-fetched (#1169). Map imagery changes rarely, so a
// monthly refresh is a reasonable balance between staying current and
// minimizing repeat load on the OSM tile server — there's no background
// sweep, a tile just quietly refreshes itself next time it's requested.
const tileCacheMaxAge = 30 * 24 * time.Hour

// getOrCreateTileToken returns the instance's public tile-proxy token,
// generating and persisting a random one on first use (#1269) so a fresh
// install works immediately with no manual setup — the main site's own map
// and the embed pages all read it back via site_settings/siteSettingsCache
// and append it to their tile URLs. There's no webmin control to rotate it
// yet (deliberately out of scope for the initial fix); an admin who needs
// to invalidate a leaked token can update the "tile_token" row directly.
func getOrCreateTileToken(db *sql.DB) string {
	if tok := getSiteSetting(db, "tile_token"); tok != "" {
		return tok
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		log.Printf("getOrCreateTileToken: rand.Read: %v", err)
		return ""
	}
	tok := hex.EncodeToString(buf)
	if err := setSiteSetting(db, "tile_token", tok); err != nil {
		log.Printf("getOrCreateTileToken: persist: %v", err)
	}
	return tok
}

// tileAPIKeyCache remembers a bearer token's recent validity against
// dansal's own API (dansal_web has no local copy of api_keys — that table
// lives in dansal's own database) so a programmatic caller (#1269's
// "wp-dansal" case) fetching many tiles doesn't trigger one cross-service
// call per tile. Valid results cache longer than invalid ones; a crude size
// cap resets the whole cache instead of growing unbounded if something
// floods the endpoint with unique garbage tokens — this is a courtesy
// cache, not a hardened rate limiter.
type tileAPIKeyCache struct {
	mu      sync.Mutex
	entries map[string]tileAPIKeyCacheEntry
}
type tileAPIKeyCacheEntry struct {
	valid   bool
	expires time.Time
}

var apiKeyTileCache = &tileAPIKeyCache{entries: map[string]tileAPIKeyCacheEntry{}}

const (
	tileAPIKeyValidTTL   = 5 * time.Minute
	tileAPIKeyInvalidTTL = 30 * time.Second
	tileAPIKeyCacheMax   = 10_000
)

func (c *tileAPIKeyCache) check(ctx context.Context, client *DansalClient, token string) bool {
	c.mu.Lock()
	if e, ok := c.entries[token]; ok && time.Now().Before(e.expires) {
		c.mu.Unlock()
		return e.valid
	}
	if len(c.entries) > tileAPIKeyCacheMax {
		c.entries = map[string]tileAPIKeyCacheEntry{}
	}
	c.mu.Unlock()

	// GET /api/v1/apikeys is a self-service "list my own keys" endpoint —
	// reusing it here as a validity probe (any 200 response means auth()
	// on the dansal side accepted the bearer token) avoids inventing a new
	// dansal-side endpoint just for this.
	_, err := client.ListAPIKeys(ctx, token)
	valid := err == nil
	ttl := tileAPIKeyInvalidTTL
	if valid {
		ttl = tileAPIKeyValidTTL
	}

	c.mu.Lock()
	c.entries[token] = tileAPIKeyCacheEntry{valid: valid, expires: time.Now().Add(ttl)}
	c.mu.Unlock()
	return valid
}

// tileRequestAuthorized implements #1269's "Option B": the tile proxy used
// to be completely open, letting anyone use a dansal instance as a free OSM
// tile mirror (bandwidth cost, and a policy violation of OSM's tile usage
// policy, which forbids that kind of unmetered third-party use). Two ways
// in, checked cheapest-first:
//  1. The instance's own public tile token as a "t" query parameter — used
//     by the main site's own map (base.js's makeTileLayer) and by the embed
//     pages, both of which inject it server-side into the tile URL
//     template. A plain URL query param is the only mechanism available at
//     all here: Leaflet's default tile layer requests tiles as plain <img>
//     loads, which cannot carry a custom Authorization header.
//  2. A real dansal API key via "Authorization: Bearer" — for a
//     programmatic caller (e.g. wp-dansal) that can attach real headers,
//     checked (with caching) against dansal's own API.
//
// This is deliberately not hardened, unbreakable security — the public
// token is visible in anyone's page source the moment they view it. The
// goal is stopping casual/automated hotlinking of an open endpoint, not
// protecting sensitive data (map tiles aren't sensitive).
func tileRequestAuthorized(r *http.Request, client *DansalClient) bool {
	if siteCfg != nil {
		if t := r.URL.Query().Get("t"); t != "" && t == siteCfg.TileToken() {
			return true
		}
	}
	if authz := r.Header.Get("Authorization"); strings.HasPrefix(authz, "Bearer ") {
		token := strings.TrimPrefix(authz, "Bearer ")
		return apiKeyTileCache.check(r.Context(), client, token)
	}
	return false
}

// tileTokenHandler serves GET /tiles/token (#1287): a public, unauthenticated
// endpoint handing out the instance's tile-proxy token as JSON, for an
// external integration (e.g. wp-dansal) with no dansal API key configured to
// use as the "?t=" fallback auth on the tile proxy instead of falling back to
// a raw OSM fetch. This isn't a secret being exposed — the same token is
// already embedded in plain page HTML on every dansal_web page (see
// tileRequestAuthorized); this just gives a non-browser caller a way to fetch
// it too, closing the gap WEB.md's tile-proxy section documents.
func tileTokenHandler(w http.ResponseWriter, r *http.Request) {
	writeJSONResponse(w, http.StatusOK, map[string]string{"token": siteCfg.TileToken()})
}

// tileProxyHandler serves GET /tiles/{scheme}/{z}/{x}/{yfile}, proxying and
// disk-caching OSM map tiles (#1079) so visitor browsers only ever talk to
// dansal_web — never a third-party tile server directly. This both brings
// tile fetching into compliance with OSM's tile usage policy (which
// forbids heavy direct use by distributed apps) and stops leaking visitor
// IPs to OSM on every map view. yfile is "{y}.png" or "{y}@2x.png" — the
// latter only ever requested if a caller enables Leaflet's detectRetina,
// which dansal_web's own tile layers currently don't, but the proxy handles
// it anyway for forward compatibility.
func tileProxyHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !tileRequestAuthorized(r, client) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		scheme := r.PathValue("scheme")
		tpl, ok := tileUpstreams[scheme]
		if !ok {
			http.NotFound(w, r)
			return
		}
		z, errZ := strconv.Atoi(r.PathValue("z"))
		x, errX := strconv.Atoi(r.PathValue("x"))
		if errZ != nil || errX != nil || z < 0 || z > 22 || x < 0 {
			http.Error(w, "bad tile coordinates", http.StatusBadRequest)
			return
		}
		yfile := r.PathValue("yfile")
		name := strings.TrimSuffix(yfile, ".png")
		if name == yfile {
			http.NotFound(w, r)
			return
		}
		retina := ""
		if strings.HasSuffix(name, "@2x") {
			retina = "@2x"
			name = strings.TrimSuffix(name, "@2x")
		}
		y, errY := strconv.Atoi(name)
		maxCoord := 1 << uint(z)
		if errY != nil || y < 0 || x >= maxCoord || y >= maxCoord {
			http.Error(w, "bad tile coordinates", http.StatusBadRequest)
			return
		}

		cacheDir := cfg.TileCacheDir
		if cacheDir == "" {
			cacheDir = defaultTileCacheDir(cfg)
		}
		basePath := filepath.Join(cacheDir, scheme, strconv.Itoa(z), strconv.Itoa(x), strconv.Itoa(y)+retina)
		pngPath := basePath + ".png"
		avifPath := basePath + ".avif"

		// Content-negotiated (#1327), same pattern as base.js's br/gzip
		// negotiation (#1323): only a client that actually advertises AVIF
		// support in its own Accept header (a plain <img> tile load still
		// carries the browser's real image Accept list) is offered the
		// smaller AVIF cache file. Leaflet's tile URL template is untouched —
		// it still requests a ".png"-suffixed URL regardless of which
		// representation actually comes back, the same "real format doesn't
		// match the URL extension" pattern some real-world tile providers
		// already use; the browser decodes by Content-Type, not the URL.
		wantsAVIF := strings.Contains(r.Header.Get("Accept"), "image/avif")

		if wantsAVIF {
			if fi, err := os.Stat(avifPath); err == nil && time.Since(fi.ModTime()) < tileCacheMaxAge {
				if data, err := os.ReadFile(avifPath); err == nil {
					writeTileResponse(w, "image/avif", data)
					return
				}
			}
		}
		if fi, err := os.Stat(pngPath); err == nil && time.Since(fi.ModTime()) < tileCacheMaxAge {
			if data, err := os.ReadFile(pngPath); err == nil {
				writeTileResponse(w, "image/png", data)
				return
			}
		}

		upstreamURL := fmt.Sprintf(tpl, z, x, y)

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstreamURL, nil)
		if err != nil {
			http.Error(w, "tile fetch error", http.StatusBadGateway)
			return
		}
		req.Header.Set("User-Agent", tileUserAgent)
		resp, err := tileHTTPClient.Do(req)
		if err != nil {
			log.Printf("tiles: upstream fetch failed url=%s err=%v", upstreamURL, err)
			http.Error(w, "tile fetch error", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			w.WriteHeader(resp.StatusCode)
			return
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, tileCacheMaxBytes))
		if err != nil {
			http.Error(w, "tile fetch error", http.StatusBadGateway)
			return
		}

		if err := os.MkdirAll(filepath.Dir(pngPath), 0755); err != nil {
			log.Printf("tiles: mkdir cache dir failed path=%s err=%v", filepath.Dir(pngPath), err)
		} else if err := os.WriteFile(pngPath, data, 0644); err != nil {
			log.Printf("tiles: write cache failed path=%s err=%v", pngPath, err)
		}

		// AVIF conversion is best-effort: any failure (corrupt/unexpected
		// upstream response, encoder trouble) just falls back to the
		// original PNG bytes already cached and about to be served below --
		// never blocks the response.
		avifData, encErr := encodeTileAVIF(data)
		if encErr != nil {
			log.Printf("tiles: avif encode failed url=%s err=%v -- serving PNG", upstreamURL, encErr)
		} else if err := os.WriteFile(avifPath, avifData, 0644); err != nil {
			log.Printf("tiles: write avif cache failed path=%s err=%v", avifPath, err)
		}

		if wantsAVIF && encErr == nil {
			writeTileResponse(w, "image/avif", avifData)
			return
		}
		writeTileResponse(w, "image/png", data)
	}
}

// encodeTileAVIF decodes a PNG tile and re-encodes it as AVIF. Real OSM
// tiles are small (256x256) so a warm encode is on the order of tens of
// milliseconds (see warmAVIFEncoder) -- only the very first encode of the
// process's lifetime pays the WASM runtime's ~2s init cost, which
// warmAVIFEncoder pays at startup instead of on a visitor's request.
func encodeTileAVIF(pngData []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(pngData))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var buf bytes.Buffer
	if err := avif.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return buf.Bytes(), nil
}

// warmAVIFEncoder pays the AVIF WASM runtime's one-time init cost (~2s,
// measured) at server startup instead of inline on whichever visitor's
// request happens to trigger the process's first tile (or image upload)
// AVIF encode. Safe to call even if no tile/image ever needs AVIF.
func warmAVIFEncoder() {
	avif.InitEncoder()
}

// writeTileResponse sends a cached or freshly-fetched tile image. Tiles for
// a given z/x/y are immutable in practice, so a long max-age is safe.
func writeTileResponse(w http.ResponseWriter, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=2592000, immutable")
	w.Write(data)
}

// backfillTileAVIF walks dir for cached .png tiles with no .avif sibling yet
// and encodes one locally from the already-on-disk PNG bytes -- no upstream
// fetch, so it never touches OSM's tile servers (#1327). Lets an existing
// instance's tile cache (populated before AVIF re-encoding shipped, or an
// instance where a backfill just wasn't run yet) get the smaller format
// immediately instead of waiting on tileCacheMaxAge's 30-day natural
// refresh. Safe to interrupt and re-run: already-converted tiles are
// skipped. Invoked via `dansal-web --backfill-tile-avif -config <path>`
// (main.go), which exits after this returns instead of starting the server.
func backfillTileAVIF(dir string) (converted, skipped, failed int) {
	warmAVIFEncoder()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("tiles backfill: walk error at %s: %v", path, err)
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".png") {
			return nil
		}
		avifPath := strings.TrimSuffix(path, ".png") + ".avif"
		if _, err := os.Stat(avifPath); err == nil {
			skipped++
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("tiles backfill: read %s: %v", path, err)
			failed++
			return nil
		}
		avifData, err := encodeTileAVIF(data)
		if err != nil {
			log.Printf("tiles backfill: encode %s: %v", path, err)
			failed++
			return nil
		}
		if err := os.WriteFile(avifPath, avifData, 0644); err != nil {
			log.Printf("tiles backfill: write %s: %v", avifPath, err)
			failed++
			return nil
		}
		converted++
		return nil
	})
	if err != nil {
		log.Printf("tiles backfill: %v", err)
	}
	return converted, skipped, failed
}
