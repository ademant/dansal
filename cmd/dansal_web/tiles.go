package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// tileUpstreams maps the client-supplied scheme segment to a printf template
// for the real upstream tile server. Upstream URL templates in Leaflet
// traditionally rotate across {a,b,c} subdomains to let browsers open more
// parallel connections; since dansal_web itself does the fetching now (the
// browser only ever talks to dansal_web), a single static subdomain is fine.
var tileUpstreams = map[string]string{
	"osm":  "https://a.tile.openstreetmap.org/%d/%d/%d.png",
	"dark": "https://a.basemaps.cartocdn.com/dark_all/%d/%d/%d%s.png",
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

// tileProxyHandler serves GET /tiles/{scheme}/{z}/{x}/{yfile}, proxying and
// disk-caching OSM/CARTO map tiles (#1079) so visitor browsers only ever
// talk to dansal_web — never a third-party tile server directly. This both
// brings tile fetching into compliance with OSM's tile usage policy (which
// forbids heavy direct use by distributed apps) and stops leaking visitor
// IPs to OSM/CARTO on every map view. yfile is "{y}.png" or "{y}@2x.png" —
// the latter only ever requested if a caller enables Leaflet's detectRetina,
// which dansal_web's own tile layers currently don't, but the proxy handles
// it anyway for forward compatibility.
func tileProxyHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		cachePath := filepath.Join(cacheDir, scheme, strconv.Itoa(z), strconv.Itoa(x), strconv.Itoa(y)+retina+".png")

		if data, err := os.ReadFile(cachePath); err == nil {
			writeTileResponse(w, data)
			return
		}

		var upstreamURL string
		if scheme == "dark" {
			upstreamURL = fmt.Sprintf(tpl, z, x, y, retina)
		} else {
			upstreamURL = fmt.Sprintf(tpl, z, x, y)
		}

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

		if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
			log.Printf("tiles: mkdir cache dir failed path=%s err=%v", filepath.Dir(cachePath), err)
		} else if err := os.WriteFile(cachePath, data, 0644); err != nil {
			log.Printf("tiles: write cache failed path=%s err=%v", cachePath, err)
		}

		writeTileResponse(w, data)
	}
}

// writeTileResponse sends a cached or freshly-fetched tile image. Tiles for
// a given z/x/y are immutable in practice, so a long max-age is safe.
func writeTileResponse(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=2592000, immutable")
	w.Write(data)
}
