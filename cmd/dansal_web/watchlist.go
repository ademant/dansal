package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// The watchlist (#1554) is kept in the visitor's browser (localStorage, see
// base.js). The server only resolves the stored ids for the watchlist page: it
// stores nothing. The page posts the ids here and removes from its local list
// any id reported as missing (deleted, unpublished, or already past).

// watchlistMaxIDs caps a watchlist at 200 events; a lookup body with more is
// refused.
const watchlistMaxIDs = 200

// watchlistLookupConcurrency bounds the API calls one lookup makes at once.
const watchlistLookupConcurrency = 8

// watchlistLookupThrottle limits how often one IP can resolve a watchlist. A
// lookup fans out to one API call per id, so it is the expensive call here.
var watchlistLookupThrottle = newSubmissionThrottle(30, time.Minute)

var (
	errWatchlistTooLong = errors.New("watchlist has too many entries")
	errWatchlistBadID   = errors.New("watchlist ids must be positive integers")
)

type watchlistLookupRequest struct {
	IDs []int `json:"ids"`
}

// WatchlistItem is one resolved watchlist entry: only what the page shows.
type WatchlistItem struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Location  string `json:"location"`
	Cancelled bool   `json:"cancelled"`
	Path      string `json:"path"`
	// Venue details for navigation and for the offline snapshot (#1605). Lat and
	// Lon are set only when the venue is geo-tagged.
	Address string   `json:"address,omitempty"`
	Town    string   `json:"town,omitempty"`
	Lat     *float64 `json:"lat,omitempty"`
	Lon     *float64 `json:"lon,omitempty"`
}

// WatchlistLookupResponse is the lookup's reply. Missing lists the ids the
// page should drop from its local list.
type WatchlistLookupResponse struct {
	Items   []WatchlistItem `json:"items"`
	Missing []int           `json:"missing"`
}

// cleanWatchlistIDs rejects anything other than a short list of positive
// integers, and drops duplicates while keeping the order.
func cleanWatchlistIDs(in []int) ([]int, error) {
	if len(in) > watchlistMaxIDs {
		return nil, errWatchlistTooLong
	}
	seen := make(map[int]bool, len(in))
	out := make([]int, 0, len(in))
	for _, id := range in {
		if id <= 0 {
			return nil, errWatchlistBadID
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// watchlistEventEnded reports whether an event is over. It uses the end time,
// or the start time when there is none. An unparseable time counts as not
// ended, so a malformed value never removes an entry.
func watchlistEventEnded(ev Event, now time.Time) bool {
	t := ev.EndTime
	if t == "" {
		t = ev.StartTime
	}
	parsed, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return false
	}
	return parsed.Before(now)
}

// resolveWatchlist fetches each id from the API. A 404 means the event is gone;
// any other API error fails the whole lookup, so the page keeps its list when
// the API is unavailable.
func resolveWatchlist(ctx context.Context, client *DansalClient, ids []int, now time.Time) (WatchlistLookupResponse, error) {
	type result struct {
		ev  Event
		err error
	}
	results := make([]result, len(ids))
	sem := make(chan struct{}, watchlistLookupConcurrency)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i, id int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ev, err := client.GetEvent(ctx, id)
			results[i] = result{ev: ev, err: err}
		}(i, id)
	}
	wg.Wait()

	resp := WatchlistLookupResponse{Items: []WatchlistItem{}, Missing: []int{}}
	for i, res := range results {
		switch {
		case errors.Is(res.err, errNotFound):
			resp.Missing = append(resp.Missing, ids[i])
		case res.err != nil:
			return WatchlistLookupResponse{}, res.err
		case !res.ev.IsPublished || watchlistEventEnded(res.ev, now):
			resp.Missing = append(resp.Missing, ids[i])
		default:
			resp.Items = append(resp.Items, watchlistItemFrom(res.ev))
		}
	}
	return resp, nil
}

func watchlistItemFrom(ev Event) WatchlistItem {
	item := WatchlistItem{
		ID:        ev.ID,
		Title:     ev.Title,
		StartTime: ev.StartTime,
		EndTime:   ev.EndTime,
		Cancelled: ev.IsCancelled,
		Path:      "/events/" + strconv.Itoa(ev.ID),
	}
	if ev.Location != nil {
		item.Location = ev.Location.Location
		item.Address = ev.Location.Address
		item.Town = ev.Location.Town
		item.Lat = ev.Location.Latitude
		item.Lon = ev.Location.Longitude
	}
	return item
}

// watchlistPageHandler serves GET /watchlist. The page is a shell: base.js
// reads the stored ids and fills the list.
func watchlistPageHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := i18n.T(r, "wl_page_title")
		renderTemplate(w, tmpls.watchlist, tmplData(r, cfg, i18n, title, nil))
	}
}

// watchlistLookupHandler serves POST /watchlist/events. It is read-only, so it
// needs no form token; it is rate-limited because each lookup costs one API
// call per id.
func watchlistLookupHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if watchlistLookupThrottle.isBlocked(ip) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		watchlistLookupThrottle.record(ip)

		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var req watchlistLookupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		ids, err := cleanWatchlistIDs(req.IDs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp, err := resolveWatchlist(r.Context(), client, ids, time.Now())
		if err != nil {
			logHTTPError(w, r, "watchlist lookup failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(resp)
	}
}
