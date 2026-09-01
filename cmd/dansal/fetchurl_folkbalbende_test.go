package main

import (
	"testing"
	"time"
)

// sample is a trimmed live response captured from
// GET https://folkbalbende.be/interface/spider_events.php (#1220), edited
// only to move the date into the future so the "already ended" filter
// doesn't skip it, and to keep the fixture small.
const folkbalbendeSample = `[
  {
    "id": 4953,
    "name": "Practica in Open Lucht",
    "recurrence": 0,
    "type": "ball",
    "cancelled": 0,
    "deleted": 0,
    "checked": 1,
    "dates": ["2099-09-01"],
    "location": {
      "id": 2,
      "name": "Esplanade Parlement Européen",
      "address": {
        "id": 2,
        "street": "Place",
        "number": "Luxembourg",
        "zip-city": "1040 - Etterbeek",
        "city": "Etterbeek",
        "region": "Brussel",
        "zip": "1040",
        "lat": 50.839,
        "lng": 4.37258
      },
      "duplicate_of": null
    },
    "prices": [],
    "reservation_type": 2,
    "reservation_url": "https://frissefolk.be/nl/event-nl/Practica/1456/",
    "websites": [{"id": 5370, "type": "websites", "url": "https://frissefolk.be/nl/event-nl/Practica/1456/"}],
    "facebook_event": "https://www.facebook.com/events/1531755915209021",
    "nl": "Folkpractica in open lucht!",
    "fr": "Practica folk en plein air",
    "en": "Open air folk practica",
    "tags": [],
    "hidden": 0,
    "organisation": {"id": 78, "name": "Frisse Folk Vzw/asbl"},
    "ball": {
      "initiation_start": "19:30:00",
      "initiation_end": "22:00:00",
      "initiators": [],
      "performances": []
    },
    "spider": 0
  },
  {
    "id": 9999,
    "name": "Deleted Ghost Event",
    "type": "ball",
    "cancelled": 0,
    "deleted": 1,
    "hidden": 0,
    "dates": ["2099-09-01"],
    "location": null
  },
  {
    "id": 9998,
    "name": "Past Event",
    "type": "ball",
    "cancelled": 0,
    "deleted": 0,
    "hidden": 0,
    "dates": ["2000-01-01"],
    "location": null,
    "ball": {"initiation_start": "19:00:00", "initiation_end": "22:00:00"}
  }
]`

func TestParseFolkbalbendeJSONToRequests(t *testing.T) {
	src := FetchSource{URL: "https://folkbalbende.be/interface/spider_events.php"}
	reqs, err := parseFolkbalbendeJSONToRequests([]byte(folkbalbendeSample), src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The deleted event and the already-ended event must both be skipped,
	// leaving only the one live ball.
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1: %+v", len(reqs), reqs)
	}
	req := reqs[0]

	if req.UID != "folkbalbende:4953" {
		t.Errorf("UID = %q, want folkbalbende:4953", req.UID)
	}
	if req.Title != "Practica in Open Lucht" {
		t.Errorf("Title = %q", req.Title)
	}
	if req.Description != "Folkpractica in open lucht!" {
		t.Errorf("Description = %q, want the nl text (first non-empty of nl/fr/en)", req.Description)
	}
	wantStart := "2099-09-01T19:30:00Z"
	if got, _ := time.Parse(time.RFC3339, req.StartTime); got.Format(time.RFC3339) != wantStart {
		t.Errorf("StartTime = %q, want %q", req.StartTime, wantStart)
	}
	wantEnd := "2099-09-01T22:00:00Z"
	if got, _ := time.Parse(time.RFC3339, req.EndTime); got.Format(time.RFC3339) != wantEnd {
		t.Errorf("EndTime = %q, want %q", req.EndTime, wantEnd)
	}
	if req.URL != "https://frissefolk.be/nl/event-nl/Practica/1456/" {
		t.Errorf("URL = %q, want the reservation_url", req.URL)
	}
	if len(req.Tags) != 1 || req.Tags[0] != "bal-folk" {
		t.Errorf("Tags = %v, want [bal-folk]", req.Tags)
	}
	if req.Location.Location != "Esplanade Parlement Européen" {
		t.Errorf("Location.Location = %q", req.Location.Location)
	}
	if req.Location.Town != "Etterbeek" {
		t.Errorf("Location.Town = %q", req.Location.Town)
	}
	if req.Location.Latitude == nil || *req.Location.Latitude != 50.839 {
		t.Errorf("Location.Latitude = %v, want 50.839", req.Location.Latitude)
	}
	if req.Location.Longitude == nil || *req.Location.Longitude != 4.37258 {
		t.Errorf("Location.Longitude = %v, want 4.37258", req.Location.Longitude)
	}
}

func TestFolkbalbendeJSONProbe(t *testing.T) {
	if !folkbalbendeJSONProbe("https://folkbalbende.be/interface/spider_events.php") {
		t.Error("expected match on folkbalbende.be URL")
	}
	if folkbalbendeJSONProbe("https://folkdance.page/some.json") {
		t.Error("unexpected match on unrelated URL")
	}
}

func TestFlexBoolUnmarshal(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true}, {"false", false},
		{"1", true}, {"0", false},
		{"null", false},
	}
	for _, c := range cases {
		var b flexBool
		if err := b.UnmarshalJSON([]byte(c.in)); err != nil {
			t.Errorf("UnmarshalJSON(%q): %v", c.in, err)
			continue
		}
		if bool(b) != c.want {
			t.Errorf("UnmarshalJSON(%q) = %v, want %v", c.in, b, c.want)
		}
	}
}
