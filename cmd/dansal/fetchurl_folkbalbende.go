package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// folkbalbende.be is a Belgian balfolk/trad event database exposing a public,
// documented, CORS-enabled JSON API explicitly built for external
// aggregators (GET /interface/spider_events.php — "Bulk event export for
// scrapers"). See #1220 for the investigation. This parser plugs into the
// same dispatch used by gancio/TEC/folkdance.page (parseBodyToRequests in
// preview.go, detectFetchType in fetchurl_folkdance.go), so it's usable both
// as a real import source and — the reason it was written — via the preview
// endpoint for #1220's cached-not-imported map overlay.

// flexBool accepts a JSON bool or a 0/1 integer for the same field.
// folkbalbende.be's endpoints are inconsistent about which they use: a live
// sample of GET /interface/events.php returned real JSON booleans for
// cancelled/deleted/checked/hidden, while GET /interface/spider_events.php
// returned 0/1 integers for the same fields on the same event shape.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	switch strings.TrimSpace(string(data)) {
	case "true", "1":
		*b = true
	case "false", "0", "null":
		*b = false
	default:
		return fmt.Errorf("flexBool: unexpected value %s", data)
	}
	return nil
}

type folkbalbendeAddress struct {
	Street string  `json:"street"`
	Number string  `json:"number"`
	City   string  `json:"city"`
	Zip    string  `json:"zip"`
	Lat    float64 `json:"lat"`
	Lng    float64 `json:"lng"`
}

type folkbalbendeLocation struct {
	Name    string               `json:"name"`
	Address *folkbalbendeAddress `json:"address"`
}

type folkbalbendeWebsite struct {
	URL string `json:"url"`
}

// folkbalbendeBall is the only per-type sub-object verified live (every
// sample event returned by the API so far has type "ball"). "course" and
// "festival" events likely carry their own differently-shaped sub-objects,
// but without a live sample of either this parser doesn't guess at their
// fields — those types fall back to the documented default time-of-day
// below rather than risk silently misreading an unverified schema.
type folkbalbendeBall struct {
	InitiationStart string `json:"initiation_start"`
	InitiationEnd   string `json:"initiation_end"`
}

type folkbalbendeEvent struct {
	ID             int                   `json:"id"`
	Name           string                `json:"name"`
	Type           string                `json:"type"` // "ball" | "course" | "festival"
	Cancelled      flexBool              `json:"cancelled"`
	Deleted        flexBool              `json:"deleted"`
	Hidden         flexBool              `json:"hidden"`
	Dates          []string              `json:"dates"` // "YYYY-MM-DD"; only the first is used
	Location       *folkbalbendeLocation `json:"location"`
	ReservationURL string                `json:"reservation_url"`
	Websites       []folkbalbendeWebsite `json:"websites"`
	FacebookEvent  string                `json:"facebook_event"`
	NL             string                `json:"nl"`
	FR             string                `json:"fr"`
	EN             string                `json:"en"`
	Ball           *folkbalbendeBall     `json:"ball"`
}

// folkbalbendeEventTypeTags maps folkbalbende.be's event "type" enum to
// dansal's shipped default balfolk tag vocabulary (see CLAUDE.md's Tags
// vocabulary section). folkbalbende is a Belgium-specific balfolk
// aggregator, only ever meaningfully configured on instances using that
// default vocabulary — a custom instance with its own tags.yaml would need
// its own mapping if it ever wanted this source, which is out of scope here.
var folkbalbendeEventTypeTags = map[string]string{
	"ball":     "bal-folk",
	"course":   "workshop",
	"festival": "festival",
}

// parseFolkbalbendeClock parses a "HH:MM" or "HH:MM:SS" wall-clock string
// into hour/minute, or returns the given defaults when s is empty or
// unparseable.
func parseFolkbalbendeClock(s string, defHour, defMin int) (int, int) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) < 2 {
		return defHour, defMin
	}
	h, errH := strconv.Atoi(parts[0])
	m, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return defHour, defMin
	}
	return h, m
}

// folkbalbendeEventToRequest converts a single folkbalbende.be event to an
// EventCreateRequest, skipping deleted/hidden events, events with no
// parseable date, and events that have already ended.
func folkbalbendeEventToRequest(fe folkbalbendeEvent, src FetchSource) (EventCreateRequest, bool) {
	if fe.Name == "" || bool(fe.Deleted) || bool(fe.Hidden) || len(fe.Dates) == 0 {
		return EventCreateRequest{}, false
	}
	day, err := time.Parse("2006-01-02", fe.Dates[0])
	if err != nil {
		return EventCreateRequest{}, false
	}

	// Wall-clock start/end: only "ball" events have a verified sub-object
	// (see folkbalbendeBall's doc comment). Default to a typical bal-folk
	// evening window for anything else.
	startH, startM := 20, 0
	endH, endM := 23, 0
	if fe.Ball != nil {
		startH, startM = parseFolkbalbendeClock(fe.Ball.InitiationStart, startH, startM)
		endH, endM = parseFolkbalbendeClock(fe.Ball.InitiationEnd, endH, endM)
	}
	startTime := time.Date(day.Year(), day.Month(), day.Day(), startH, startM, 0, 0, time.UTC)
	endTime := time.Date(day.Year(), day.Month(), day.Day(), endH, endM, 0, 0, time.UTC)
	if !endTime.After(startTime) {
		// Matches the RSS importer's own fallback idiom (rssEventDates):
		// a documented default duration rather than an inverted/zero range.
		endTime = startTime.Add(3 * time.Hour)
	}
	if endTime.Before(time.Now().UTC()) {
		return EventCreateRequest{}, false
	}

	eventURL := firstSet(fe.ReservationURL)
	if eventURL == "" {
		for _, w := range fe.Websites {
			if w.URL != "" {
				eventURL = w.URL
				break
			}
		}
	}
	if eventURL == "" {
		eventURL = fe.FacebookEvent
	}

	loc := EventLocationRequest{}
	if fe.Location != nil {
		loc.Location = fe.Location.Name
		if a := fe.Location.Address; a != nil {
			loc.Address = strings.TrimSpace(a.Street + " " + a.Number)
			loc.Town = a.City
			loc.Zipcode = a.Zip
			loc.Country = "Belgium"
			if a.Lat != 0 || a.Lng != 0 {
				lat, lng := a.Lat, a.Lng
				loc.Latitude = &lat
				loc.Longitude = &lng
			}
		}
	}

	var tags []string
	if slug, ok := folkbalbendeEventTypeTags[fe.Type]; ok {
		tags = []string{slug}
	}
	tags = mergeTags(tags, src.Tags)

	return EventCreateRequest{
		UID:           fmt.Sprintf("folkbalbende:%d", fe.ID),
		Source:        src.URL,
		FetchSourceID: src.ID,
		EventWriteRequest: EventWriteRequest{
			Title:          fe.Name,
			Description:    firstSet(fe.NL, fe.FR, fe.EN),
			StartTime:      startTime.Format(time.RFC3339),
			EndTime:        endTime.Format(time.RFC3339),
			IsCancelled:    bool(fe.Cancelled),
			Tags:           tags,
			URL:            eventURL,
			OrganizationID: src.OrganizationID,
			Dances:         src.DanceIDs,
			Location:       loc,
		},
	}, true
}

// parseFolkbalbendeJSONToRequests converts a folkbalbende.be JSON body (a
// bare array of events, as returned by both events.php and
// spider_events.php) into EventCreateRequests without touching the
// database. Used by the preview endpoint.
func parseFolkbalbendeJSONToRequests(body []byte, src FetchSource) ([]EventCreateRequest, error) {
	var events []folkbalbendeEvent
	if err := json.Unmarshal(body, &events); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	var reqs []EventCreateRequest
	for _, fe := range events {
		if req, ok := folkbalbendeEventToRequest(fe, src); ok {
			reqs = append(reqs, req)
		}
	}
	return reqs, nil
}

// folkbalbendeJSONProbe returns true when the URL looks like a
// folkbalbende.be event feed (its JSON API, e.g. spider_events.php or
// events.php).
func folkbalbendeJSONProbe(rawURL string) bool {
	return strings.Contains(rawURL, "folkbalbende.be")
}
