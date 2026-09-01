package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"mime/multipart"
	"sync"
	"time"
)

// External event overlay (#1220): live map pins sourced from external sites
// (e.g. folkbalbende.be), fetched periodically via dansal's own
// POST /api/v1/events/preview endpoint and cached in memory — never
// imported, never written to dansal's DB. Reusing the preview endpoint
// means reusing its parser (parseBodyToRequests) and, critically, its dedup
// check (previewDuplicateStatus, the same findExistingEvent hierarchy real
// imports use): only events the preview endpoint tags "new" are kept, so an
// event dansal already has (via a real import, or entered manually) is
// never shown twice. Fully opt-in — see cfg.ExternalOverlaySources.

var (
	externalOverlayMu   sync.RWMutex
	externalOverlayJSON = []byte("[]")
)

// currentExternalOverlayJSON returns the last-refreshed external overlay
// cache, pre-marshalled as a JSON array of geoEvent-shaped pins, ready to
// embed directly into the index page. Always valid JSON, even before the
// first refresh completes or when the feature isn't configured.
func currentExternalOverlayJSON() []byte {
	externalOverlayMu.RLock()
	defer externalOverlayMu.RUnlock()
	return externalOverlayJSON
}

// startExternalOverlay periodically refreshes the external map overlay
// cache from cfg's configured sources. No-op (never starts a ticker) when
// no sources — or no API key to call the preview endpoint with — are
// configured, keeping the feature a true no-op for instances that don't
// opt in.
func startExternalOverlay(cfg *Config, client *DansalClient) {
	if len(cfg.ExternalOverlaySources) == 0 || cfg.ExternalOverlayAPIKey == "" {
		return
	}
	pollMins := cfg.ExternalOverlayPollMins
	if pollMins <= 0 {
		pollMins = 30
	}
	refreshExternalOverlay(cfg, client)
	ticker := time.NewTicker(time.Duration(pollMins) * time.Minute)
	for range ticker.C {
		refreshExternalOverlay(cfg, client)
	}
}

// refreshExternalOverlay fetches every configured source and replaces the
// cache with the combined result. A source that fails to fetch is logged
// and skipped for this cycle rather than blanking the whole cache — the
// previous cycle's pins for the other sources stay in place until it
// succeeds again.
func refreshExternalOverlay(cfg *Config, client *DansalClient) {
	var pins []geoEvent
	for _, src := range cfg.ExternalOverlaySources {
		fetched, err := fetchExternalOverlaySource(cfg, client, src)
		if err != nil {
			log.Printf("external overlay: fetch %s: %v", src.Name, err)
			continue
		}
		pins = append(pins, fetched...)
	}
	// Assign synthetic negative IDs, unique across all sources combined —
	// preview responses carry no dansal event ID (nothing was created) and
	// no stable per-source ID either, so index.html's allMarkers[e.id]
	// registry just needs uniqueness within one refresh cycle, not
	// stability across cycles (pins are never linked to or bookmarked).
	for i := range pins {
		pins[i].ID = -(i + 1)
	}
	if pins == nil {
		pins = []geoEvent{}
	}
	b, err := json.Marshal(pins)
	if err != nil {
		log.Printf("external overlay: marshal: %v", err)
		return
	}
	externalOverlayMu.Lock()
	externalOverlayJSON = b
	externalOverlayMu.Unlock()
}

// fetchExternalOverlaySource fetches and parses one configured source via
// dansal's preview endpoint (fetch + parse + dedup, all reused — #1220),
// keeping only events dansal doesn't already have ("new") that carry both
// coordinates (nothing else can be placed on the map) and a URL (nothing
// else is useful to click through to, since there's no dansal event page
// for an external pin).
func fetchExternalOverlaySource(cfg *Config, client *DansalClient, src ExternalOverlaySource) ([]geoEvent, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("url", src.URL); err != nil {
		return nil, err
	}
	if src.Type != "" {
		if err := mw.WriteField("type", src.Type); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events, err := client.PreviewEvents(ctx, &buf, mw.FormDataContentType(), cfg.ExternalOverlayAPIKey)
	if err != nil {
		return nil, err
	}

	var pins []geoEvent
	for _, ev := range events {
		if ev.Status != "" && ev.Status != "new" {
			continue // dansal already has this event — skip to avoid a duplicate pin
		}
		if ev.Location.Latitude == nil || ev.Location.Longitude == nil {
			continue
		}
		if ev.URL == "" {
			continue
		}
		pins = append(pins, geoEvent{
			Title: ev.Title, Start: ev.StartTime, End: ev.EndTime,
			Location: ev.Location.Location, Town: ev.Location.Town, Country: ev.Location.Country,
			Lat: *ev.Location.Latitude, Lng: *ev.Location.Longitude,
			URL: ev.URL, Cancelled: ev.IsCancelled,
			Ext: true, Src: src.Name,
		})
	}
	return pins, nil
}
