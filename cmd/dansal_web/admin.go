package main

import (
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// safeReferer extracts the path+query from the Referer header and validates it
// as a same-site path. Returns fallback if the Referer is absent, points to a
// different host, or fails safeReturnPath validation.
func safeReferer(r *http.Request, fallback string) string {
	raw := r.Header.Get("Referer")
	if raw == "" {
		return fallback
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fallback
	}
	if u.Host != "" && u.Host != r.Host {
		return fallback
	}
	if p := safeReturnPath(u.RequestURI()); p != "" {
		return p
	}
	return fallback
}

// safeReturnPath validates that raw is a same-site relative path suitable for
// redirecting back to after an admin edit (e.g. the page the user came from).
// Returns "" if raw is empty or not a safe same-site path.
func safeReturnPath(raw string) string {
	if raw == "" || raw[0] != '/' {
		return ""
	}
	if strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/\\") {
		return ""
	}
	if strings.ContainsAny(raw, "\\\t\r\n") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" {
		return ""
	}
	return raw
}

// geoDistKm returns the haversine distance in km between two lat/lon points.
func geoDistKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// sortLocsByDistanceThenName sorts in-place: by distance from refLat/refLng (when
// both the reference and the candidate have coordinates), falling back to name.
func sortLocsByDistanceThenName(locs []Location, refLat, refLng *float64) {
	sort.SliceStable(locs, func(i, j int) bool {
		li, lj := locs[i], locs[j]
		if refLat != nil && refLng != nil {
			iHas := li.Latitude != nil && li.Longitude != nil
			jHas := lj.Latitude != nil && lj.Longitude != nil
			if iHas && jHas {
				di := geoDistKm(*refLat, *refLng, *li.Latitude, *li.Longitude)
				dj := geoDistKm(*refLat, *refLng, *lj.Latitude, *lj.Longitude)
				if math.Abs(di-dj) > 0.1 {
					return di < dj
				}
			}
		}
		ni := li.ShortName
		if ni == "" {
			ni = li.Location
		}
		nj := lj.ShortName
		if nj == "" {
			nj = lj.Location
		}
		return ni < nj
	})
}

// topLevelLocations filters out child locations (rooms) — a room is a
// normal Location with ParentID set (#687); venue pickers only ever offer
// the top-level building, with rooms surfaced via its .Children instead.
func topLevelLocations(locs []Location) []Location {
	out := make([]Location, 0, len(locs))
	for _, l := range locs {
		if l.ParentID == nil {
			out = append(out, l)
		}
	}
	return out
}

// rollUpChildEventCounts adds each room's event count onto its parent
// building's entry in counts (keyed by location ID), so a building's total
// reflects events held in any of its rooms too (#882) — counts otherwise
// only reflect exact location_id matches, undercounting any building with
// rooms since events assigned to a room carry the room's location_id, not
// the building's.
func rollUpChildEventCounts(locs []Location, counts map[int]int) {
	for _, loc := range locs {
		for _, child := range loc.Children {
			counts[loc.ID] += counts[child.ID]
		}
	}
}

// splitEventLocations divides top-level locations into two groups for the
// event-edit location dropdown: org-first (locations belonging to the
// event's organization, and/or sharing an org with the event's pre-assigned
// location) and others. Within each group locations are sorted by distance
// from the pre-assigned location (when it has coordinates), then by name.
// allLocs must be the full (unfiltered) list so a room's own org
// assignment can still be found even though rooms never appear as options.
func splitEventLocations(allLocs []Location, event Event) (orgFirst, others []Location) {
	var refLat, refLng *float64
	evOrgIDs := make(map[int]bool)
	if event.OrganizationID != nil {
		evOrgIDs[*event.OrganizationID] = true
	}
	if event.LocationID != nil {
		for _, loc := range allLocs {
			if loc.ID == *event.LocationID {
				refLat = loc.Latitude
				refLng = loc.Longitude
				for _, oid := range loc.OrganizationIDs {
					evOrgIDs[oid] = true
				}
				break
			}
		}
	}
	topLocs := topLevelLocations(allLocs)
	if len(evOrgIDs) == 0 {
		others = append([]Location(nil), topLocs...)
		sortLocsByDistanceThenName(others, refLat, refLng)
		return
	}
	for _, loc := range topLocs {
		inOrg := false
		for _, oid := range loc.OrganizationIDs {
			if evOrgIDs[oid] {
				inOrg = true
				break
			}
		}
		if inOrg {
			orgFirst = append(orgFirst, loc)
		} else {
			others = append(others, loc)
		}
	}
	sortLocsByDistanceThenName(orgFirst, refLat, refLng)
	sortLocsByDistanceThenName(others, refLat, refLng)
	return
}

var locAttrKeys = []string{"wheelchair", "hearing_loop", "visual_support", "bar", "kitchen", "toilet"}

// locationAttrsFromForm reads the tri-state (unset/yes/no) attribute radio
// groups on the location edit form (#1188). It shares eventAttrsFromForm's
// logic exactly -- both read "attr_"+key from the same locAttrKeys list and
// the same "", "1", "0" values -- so an unchecked/omitted radio stays
// genuinely "no info" rather than being read as "no", letting a location be
// e.g. wheelchair-friendly without that having been explicitly recorded.
func locationAttrsFromForm(r *http.Request) map[string]bool {
	return eventAttrsFromForm(r)
}

func eventAttrsFromForm(r *http.Request) map[string]bool {
	attrs := map[string]bool{}
	for _, k := range locAttrKeys {
		v := r.FormValue("attr_" + k)
		if v == "1" {
			attrs[k] = true
		} else if v == "0" {
			attrs[k] = false
		}
	}
	if len(attrs) == 0 {
		return nil
	}
	return attrs
}

func parseLatLng(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return &f
	}
	return nil
}

func parseOsmID(s string) *int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return &n
	}
	return nil
}

// requireLogin redirects to /login if no session user, returning false when redirect was sent.
func requireLogin(w http.ResponseWriter, r *http.Request) (*SessionUser, bool) {
	u := getSessionUser(r)
	if u == nil {
		next := r.URL.RequestURI()
		http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
		return nil, false
	}
	return u, true
}
