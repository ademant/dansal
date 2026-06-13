package main

import (
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// safeReturnPath validates that raw is a same-site relative path suitable for
// redirecting back to after an admin edit (e.g. the page the user came from).
// Returns "" if raw is empty or not a safe same-site path.
func safeReturnPath(raw string) string {
	if raw == "" || raw[0] != '/' || strings.HasPrefix(raw, "//") || strings.Contains(raw, "://") {
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

// splitEventLocations divides all locations into two groups for the event-edit
// location dropdown: org-first (same org as the event's pre-assigned location)
// and others. Within each group locations are sorted by distance from the
// pre-assigned location (when it has coordinates), then by name.
func splitEventLocations(allLocs []Location, event Event) (orgFirst, others []Location) {
	if event.LocationID == nil {
		others = append([]Location(nil), allLocs...)
		sortLocsByDistanceThenName(others, nil, nil)
		return
	}
	var refLat, refLng *float64
	var evOrgIDs map[int]bool
	for _, loc := range allLocs {
		if loc.ID == *event.LocationID {
			refLat = loc.Latitude
			refLng = loc.Longitude
			if len(loc.OrganizationIDs) > 0 {
				evOrgIDs = make(map[int]bool, len(loc.OrganizationIDs))
				for _, oid := range loc.OrganizationIDs {
					evOrgIDs[oid] = true
				}
			}
			break
		}
	}
	if len(evOrgIDs) == 0 {
		others = append([]Location(nil), allLocs...)
		sortLocsByDistanceThenName(others, refLat, refLng)
		return
	}
	for _, loc := range allLocs {
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

var locAttrKeys = []string{"wheelchair", "hearing_loop", "visual_support", "bar", "kitchen"}

func locationAttrsFromForm(r *http.Request) map[string]bool {
	attrs := map[string]bool{}
	for _, k := range locAttrKeys {
		if r.FormValue("attr_"+k) == "1" {
			attrs[k] = true
		}
	}
	if len(attrs) == 0 {
		return nil
	}
	return attrs
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
