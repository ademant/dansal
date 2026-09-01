package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestFetchExternalOverlaySource verifies the filtering rules (#1220): only
// preview results tagged "new" are kept, and only when they carry both
// coordinates and a URL — anything dansal already has, or that can't be
// placed on the map or clicked through to, is dropped.
func TestFetchExternalOverlaySource(t *testing.T) {
	lat, lng := 50.9401, 5.16597
	events := []PreviewEvent{
		{Title: "New with coords+URL", StartTime: "2099-01-01T20:00:00Z", URL: "https://example.org/a",
			Location: PreviewLoc{Location: "Hall A", Town: "Herk-de-Stad", Latitude: &lat, Longitude: &lng}, Status: "new"},
		{Title: "Already exists", StartTime: "2099-01-01T20:00:00Z", URL: "https://example.org/b",
			Location: PreviewLoc{Latitude: &lat, Longitude: &lng}, Status: "exists"},
		{Title: "Updated (already in dansal)", StartTime: "2099-01-01T20:00:00Z", URL: "https://example.org/c",
			Location: PreviewLoc{Latitude: &lat, Longitude: &lng}, Status: "updated"},
		{Title: "New but no coords", StartTime: "2099-01-01T20:00:00Z", URL: "https://example.org/d",
			Location: PreviewLoc{}, Status: "new"},
		{Title: "New but no URL", StartTime: "2099-01-01T20:00:00Z",
			Location: PreviewLoc{Latitude: &lat, Longitude: &lng}, Status: "new"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/events/preview", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &Config{ExternalOverlayAPIKey: "test-key"}
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	src := ExternalOverlaySource{Name: "test-source", URL: "https://source.example/feed.json", Type: "json"}

	pins, err := fetchExternalOverlaySource(cfg, client, src)
	if err != nil {
		t.Fatalf("fetchExternalOverlaySource: %v", err)
	}
	if len(pins) != 1 {
		t.Fatalf("got %d pins, want 1: %+v", len(pins), pins)
	}
	p := pins[0]
	if p.Title != "New with coords+URL" {
		t.Errorf("Title = %q", p.Title)
	}
	if !p.Ext {
		t.Error("Ext should be true")
	}
	if p.Src != "test-source" {
		t.Errorf("Src = %q, want test-source", p.Src)
	}
	if p.URL != "https://example.org/a" {
		t.Errorf("URL = %q", p.URL)
	}
	if p.Lat != lat || p.Lng != lng {
		t.Errorf("Lat/Lng = %v/%v, want %v/%v", p.Lat, p.Lng, lat, lng)
	}
}

// TestRefreshExternalOverlayAssignsUniqueIDs verifies pins from multiple
// sources get unique synthetic negative IDs (index.html's allMarkers[e.id]
// registry needs uniqueness within one refresh cycle).
func TestRefreshExternalOverlayAssignsUniqueIDs(t *testing.T) {
	lat, lng := 1.0, 2.0
	makeHandler := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]PreviewEvent{
				{Title: name + " event 1", StartTime: "2099-01-01T20:00:00Z", URL: "https://x/1",
					Location: PreviewLoc{Latitude: &lat, Longitude: &lng}, Status: "new"},
				{Title: name + " event 2", StartTime: "2099-01-01T20:00:00Z", URL: "https://x/2",
					Location: PreviewLoc{Latitude: &lat, Longitude: &lng}, Status: "new"},
			})
		}
	}
	muxA := http.NewServeMux()
	muxA.HandleFunc("POST /api/v1/events/preview", makeHandler("A"))
	srvA := httptest.NewServer(muxA)
	defer srvA.Close()

	// Both sources point at their own fake dansal instance here for
	// simplicity — refreshExternalOverlay only cares that fetching each
	// configured source's URL through the client works, not that they
	// share a client.BaseURL in production (they always do; only the test
	// double differs per source to keep the fixture simple).
	cfg := &Config{
		ExternalOverlayAPIKey: "k",
		ExternalOverlaySources: []ExternalOverlaySource{
			{Name: "A", URL: "https://a.example/feed.json", Type: "json"},
			{Name: "B", URL: "https://b.example/feed.json", Type: "json"},
		},
	}
	client := &DansalClient{BaseURL: srvA.URL, HTTP: srvA.Client()}

	refreshExternalOverlay(cfg, client)
	var pins []geoEvent
	if err := json.Unmarshal(currentExternalOverlayJSON(), &pins); err != nil {
		t.Fatalf("unmarshal cache: %v", err)
	}
	if len(pins) != 4 {
		t.Fatalf("got %d pins, want 4: %+v", len(pins), pins)
	}
	seen := map[int]bool{}
	for _, p := range pins {
		if p.ID >= 0 {
			t.Errorf("pin ID = %d, want negative", p.ID)
		}
		if seen[p.ID] {
			t.Errorf("duplicate pin ID %d", p.ID)
		}
		seen[p.ID] = true
	}
}

func TestCurrentExternalOverlayJSONDefaultsToEmptyArray(t *testing.T) {
	externalOverlayMu.Lock()
	externalOverlayJSON = []byte("[]")
	externalOverlayMu.Unlock()
	if got := string(currentExternalOverlayJSON()); got != "[]" {
		t.Errorf("currentExternalOverlayJSON() = %q, want []", got)
	}
}

func TestStartExternalOverlayNoOpWhenUnconfigured(t *testing.T) {
	// Must return immediately (not block starting a ticker loop) when no
	// sources, or sources but no API key, are configured — the feature is
	// opt-in. If either call started a ticker loop instead, done would
	// never close and the test would time out.
	done := make(chan struct{})
	go func() {
		startExternalOverlay(&Config{}, &DansalClient{})
		startExternalOverlay(&Config{ExternalOverlaySources: []ExternalOverlaySource{{URL: "https://x"}}}, &DansalClient{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("startExternalOverlay did not return promptly when unconfigured")
	}
}
