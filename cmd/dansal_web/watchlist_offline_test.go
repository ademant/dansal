package main

import (
	"encoding/json"
	"testing"
)

// #1605: the watchlist items carry the venue details the offline snapshot and
// the navigate link need. Venues without coordinates leave lat and lon out.
func TestWatchlistItemCarriesVenueDetails(t *testing.T) {
	lat, lon := 50.73, 7.09
	ev := Event{
		ID:        7,
		Title:     "Ball",
		StartTime: "2026-11-02T19:00:00+01:00",
		EndTime:   "2026-11-02T23:00:00+01:00",
		Location: &Location{
			Location:  "Saal",
			Address:   "Hauptstr. 1",
			Town:      "Bonn",
			Latitude:  &lat,
			Longitude: &lon,
		},
	}
	item := watchlistItemFrom(ev)
	if item.Address != "Hauptstr. 1" || item.Town != "Bonn" {
		t.Errorf("address=%q town=%q", item.Address, item.Town)
	}
	if item.Lat == nil || *item.Lat != lat || item.Lon == nil || *item.Lon != lon {
		t.Errorf("lat=%v lon=%v, want %v and %v", item.Lat, item.Lon, lat, lon)
	}

	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["lat"] != lat || m["lon"] != lon {
		t.Errorf("JSON lat/lon = %v/%v", m["lat"], m["lon"])
	}
}

func TestWatchlistItemWithoutCoordinatesOmitsThem(t *testing.T) {
	ev := Event{ID: 8, Title: "Workshop", StartTime: "2026-11-03T10:00:00+01:00", Location: &Location{Location: "Hof"}}
	raw, _ := json.Marshal(watchlistItemFrom(ev))
	var m map[string]any
	json.Unmarshal(raw, &m)
	if _, ok := m["lat"]; ok {
		t.Error("lat must be omitted for a venue without coordinates")
	}
	if _, ok := m["lon"]; ok {
		t.Error("lon must be omitted for a venue without coordinates")
	}
}
