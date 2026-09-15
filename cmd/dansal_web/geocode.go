package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Online city fallback for /search's town filter (#977): when a visitor
// types a place that has no events (and so no match against the DB's known
// towns), this endpoint geocodes it via Nominatim so the radius filter can
// still be used, exactly like the existing "Locate me" flow.

// geocodeCacheTTL is how long a cached Nominatim result is served without
// re-fetching. City coordinates are effectively static, so this is generous;
// it's measured from first insert, not refreshed on reads (see setGeocodeCache).
const geocodeCacheTTL = 90 * 24 * time.Hour

// geocodeMinQueryLen mirrors the frontend's own debounce/min-length gate —
// enforced again here since the endpoint is reachable directly.
const geocodeMinQueryLen = 3

// geocodeMinInterval paces outbound Nominatim requests globally (across all
// visitors), independent of the per-IP throttle, per Nominatim's usage
// policy (max ~1 request/second, and no parallel requests).
const geocodeMinInterval = 1100 * time.Millisecond

var (
	geocodeRateMu   sync.Mutex
	geocodeLastCall time.Time
)

// nominatimBaseURL is a package-level var (not a const) so tests can point
// it at an httptest server instead of the real Nominatim — mirrors
// tileUpstreams' swappable-for-tests pattern in tiles.go.
var nominatimBaseURL = "https://nominatim.openstreetmap.org"

// geocodeResult is the trimmed shape sent to the browser — just enough to
// label a suggestion and drive the radius filter.
type geocodeResult struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lng  float64 `json:"lng"`
}

// nominatimItem mirrors the subset of Nominatim's /search response used here.
type nominatimItem struct {
	DisplayName string `json:"display_name"`
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
}

func geocodeHandler(cfg *Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if geocodeThrottle.isBlocked(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}
		geocodeThrottle.record(ip)

		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if len(q) < geocodeMinQueryLen {
			http.Error(w, "q must be at least 3 characters", http.StatusBadRequest)
			return
		}
		cacheKey := strings.ToLower(q)

		w.Header().Set("Content-Type", "application/json")
		if cached, ok := getGeocodeCache(db, cacheKey, geocodeCacheTTL); ok {
			w.Write([]byte(cached))
			return
		}

		results, err := fetchNominatim(r.Context(), cfg, q)
		if err != nil {
			logHTTPError(w, r, "geocode lookup failed", http.StatusBadGateway)
			return
		}

		b, err := json.Marshal(results)
		if err != nil {
			logHTTPError(w, r, "could not encode results", http.StatusInternalServerError)
			return
		}
		_ = setGeocodeCache(db, cacheKey, string(b))
		w.Write(b)
	}
}

// fetchNominatim queries Nominatim's /search endpoint restricted to
// settlement-level results (city/town/village) so street addresses and POIs
// don't clutter city-radius suggestions.
func fetchNominatim(ctx context.Context, cfg *Config, q string) ([]geocodeResult, error) {
	body, err := nominatimGet(ctx, cfg, "/search", url.Values{
		"q":              {q},
		"format":         {"json"},
		"limit":          {"5"},
		"addressdetails": {"0"},
		"featureType":    {"settlement"},
	})
	if err != nil {
		return nil, err
	}

	var items []nominatimItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}

	results := make([]geocodeResult, 0, len(items))
	for _, it := range items {
		lat, err1 := strconv.ParseFloat(it.Lat, 64)
		lng, err2 := strconv.ParseFloat(it.Lon, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		results = append(results, geocodeResult{Name: it.DisplayName, Lat: lat, Lng: lng})
	}
	return results, nil
}

// nominatimMaxBody caps how much of an upstream Nominatim response is read,
// guarding against a misbehaving/compromised upstream — real responses here
// are at most a few KB per result.
const nominatimMaxBody = 1 << 20 // 1MB

// nominatimUserAgent builds the User-Agent Nominatim's usage policy
// requires (https://operations.osmfoundation.org/policies/nominatim/) —
// shared by every dansal_web call to Nominatim, direct (fetchNominatim) or
// proxied (nominatimGeocodeSearchHandler/nominatimGeocodeReverseHandler).
func nominatimUserAgent(cfg *Config) string {
	contact := cfg.SecurityContact
	if contact == "" {
		contact = "https://" + cfg.Domain
	}
	return fmt.Sprintf("dansal-web/1.0 (+https://%s; %s)", cfg.Domain, contact)
}

// nominatimGet waits for the shared pacing slot (waitGeocodeSlot), issues a
// GET to Nominatim at path with the given query params, and returns the raw
// response body. Every dansal_web call to Nominatim goes through this one
// function, so the combined outbound rate — regardless of which feature or
// how many concurrent local users triggered it — never exceeds Nominatim's
// ~1 request/second policy.
func nominatimGet(ctx context.Context, cfg *Config, path string, params url.Values) ([]byte, error) {
	if err := waitGeocodeSlot(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nominatimBaseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", nominatimUserAgent(cfg))

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nominatim returned %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, nominatimMaxBody))
}

// nominatimLangPattern matches the shape of every lang value dansal's own
// JS actually sends (document.documentElement.lang, e.g. "de", or
// nominatimLang()'s comma-joined region fallbacks, e.g. "ca,fr,it") — a
// defensive allowlist for a value that ends up in an outbound query param
// to a third party, rather than passing arbitrary client input through.
var nominatimLangPattern = regexp.MustCompile(`^[a-zA-Z]{2}(-[a-zA-Z]{2})?(,[a-zA-Z]{2}(-[a-zA-Z]{2})?){0,4}$`)

func sanitizeNominatimLang(lang string) string {
	if nominatimLangPattern.MatchString(lang) {
		return lang
	}
	return ""
}

// clampGeocodeLimit parses raw as a result-count limit, defaulting to 5 and
// capping at 10 — every current caller asks for 1, 5, or 8.
func clampGeocodeLimit(raw string) string {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		n = 5
	} else if n > 10 {
		n = 10
	}
	return strconv.Itoa(n)
}

// nominatimGeocodeSearchHandler is GET /search/geocode/search — a general
// (not settlement-restricted) Nominatim address search, proxied server-side
// so the browser never talks to Nominatim directly (#1313): a browser
// fetch() can't set the User-Agent Nominatim's policy requires (it's a
// forbidden header), and independent page loads have no way to coordinate
// the required shared pacing among themselves. Shares geocodeThrottle
// (per-IP) with the settlement-only /search/geocode endpoint above, and
// nominatimGet's pacing with every other Nominatim call in this file — used
// by both admin location-entry forms and the public board-post/
// suggest-event location pickers.
func nominatimGeocodeSearchHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		w.Header().Set("Content-Type", "application/json")
		if geocodeThrottle.isBlocked(ip) {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`[]`))
			return
		}
		geocodeThrottle.record(ip)

		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			http.Error(w, "q is required", http.StatusBadRequest)
			return
		}
		params := url.Values{
			"format":         {"json"},
			"q":              {q},
			"limit":          {clampGeocodeLimit(r.URL.Query().Get("limit"))},
			"addressdetails": {"1"},
		}
		if lang := sanitizeNominatimLang(r.URL.Query().Get("lang")); lang != "" {
			params.Set("accept-language", lang)
		}
		body, err := nominatimGet(r.Context(), cfg, "/search", params)
		if err != nil {
			logHTTPError(w, r, "geocode search failed", http.StatusBadGateway)
			return
		}
		w.Write(body)
	}
}

// nominatimGeocodeReverseHandler is GET /search/geocode/reverse — see
// nominatimGeocodeSearchHandler above for why this is proxied server-side
// rather than called from the browser directly. Unlike search, every
// current caller (admin_location_edit.html, admin_event_form.html,
// admin_locations_maintenance.html) is an admin-only page — no public form
// does reverse geocoding — so this stays behind requireLogin rather than
// being reachable by anyone the way the public search proxy has to be.
func nominatimGeocodeReverseHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireLogin(w, r); !ok {
			return
		}
		ip := getClientIP(r)
		w.Header().Set("Content-Type", "application/json")
		if geocodeThrottle.isBlocked(ip) {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}
		geocodeThrottle.record(ip)

		lat := r.URL.Query().Get("lat")
		lon := r.URL.Query().Get("lon")
		if _, err := strconv.ParseFloat(lat, 64); err != nil {
			http.Error(w, "lat is required", http.StatusBadRequest)
			return
		}
		if _, err := strconv.ParseFloat(lon, 64); err != nil {
			http.Error(w, "lon is required", http.StatusBadRequest)
			return
		}
		params := url.Values{"format": {"json"}, "lat": {lat}, "lon": {lon}, "addressdetails": {"1"}}
		if lang := sanitizeNominatimLang(r.URL.Query().Get("lang")); lang != "" {
			params.Set("accept-language", lang)
		}
		body, err := nominatimGet(r.Context(), cfg, "/reverse", params)
		if err != nil {
			logHTTPError(w, r, "geocode reverse failed", http.StatusBadGateway)
			return
		}
		w.Write(body)
	}
}

// waitGeocodeSlot blocks until at least geocodeMinInterval has passed since
// the last outbound Nominatim call, or the request context is cancelled.
func waitGeocodeSlot(ctx context.Context) error {
	geocodeRateMu.Lock()
	wait := geocodeMinInterval - time.Since(geocodeLastCall)
	if wait < 0 {
		wait = 0
	}
	geocodeLastCall = time.Now().Add(wait)
	geocodeRateMu.Unlock()

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
