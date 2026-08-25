package main

import (
	"fmt"
	"slices"
)

// OpenActive-profiled schema.org JSON-LD alternate event representation
// (#1154), triggered by ?format=openactive on GET /api/v1/events and
// GET /api/v1/events/{id}.
//
// A query param rather than Accept-header negotiation (unlike the
// text/calendar and application/calendar+json branches) because OpenActive
// doesn't register a distinct media type for a single event's JSON-LD the
// way jCal does — it's plain application/json with an @context, normally
// delivered as an item inside their RPDE feed envelope. See #1154 for that
// reasoning.
//
// This is deliberately NOT an RPDE (Realtime Paged Data Exchange) feed —
// no cursor, no change tracking, no deletion tombstones (deleteEvent
// still hard-deletes). That protocol was explicitly scoped out of #1154
// pending an actual OpenActive consumer; this endpoint is just the
// OpenActive/schema.org *event shape*, for direct GET consumption, not a
// claim of full OpenActive Open Data compliance.

// openActiveActivityLabels gives a best-effort, human-readable OpenActive
// Activity Concept label for each dansal tag that has one. Deliberately
// omits the Activity List's "id" URI (https://openactive.io/activity-list/)
// since dansal has no verified mapping to that registry's specific
// entries — wiring up real ids is follow-up work for whoever integrates
// against a specific OpenActive consumer, not guessed here.
var openActiveActivityLabels = map[string]string{
	"bal-folk":          "Folk Dancing",
	"fest-noz":          "Folk Dancing",
	"session":           "Music Session",
	"concert":           "Music",
	"workshop":          "Dance Class",
	"dance-workshop":    "Dance Class",
	"musician-workshop": "Music Lesson",
	"music-course":      "Music Lesson",
}

// buildOpenActiveEvent converts a dansal Event into an OpenActive-profiled
// schema.org Event JSON-LD object. Returned as a map (json.Marshal-ready)
// rather than a struct since most fields are conditionally present —
// schema.org JSON-LD is naturally a sparse, optional-field shape.
func buildOpenActiveEvent(event Event, publicBase string) map[string]any {
	id := fmt.Sprintf("%s/events/%d", publicBase, event.ID)
	out := map[string]any{
		"@context":    []string{"https://openactive.io/", "https://schema.org"},
		"@type":       openActiveEventType(event.Tags),
		"@id":         id,
		"name":        event.Title,
		"eventStatus": "https://schema.org/EventScheduled",
	}
	if event.IsCancelled {
		out["eventStatus"] = "https://schema.org/EventCancelled"
	}
	if event.Description != "" {
		out["description"] = event.Description
	}
	if event.StartTime != "" {
		out["startDate"] = event.StartTime
	}
	if event.EndTime != "" {
		out["endDate"] = event.EndTime
	}
	if event.URL != "" {
		out["url"] = event.URL
	} else {
		out["url"] = id
	}
	if place := buildOpenActivePlace(event.Location); place != nil {
		out["location"] = place
	}
	if offers := buildOpenActiveOffers(event.Pricing); len(offers) > 0 {
		out["offers"] = offers
	}
	if event.TicketsTotal > 0 {
		out["maximumAttendeeCapacity"] = event.TicketsTotal
	}
	if activities := buildOpenActiveActivities(event.Tags); len(activities) > 0 {
		out["activity"] = activities
	}
	return out
}

// openActiveEventType picks the most specific schema.org Event subtype
// dansal's tags support. Priority reflects which tag combination is most
// defining when an event carries several (e.g. a festival that also has a
// workshop track is still, first and foremost, a Festival).
func openActiveEventType(tags []string) string {
	switch {
	case slices.Contains(tags, "festival"):
		return "Festival"
	case slices.Contains(tags, "workshop") || slices.Contains(tags, "dance-workshop") ||
		slices.Contains(tags, "musician-workshop") || slices.Contains(tags, "music-course"):
		return "EducationEvent"
	case slices.Contains(tags, "concert"):
		return "MusicEvent"
	case slices.Contains(tags, "bal-folk") || slices.Contains(tags, "fest-noz") || slices.Contains(tags, "session"):
		return "DanceEvent"
	default:
		return "Event"
	}
}

func buildOpenActivePlace(loc *Location) map[string]any {
	if loc == nil || loc.Location == "" {
		return nil
	}
	place := map[string]any{
		"@type": "Place",
		"name":  loc.Location,
	}
	if loc.Address != "" || loc.Town != "" || loc.Zipcode != "" || loc.Country != "" {
		addr := map[string]any{"@type": "PostalAddress"}
		if loc.Address != "" {
			addr["streetAddress"] = loc.Address
		}
		if loc.Town != "" {
			addr["addressLocality"] = loc.Town
		}
		if loc.Zipcode != "" {
			addr["postalCode"] = loc.Zipcode
		}
		if loc.Country != "" {
			addr["addressCountry"] = loc.Country
		}
		place["address"] = addr
	}
	if loc.Latitude != nil && loc.Longitude != nil {
		place["geo"] = map[string]any{
			"@type":     "GeoCoordinates",
			"latitude":  *loc.Latitude,
			"longitude": *loc.Longitude,
		}
	}
	return place
}

// buildOpenActiveOffers maps dansal's Pricing (type: free/donation/single/
// multiple) onto an array of schema.org Offer objects.
func buildOpenActiveOffers(p *Pricing) []map[string]any {
	if p == nil {
		return nil
	}
	newOffer := func(name string, price float64) map[string]any {
		offer := map[string]any{
			"@type":        "Offer",
			"price":        price,
			"availability": "https://schema.org/InStock",
		}
		if name != "" {
			offer["name"] = name
		}
		if p.Currency != "" {
			offer["priceCurrency"] = p.Currency
		}
		return offer
	}
	switch p.Type {
	case "free":
		return []map[string]any{newOffer("", 0)}
	case "single":
		return []map[string]any{newOffer("", p.Amount)}
	case "multiple":
		offers := make([]map[string]any, 0, len(p.Prices))
		for _, price := range p.Prices {
			offers = append(offers, newOffer(price.Label, price.Amount))
		}
		return offers
	case "donation":
		return []map[string]any{{
			"@type":        "Offer",
			"description":  "donation-based",
			"availability": "https://schema.org/InStock",
		}}
	default:
		return nil
	}
}

func buildOpenActiveActivities(tags []string) []map[string]any {
	seen := map[string]bool{}
	var out []map[string]any
	for _, tag := range tags {
		label, ok := openActiveActivityLabels[tag]
		if !ok || seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, map[string]any{"type": "Concept", "prefLabel": label})
	}
	return out
}
