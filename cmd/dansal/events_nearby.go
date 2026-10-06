package main

import (
	"math"
	"net/http"
	"strconv"
	"time"
)

// nearbyCountRadiiKm are the radius options the nearby-counts endpoint
// reports (#1436) — must match the search page's own radius <select>
// options (cmd/dansal_web/templates/search.html, #sf-radius) so a "N events
// within X km" link always carries a radius the search page can preselect.
var nearbyCountRadiiKm = []int{10, 50, 100, 200, 500}

type nearbyCountsResponse struct {
	RadiiKm []int          `json:"radii_km"`
	Counts  map[string]int `json:"counts"`
}

// GET /api/v1/events/nearby-counts?lat=&lon=&from=&to=
//
// Counts published, non-cancelled events starting in [from,to) within each
// of nearbyCountRadiiKm, cumulatively (#1436) — used by the venue page to
// offer "no events here, but N within X km" instead of a dead end when a
// venue's season is over. One query (bbox pre-filter on the largest
// radius) + an exact haversine distance per row in Go, same bbox-then-
// exact-distance shape as applyEventFilters' own lat/lon/radius_km filter,
// so this endpoint's counts and that filter's results can't disagree.
//
// Always public/published-only regardless of caller — unlike getEvents,
// there's no authenticated "see my org's unpublished events" mode here.
func getEventsNearbyCounts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, latErr := strconv.ParseFloat(q.Get("lat"), 64)
	lon, lonErr := strconv.ParseFloat(q.Get("lon"), 64)
	if latErr != nil || lonErr != nil {
		writeError(w, "lat and lon are required decimal degrees", http.StatusBadRequest)
		return
	}
	from, fromErr := time.ParseInLocation("2006-01-02", q.Get("from"), instanceTimezone)
	to, toErr := time.ParseInLocation("2006-01-02", q.Get("to"), instanceTimezone)
	if fromErr != nil || toErr != nil || to.Before(from) {
		writeError(w, "from and to are required ISO dates (YYYY-MM-DD) with to >= from", http.StatusBadRequest)
		return
	}
	fromUnix := from.Unix()
	toUnix := to.AddDate(0, 0, 1).Unix() // end of the "to" day, exclusive

	maxRadius := float64(nearbyCountRadiiKm[len(nearbyCountRadiiKm)-1])
	latDelta := maxRadius / 111.0
	lonDelta := maxRadius / (111.0 * math.Cos(lat*math.Pi/180))

	rows, err := db.Query(
		`SELECT l.latitude, l.longitude FROM events e JOIN locations l ON e.location_id = l.id
		 WHERE e.is_published = 1 AND e.is_cancelled = 0
		   AND e.start_time >= ? AND e.start_time < ?
		   AND l.latitude BETWEEN ? AND ? AND l.longitude BETWEEN ? AND ?`,
		fromUnix, toUnix, lat-latDelta, lat+latDelta, lon-lonDelta, lon+lonDelta,
	)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()

	counts := make(map[string]int, len(nearbyCountRadiiKm))
	for _, radius := range nearbyCountRadiiKm {
		counts[strconv.Itoa(radius)] = 0
	}
	for rows.Next() {
		var evLat, evLon float64
		if err := rows.Scan(&evLat, &evLon); err != nil {
			writeInternalError(w, err)
			return
		}
		d := haversineKm(lat, lon, evLat, evLon)
		for _, radius := range nearbyCountRadiiKm {
			if d <= float64(radius) {
				counts[strconv.Itoa(radius)]++
			}
		}
	}
	if err := rows.Err(); err != nil {
		writeInternalError(w, err)
		return
	}

	writeJSON(w, nearbyCountsResponse{RadiiKm: nearbyCountRadiiKm, Counts: counts})
}
