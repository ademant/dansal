package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

// fetchNominatim waits for the global pacing slot, then queries Nominatim's
// /search endpoint restricted to settlement-level results (city/town/village)
// so street addresses and POIs don't clutter city-radius suggestions.
func fetchNominatim(ctx context.Context, cfg *Config, q string) ([]geocodeResult, error) {
	if err := waitGeocodeSlot(ctx); err != nil {
		return nil, err
	}

	endpoint := "https://nominatim.openstreetmap.org/search?" + url.Values{
		"q":              {q},
		"format":         {"json"},
		"limit":          {"5"},
		"addressdetails": {"0"},
		"featureType":    {"settlement"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	// Nominatim's usage policy requires a User-Agent identifying the
	// application and a contact — reused from cfg so it stays accurate
	// without a separate setting.
	contact := cfg.SecurityContact
	if contact == "" {
		contact = "https://" + cfg.Domain
	}
	req.Header.Set("User-Agent", fmt.Sprintf("dansal-web/1.0 (+https://%s; %s)", cfg.Domain, contact))

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nominatim returned %d", resp.StatusCode)
	}

	var items []nominatimItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
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
