package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// This file proxies the admin-only data-enrichment lookups (MusicBrainz,
// Discogs, Wikidata) that admin_musician_edit.html and admin_org_edit.html
// used to call straight from browser JS (#1313) — same problem as the
// Nominatim calls fixed in geocode.go: a browser fetch() can't set a
// compliant User-Agent (forbidden header), and independently-loaded admin
// pages have no way to coordinate the rate limit each of these services
// documents. Each upstream gets its own pacer instance since their limits
// are independent of each other and of Nominatim's.

// externalAPIPacer enforces a minimum gap between outbound calls to one
// third-party host, shared across every dansal_web caller of that host —
// the same role waitGeocodeSlot plays for Nominatim specifically, factored
// out here since three more independent upstreams need the same thing.
type externalAPIPacer struct {
	mu       sync.Mutex
	lastCall time.Time
	minGap   time.Duration
}

func newExternalAPIPacer(minGap time.Duration) *externalAPIPacer {
	return &externalAPIPacer{minGap: minGap}
}

func (p *externalAPIPacer) wait(ctx context.Context) error {
	p.mu.Lock()
	wait := p.minGap - time.Since(p.lastCall)
	if wait < 0 {
		wait = 0
	}
	p.lastCall = time.Now().Add(wait)
	p.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// externalAPIMaxBody caps how much of an upstream response is read, guarding
// against a misbehaving/compromised upstream.
const externalAPIMaxBody = 1 << 20 // 1MB

// externalAPIGet issues a paced GET to url with the given User-Agent and
// returns the raw response body.
func externalAPIGet(ctx context.Context, pacer *externalAPIPacer, rawURL, userAgent string) ([]byte, error) {
	if err := pacer.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, externalAPIMaxBody))
}

// ── MusicBrainz ──────────────────────────────────────────────────────────

// musicBrainzPacer paces at MusicBrainz's documented ~1 request/second
// (https://musicbrainz.org/doc/MusicBrainz_API/Rate_Limiting), shared
// across the search and artist-lookup endpoints below.
var musicBrainzPacer = newExternalAPIPacer(1100 * time.Millisecond)

// musicBrainzBaseURL/discogsBaseURL/wikidataBaseURL are package-level vars
// (not consts) so tests can point them at an httptest server — same
// swappable-for-tests pattern as nominatimBaseURL in geocode.go.
var musicBrainzBaseURL = "https://musicbrainz.org"

func musicBrainzUserAgent(cfg *Config) string {
	contact := cfg.SecurityContact
	if contact == "" {
		contact = "https://" + cfg.Domain
	}
	return fmt.Sprintf("dansal-web/1.0 (+https://%s; %s)", cfg.Domain, contact)
}

// musicBrainzSearchHandler is GET /admin/api/musicbrainz/search?q=...&limit=...
func musicBrainzSearchHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireLogin(w, r); !ok {
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			http.Error(w, "q is required", http.StatusBadRequest)
			return
		}
		endpoint := musicBrainzBaseURL + "/ws/2/artist?" + url.Values{
			"query": {q},
			"fmt":   {"json"},
			"limit": {clampGeocodeLimit(r.URL.Query().Get("limit"))},
		}.Encode()
		body, err := externalAPIGet(r.Context(), musicBrainzPacer, endpoint, musicBrainzUserAgent(cfg))
		if err != nil {
			logHTTPError(w, r, "musicbrainz search failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}
}

// musicBrainzArtistVariants maps the small, fixed set of `inc` combinations
// admin_musician_edit.html actually needs to a `variant` query value, rather
// than passing an arbitrary `inc=` straight through from the client.
var musicBrainzArtistVariants = map[string]string{
	"releases": "release-groups",
	"full":     "url-rels+genres+annotation+release-groups",
}

// musicBrainzArtistHandler is GET /admin/api/musicbrainz/artist/{mbid}?variant=releases|full
func musicBrainzArtistHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireLogin(w, r); !ok {
			return
		}
		mbid := r.PathValue("mbid")
		if mbid == "" {
			http.NotFound(w, r)
			return
		}
		inc, ok := musicBrainzArtistVariants[r.URL.Query().Get("variant")]
		if !ok {
			http.Error(w, "unknown variant", http.StatusBadRequest)
			return
		}
		endpoint := musicBrainzBaseURL + "/ws/2/artist/" + url.PathEscape(mbid) + "?" + url.Values{
			"inc": {inc},
			"fmt": {"json"},
		}.Encode()
		body, err := externalAPIGet(r.Context(), musicBrainzPacer, endpoint, musicBrainzUserAgent(cfg))
		if err != nil {
			logHTTPError(w, r, "musicbrainz artist lookup failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}
}

// ── Discogs ──────────────────────────────────────────────────────────────

// discogsPacer paces well under Discogs' 25 requests/minute unauthenticated
// limit (https://www.discogs.com/developers) — 2.5s keeps a comfortable
// margin even with several admins searching at once.
var discogsPacer = newExternalAPIPacer(2500 * time.Millisecond)

var discogsBaseURL = "https://api.discogs.com"

func discogsUserAgent(cfg *Config) string {
	contact := cfg.SecurityContact
	if contact == "" {
		contact = "https://" + cfg.Domain
	}
	// Discogs' own docs example a "Name/Version +URL" shape — the previous
	// client-side attempt at a custom User-Agent (`fetch(...,
	// {headers:{'User-Agent':'dansal/1.0'}})`) never actually worked:
	// browsers silently drop User-Agent from a fetch()'s header list, so
	// every request went out generically identified — exactly what Discogs'
	// policy says gets throttled harder.
	return fmt.Sprintf("dansal-web/1.0 +https://%s (%s)", cfg.Domain, contact)
}

// discogsSearchHandler is GET /admin/api/discogs/search?q=...
func discogsSearchHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireLogin(w, r); !ok {
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			http.Error(w, "q is required", http.StatusBadRequest)
			return
		}
		endpoint := discogsBaseURL + "/database/search?" + url.Values{
			"q":        {q},
			"type":     {"artist"},
			"per_page": {"8"},
		}.Encode()
		body, err := externalAPIGet(r.Context(), discogsPacer, endpoint, discogsUserAgent(cfg))
		if err != nil {
			logHTTPError(w, r, "discogs search failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}
}

// ── Wikidata Query Service ───────────────────────────────────────────────

// wikidataPacer paces well under the Wikidata Query Service's documented
// fair-use limit of 30 queries/minute per IP
// (https://www.wikidata.org/wiki/Wikidata:SPARQL_query_service/Wikidata_Query_Help).
var wikidataPacer = newExternalAPIPacer(1 * time.Second)

var wikidataBaseURL = "https://query.wikidata.org"

func wikidataUserAgent(cfg *Config) string {
	contact := cfg.SecurityContact
	if contact == "" {
		contact = "https://" + cfg.Domain
	}
	return fmt.Sprintf("dansal-web/1.0 (https://%s; %s)", cfg.Domain, contact)
}

// wikidataQIDProperties maps the small, fixed set of exact-match lookups
// dansal actually needs to their Wikidata property id, rather than
// accepting an arbitrary property (or raw SPARQL) from the client:
//   - website: P856 "official website" (admin_org_edit.html, by org URL)
//   - musicbrainz_artist: P434 "MusicBrainz artist ID" (admin_musician_edit.html, by MBID)
var wikidataQIDProperties = map[string]string{
	"website":            "P856",
	"musicbrainz_artist": "P434",
}

// wikidataQIDHandler is GET /admin/api/wikidata/qid?prop=website|musicbrainz_artist&value=...
// — runs the one matching fixed SPARQL query entirely server-side (find the
// Wikidata item whose statement for that property exactly matches value).
// The client never supplies SPARQL itself, so this can't become an open
// SPARQL relay. Returns {"qid":"Q123"} when exactly one match is found,
// {} otherwise (including on any lookup error — this is a best-effort
// autofill, never worth surfacing a hard failure for).
func wikidataQIDHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireLogin(w, r); !ok {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		prop, ok := wikidataQIDProperties[r.URL.Query().Get("prop")]
		if !ok {
			w.Write([]byte(`{}`))
			return
		}
		value := strings.TrimSpace(r.URL.Query().Get("value"))
		if value == "" || strings.ContainsAny(value, "\"\\") {
			// A value containing a literal quote or backslash can't be safely
			// embedded in the SPARQL string literal below; just treat it as
			// "no match" rather than trying to escape arbitrary input into a
			// query — neither a URL nor an MBID legitimately needs either
			// character.
			w.Write([]byte(`{}`))
			return
		}
		sparql := fmt.Sprintf(`SELECT ?item WHERE { ?item wdt:%s "%s" }`, prop, value)
		endpoint := wikidataBaseURL + "/sparql?" + url.Values{
			"query":  {sparql},
			"format": {"json"},
		}.Encode()
		body, err := externalAPIGet(r.Context(), wikidataPacer, endpoint, wikidataUserAgent(cfg))
		if err != nil {
			w.Write([]byte(`{}`))
			return
		}
		var result struct {
			Results struct {
				Bindings []struct {
					Item struct {
						Value string `json:"value"`
					} `json:"item"`
				} `json:"bindings"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &result); err != nil || len(result.Results.Bindings) != 1 {
			w.Write([]byte(`{}`))
			return
		}
		qid := strings.TrimPrefix(result.Results.Bindings[0].Item.Value, "http://www.wikidata.org/entity/")
		fmt.Fprintf(w, `{"qid":%q}`, qid)
	}
}
