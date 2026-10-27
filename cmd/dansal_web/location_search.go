package main

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
)

// #1414: venue picker for the public event-suggest form. Known venues are
// offered before OpenStreetMap results, so a suggester picks the stored venue
// (sent by its exact name + OSM id, which ensureLocation matches) instead of
// creating a duplicate — typically one named after its street address.

const locationSearchMax = 8

// LocationHit is one known venue offered by GET /search/locations.
type LocationHit struct {
	ID       int    `json:"id"`
	Name     string `json:"name"` // the stored locations.location — what the form submits
	Label    string `json:"label"`
	Address  string `json:"address,omitempty"`
	Zipcode  string `json:"zipcode,omitempty"`
	Town     string `json:"town,omitempty"`
	Country  string `json:"country,omitempty"`
	OsmID    *int64 `json:"osm_id,omitempty"`
	OsmType  string `json:"osm_type,omitempty"`
	HasCoord bool   `json:"has_coord"`
}

// matchLocations returns the top-level venues that match the query
// (diacritics-insensitive). A venue matches when its name, short name, aliases,
// town, street address or zipcode contain every word of q. Venues matching on
// their own names rank before town/address-only matches. Rooms are skipped: a
// suggester picks the building.
//
// #1679: when no venue matches every word, the query is retried loosely: a
// venue qualifies with at least two words and at least half of the query's
// words, so "name, town, street" still finds the venue when one word is
// unknown. Loose matches are offered only when no strict match exists, so the
// strict results are never diluted.
func matchLocations(q string, locs []Location) []LocationHit {
	qw := foldWords(q)
	if len(strings.Join(qw, "")) < 2 {
		return nil
	}
	type scored struct {
		hit   LocationHit
		score int
	}
	var strict, loose []scored
	for _, l := range locs {
		if l.ParentID != nil || l.Location == "" {
			continue
		}
		names := strings.Join(foldWords(strings.Join(append([]string{l.Location, l.ShortName}, l.Aliases...), " ")), " ")
		all := names + " " + strings.Join(foldWords(strings.Join([]string{l.Town, l.Address, l.Zipcode}, " ")), " ")
		inNames, inAll := true, true
		matched := 0
		for _, w := range qw {
			if strings.Contains(all, w) {
				matched++
			} else {
				inAll = false
			}
			if !strings.Contains(names, w) {
				inNames = false
			}
		}
		if !inAll && (matched < 2 || matched*2 < len(qw)) {
			continue
		}
		label := cmp.Or(l.ShortName, l.Location)
		if l.Town != "" {
			label += ", " + l.Town
		}
		hit := LocationHit{
			ID: l.ID, Name: l.Location, Label: label, Address: l.Address, Zipcode: l.Zipcode,
			Town: l.Town, Country: l.Country, OsmID: l.OsmID, OsmType: l.OsmType,
			HasCoord: l.Latitude != nil && l.Longitude != nil,
		}
		if inAll {
			score := 1
			if inNames {
				score = 0
			}
			strict = append(strict, scored{hit, score})
		} else {
			loose = append(loose, scored{hit, 2 + len(qw) - matched})
		}
	}
	out := strict
	if len(out) == 0 {
		out = loose
	}
	slices.SortStableFunc(out, func(a, b scored) int {
		return cmp.Or(cmp.Compare(a.score, b.score), strings.Compare(a.hit.Label, b.hit.Label))
	})
	// Venues with the same stored name and town (duplicate rows awaiting an
	// admin merge) can't be told apart — the form submits by name and
	// ensureLocation takes the first match anyway — so offer them once.
	hits := make([]LocationHit, 0, min(len(out), locationSearchMax))
	seen := map[string]bool{}
	for _, s := range out {
		if len(hits) == locationSearchMax {
			break
		}
		k := strings.ToLower(s.hit.Name) + "|" + strings.ToLower(s.hit.Town)
		if seen[k] {
			continue
		}
		seen[k] = true
		hits = append(hits, s.hit)
	}
	return hits
}

// locationSearchHandler serves GET /search/locations?q=… (#1414).
func locationSearchHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" || len(q) > 200 {
			writeJSONError(w, r, http.StatusBadRequest, "q parameter required")
			return
		}
		locs, err := client.GetLocations(r.Context())
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, "could not load locations")
			return
		}
		writeJSONResponse(w, http.StatusOK, matchLocations(q, locs))
	}
}
