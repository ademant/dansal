package main

// JSON-LD event-page import (#1376): accept a schema.org/Event page URL
// (submitted through the public feed-suggestion form, where a human already
// does the discovery a generic crawler would otherwise need) rather than a
// classic iCal/JSON feed.
//
// dansal already *emits* schema.org/Event JSON-LD (openactive.go, and the
// ld+json blocks in the web templates), so a dansal event page is valid
// input here with no special casing.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// errNoMachineReadableEvents distinguishes "the page has no schema.org Event
// node at all" from "events were found but none survived filtering" (an
// empty-but-successful parse) — the suggest handlers surface it as a
// specific, translated error instead of the generic "no events found" one.
var errNoMachineReadableEvents = fmt.Errorf("no machine-readable events")

// jsonldEventTypes are schema.org's Event subclasses
// (https://schema.org/Event) — a page describing a concert or a festival
// should be accepted exactly like a bare Event.
var jsonldEventTypes = map[string]bool{
	"Event": true, "MusicEvent": true, "Festival": true, "SocialEvent": true,
	"BusinessEvent": true, "ExhibitionEvent": true, "ScreeningEvent": true,
	"EducationEvent": true, "PublicationEvent": true, "SaleEvent": true,
}

// extractJSONLDBlocks walks the HTML document with golang.org/x/net/html
// (already an indirect module dependency) and returns the text content of
// every <script type="application/ld+json"> element, in document order.
// Deliberately not a regex: nested quotes, escaped entities and CDATA inside
// ld+json are exactly what tecStripHTML's regex approach gets wrong.
func extractJSONLDBlocks(body []byte) []string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	var blocks []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" {
			for _, a := range n.Attr {
				if a.Key == "type" && strings.EqualFold(strings.TrimSpace(a.Val), "application/ld+json") {
					if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
						blocks = append(blocks, n.FirstChild.Data)
					}
					break
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return blocks
}

// jsonldEventNodes walks a decoded JSON-LD document for Event nodes (and its
// accepted subclasses), descending through the two wrapper shapes real event
// pages use: @graph (a flat list of nodes sharing one @context) and
// ItemList.itemListElement (a listing page, each entry usually a ListItem
// wrapping the real node in "item"). An Event node is a leaf — its own
// nested structure (offers, location, ...) is read by the caller, not walked
// for further Event nodes.
func jsonldEventNodes(doc any) []map[string]any {
	var out []map[string]any
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			if isJSONLDEventType(t["@type"]) {
				out = append(out, t)
				return
			}
			if graph, ok := t["@graph"]; ok {
				walk(graph)
			}
			if items, ok := t["itemListElement"]; ok {
				walk(items)
			}
			if item, ok := t["item"]; ok {
				walk(item)
			}
		}
	}
	walk(doc)
	return out
}

func isJSONLDEventType(t any) bool {
	switch v := t.(type) {
	case string:
		return jsonldEventTypes[v]
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && jsonldEventTypes[s] {
				return true
			}
		}
	}
	return false
}

// jsonldString reads a string field that is sometimes a plain string and
// sometimes an expanded {"@value": "..."} form.
func jsonldString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if s, ok := t["@value"].(string); ok {
			return s
		}
	}
	return ""
}

// jsonldFloat reads a float from a JSON number or a numeric string —
// GeoCoordinates latitude/longitude are sometimes serialized as strings.
func jsonldFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	}
	return 0, false
}

// jsonldLocation maps a schema.org Place (or, rarely, a bare string) to an
// EventLocationRequest: name, PostalAddress fields, and GeoCoordinates.
func jsonldLocation(v any) EventLocationRequest {
	var loc EventLocationRequest
	switch t := v.(type) {
	case string:
		loc.Location = t
	case map[string]any:
		loc.Location = jsonldString(t["name"])
		switch a := t["address"].(type) {
		case string:
			if loc.Location == "" {
				loc.Location = a
			} else {
				loc.Address = a
			}
		case map[string]any:
			loc.Address = jsonldString(a["streetAddress"])
			loc.Town = jsonldString(a["addressLocality"])
			loc.Zipcode = jsonldString(a["postalCode"])
			loc.Country = jsonldString(a["addressCountry"])
			loc.Region = jsonldString(a["addressRegion"])
			if loc.Location == "" {
				// No venue name at all: the locality is still a better
				// location signal than leaving this event unresolvable.
				loc.Location = loc.Town
			}
		}
		if geo, ok := t["geo"].(map[string]any); ok {
			if lat, ok := jsonldFloat(geo["latitude"]); ok {
				loc.Latitude = &lat
			}
			if lon, ok := jsonldFloat(geo["longitude"]); ok {
				loc.Longitude = &lon
			}
		}
	}
	return loc
}

// jsonldPricing maps a schema.org Offer (or the first of an array of Offers)
// to a Pricing. Best-effort: a page with a complex multi-tier offers list
// just doesn't get pricing populated rather than guessing which tier to use.
func jsonldPricing(v any) *Pricing {
	var offer map[string]any
	switch t := v.(type) {
	case map[string]any:
		offer = t
	case []any:
		if len(t) == 0 {
			return nil
		}
		m, ok := t[0].(map[string]any)
		if !ok {
			return nil
		}
		offer = m
	default:
		return nil
	}
	var amount float64
	switch p := offer["price"].(type) {
	case float64:
		amount = p
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil
		}
		amount = f
	default:
		return nil
	}
	if amount == 0 {
		return &Pricing{Type: "free"}
	}
	return &Pricing{Type: "single", Amount: amount, Currency: jsonldString(offer["priceCurrency"])}
}

// jsonldParseTime parses a schema.org Date or DateTime value. dateOnly is
// true when the value carried no time component at all (a bare Date, e.g.
// "2026-10-03") — the caller must flag the resulting request rather than
// silently guessing midnight (#1376). A DateTime with an explicit offset or
// "Z" keeps its unambiguous absolute instant; one with neither (legal per
// schema.org, which has no TZID-equivalent) is anchored in the instance zone,
// the same treatment floating iCal DTSTART values get (#1391).
func jsonldParseTime(s string) (t time.Time, dateOnly bool, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z0700"} {
		if pt, err := time.Parse(layout, s); err == nil {
			return pt, false, true
		}
	}
	if pt, err := time.ParseInLocation("2006-01-02T15:04:05", s, instanceLoc()); err == nil {
		return pt, false, true
	}
	if d, err := time.ParseInLocation("2006-01-02", s, instanceLoc()); err == nil {
		return d, true, true
	}
	return time.Time{}, false, false
}

// jsonldFallbackSpan is used when an event has neither endDate nor duration —
// the same 2h default the RSS importer falls back to (rssEventDates).
const jsonldFallbackSpan = 2 * time.Hour

// parseJSONLDBody extracts EventCreateRequests from an HTML page's
// schema.org/JSON-LD Event markup. rep tracks date-only startDate/endDate
// fallbacks the same way the iCal path tracks TZID fallbacks, so a degraded
// parse is visible in the admin run summary instead of importing silently.
func parseJSONLDBody(body []byte, src FetchSource, rep *icalParseReport) ([]EventCreateRequest, error) {
	var nodes []map[string]any
	for _, block := range extractJSONLDBlocks(body) {
		var doc any
		if err := json.Unmarshal([]byte(block), &doc); err != nil {
			continue // one malformed <script> block must not sink the whole page
		}
		nodes = append(nodes, jsonldEventNodes(doc)...)
	}
	if len(nodes) == 0 {
		return nil, errNoMachineReadableEvents
	}

	now := time.Now().UTC()
	var reqs []EventCreateRequest
	for i, node := range nodes {
		title := jsonldString(node["name"])
		if title == "" {
			continue
		}
		label := fmt.Sprintf("%s#%d", src.URL, i)

		startT, startDateOnly, ok := jsonldParseTime(jsonldString(node["startDate"]))
		if !ok {
			rep.unparsed(label, fmt.Errorf("missing or unparseable startDate"))
			continue
		}

		var endT time.Time
		endDateOnly := false
		if endStr := jsonldString(node["endDate"]); endStr != "" {
			if et, eDateOnly, ok := jsonldParseTime(endStr); ok {
				endT, endDateOnly = et, eDateOnly
			}
		}
		if endT.IsZero() {
			if durStr := jsonldString(node["duration"]); durStr != "" {
				if d, err := parseICalDuration(durStr); err == nil {
					endT = startT.Add(d)
				}
			}
		}
		if endT.IsZero() {
			endT = startT.Add(jsonldFallbackSpan)
		}
		if endT.Before(now) {
			continue
		}

		usedFallback := startDateOnly || endDateOnly
		if usedFallback {
			rep.fallback(label, "")
		}

		reqs = append(reqs, EventCreateRequest{
			Source:           src.URL,
			FetchSourceID:    src.ID,
			TimezoneFallback: usedFallback,
			EventWriteRequest: EventWriteRequest{
				Title:          title,
				Description:    jsonldString(node["description"]),
				StartTime:      startT.In(instanceLoc()).Format(time.RFC3339),
				EndTime:        endT.In(instanceLoc()).Format(time.RFC3339),
				URL:            jsonldString(node["url"]),
				OrganizationID: src.OrganizationID,
				Dances:         src.DanceIDs,
				Location:       jsonldLocation(node["location"]),
				Pricing:        jsonldPricing(node["offers"]),
			},
		})
	}
	return reqs, nil
}

// markFetchSourceImportedOnce flags src as done: a jsonld source is a
// one-shot import of a single event page, not a subscription, so
// adminFetchAll's periodic refresh loop skips it after this — see
// FetchSource.ImportedOnce.
func markFetchSourceImportedOnce(id int) {
	if _, err := db.Exec("UPDATE fetch_sources SET imported_once=1 WHERE id=?", id); err != nil {
		log.Printf("jsonld: mark source %d imported_once: %v", id, err)
	}
}

// importFromJSONLDSource fetches a jsonld source's page and imports its
// event(s), reusing importParsedFeed's shared fetch/import-transaction
// skeleton (the same one RSS and gancio use) since a JSON-LD event page
// needs none of the iCal path's per-VEVENT enrichment (organizer-based org
// resolution, ATTACH image download).
func importFromJSONLDSource(ctx context.Context, src FetchSource) ([]Event, ImportCounts, error) {
	rep := &icalParseReport{Kind: "JSON-LD"}
	events, counts, err := importParsedFeed(ctx, src, func(body []byte) ([]EventCreateRequest, error) {
		return parseJSONLDBody(body, src, rep)
	})
	rep.fold(&counts)
	if err == nil {
		markFetchSourceImportedOnce(src.ID)
	}
	return events, counts, err
}
