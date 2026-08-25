package main

import "testing"

func TestOpenActiveEventTypePriority(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{"festival wins over workshop", []string{"workshop", "festival"}, "Festival"},
		{"workshop over concert", []string{"concert", "dance-workshop"}, "EducationEvent"},
		{"concert alone", []string{"concert"}, "MusicEvent"},
		{"bal-folk", []string{"bal-folk"}, "DanceEvent"},
		{"fest-noz", []string{"fest-noz"}, "DanceEvent"},
		{"unknown tags fall back", []string{"open-air"}, "Event"},
		{"no tags", nil, "Event"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := openActiveEventType(c.tags); got != c.want {
				t.Errorf("openActiveEventType(%v) = %q, want %q", c.tags, got, c.want)
			}
		})
	}
}

func TestBuildOpenActiveEventCore(t *testing.T) {
	lat, lon := 51.2562, 7.1508
	ticketsTotal := 42
	event := Event{
		ID:           864,
		Title:        "Bal Luminescence",
		Description:  "An evening of balfolk.",
		StartTime:    "2026-09-25T19:00:00Z",
		EndTime:      "2026-09-25T23:30:00Z",
		Tags:         []string{"bal-folk", "workshop"},
		TicketsTotal: ticketsTotal,
		Location: &Location{
			Location: "Färberei",
			Address:  "Peter-Hansen-Platz 1",
			Town:     "Wuppertal",
			Zipcode:  "42275",
			Country:  "Germany",
			Latitude: &lat, Longitude: &lon,
		},
		Pricing: &Pricing{Type: "single", Amount: 12.5, Currency: "EUR"},
	}

	out := buildOpenActiveEvent(event, "https://balfolk.jetzt")

	if out["@type"] != "EducationEvent" { // workshop tag wins priority over bal-folk
		t.Errorf("@type = %v", out["@type"])
	}
	if out["name"] != "Bal Luminescence" {
		t.Errorf("name = %v", out["name"])
	}
	if out["@id"] != "https://balfolk.jetzt/events/864" {
		t.Errorf("@id = %v", out["@id"])
	}
	if out["url"] != "https://balfolk.jetzt/events/864" {
		t.Errorf("url fallback = %v", out["url"])
	}
	if out["eventStatus"] != "https://schema.org/EventScheduled" {
		t.Errorf("eventStatus = %v", out["eventStatus"])
	}
	if out["maximumAttendeeCapacity"] != ticketsTotal {
		t.Errorf("maximumAttendeeCapacity = %v", out["maximumAttendeeCapacity"])
	}

	place, ok := out["location"].(map[string]any)
	if !ok {
		t.Fatalf("location missing or wrong type: %v", out["location"])
	}
	if place["name"] != "Färberei" {
		t.Errorf("location.name = %v", place["name"])
	}
	addr, ok := place["address"].(map[string]any)
	if !ok || addr["addressLocality"] != "Wuppertal" {
		t.Errorf("location.address = %v", place["address"])
	}
	geo, ok := place["geo"].(map[string]any)
	if !ok || geo["latitude"] != lat || geo["longitude"] != lon {
		t.Errorf("location.geo = %v", place["geo"])
	}

	offers, ok := out["offers"].([]map[string]any)
	if !ok || len(offers) != 1 || offers[0]["price"] != 12.5 || offers[0]["priceCurrency"] != "EUR" {
		t.Errorf("offers = %v", out["offers"])
	}

	activities, ok := out["activity"].([]map[string]any)
	if !ok || len(activities) == 0 {
		t.Fatalf("activity missing: %v", out["activity"])
	}
	found := false
	for _, a := range activities {
		if a["prefLabel"] == "Dance Class" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a \"Dance Class\" activity concept, got %v", activities)
	}
}

func TestBuildOpenActiveEventCancelledAndMinimal(t *testing.T) {
	event := Event{ID: 1, Title: "Cancelled thing", IsCancelled: true}
	out := buildOpenActiveEvent(event, "https://balfolk.jetzt")
	if out["eventStatus"] != "https://schema.org/EventCancelled" {
		t.Errorf("eventStatus = %v", out["eventStatus"])
	}
	if _, present := out["location"]; present {
		t.Errorf("location should be absent for a nil Location, got %v", out["location"])
	}
	if _, present := out["offers"]; present {
		t.Errorf("offers should be absent for nil Pricing, got %v", out["offers"])
	}
	if _, present := out["activity"]; present {
		t.Errorf("activity should be absent with no tags, got %v", out["activity"])
	}
}

func TestBuildOpenActiveOffersPricingTypes(t *testing.T) {
	cases := []struct {
		name    string
		pricing *Pricing
		want    int // expected offer count
	}{
		{"nil pricing", nil, 0},
		{"free", &Pricing{Type: "free"}, 1},
		{"single", &Pricing{Type: "single", Amount: 8}, 1},
		{"multiple", &Pricing{Type: "multiple", Prices: []Price{{Label: "Standard", Amount: 10}, {Label: "Reduced", Amount: 5}}}, 2},
		{"donation", &Pricing{Type: "donation"}, 1},
		{"unknown type", &Pricing{Type: "bogus"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildOpenActiveOffers(c.pricing)
			if len(got) != c.want {
				t.Errorf("buildOpenActiveOffers(%+v) returned %d offers, want %d", c.pricing, len(got), c.want)
			}
		})
	}
}
