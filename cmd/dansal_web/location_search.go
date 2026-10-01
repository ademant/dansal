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

// matchLocations returns the top-level venues whose name, short name,
// aliases or town contain every word of q (diacritics-insensitive). Venues
// matching on their own names rank before town-only matches. Rooms are
// skipped: a suggester picks the building.
func matchLocations(q string, locs []Location) []LocationHit {
	qw := foldWords(q)
	if len(strings.Join(qw, "")) < 2 {
		return nil
	}
	type scored struct {
		hit   LocationHit
		score int
	}
	var out []scored
	for _, l := range locs {
		if l.ParentID != nil || l.Location == "" {
			continue
		}
		names := strings.Join(foldWords(strings.Join(append([]string{l.Location, l.ShortName}, l.Aliases...), " ")), " ")
		all := names + " " + strings.Join(foldWords(l.Town), " ")
		inNames, inAll := true, true
		for _, w := range qw {
			if !strings.Contains(names, w) {
				inNames = false
			}
			if !strings.Contains(all, w) {
				inAll = false
			}
		}
		if !inAll {
			continue
		}
		score := 1
		if inNames {
			score = 0
		}
		label := cmp.Or(l.ShortName, l.Location)
		if l.Town != "" {
			label += ", " + l.Town
		}
		out = append(out, scored{LocationHit{
			ID: l.ID, Name: l.Location, Label: label, Address: l.Address, Zipcode: l.Zipcode,
			Town: l.Town, Country: l.Country, OsmID: l.OsmID, OsmType: l.OsmType,
			HasCoord: l.Latitude != nil && l.Longitude != nil,
		}, score})
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
