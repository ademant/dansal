package main

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"time"
)

// searchRadiiKm are the radius options the search page's own <select
// id="sf-radius"> offers (cmd/dansal_web/templates/search.html) — must match
// nearbyCountRadiiKm in cmd/dansal so a venue page's "N events within X km"
// link (#1436) always carries a radius this page can actually preselect.
var searchRadiiKm = []int{10, 50, 100, 200, 500}

// searchMaxResults is the threshold past which /search/results asks the user
// to narrow their filters. It is advisory only: past the threshold the handler
// still returns the rendered rows and geo, and the template shows a counted
// banner alongside them (see #1373). Raising this is therefore a rendering-cost
// knob, not a hard cutoff. Well below the API's own ceiling of 1000
// (applyListPagination in cmd/dansal/events.go).
const searchMaxResults = 500

// searchLimit caps how many events /search/results fetches in one shot. It sits
// just above searchMaxResults so the whole displayable page (up to the
// TooMany threshold) always arrives in a single request.
const searchLimit = 550

// SearchData carries the initial-load defaults for the /search page.
type SearchData struct {
	DateFrom string // ISO date, defaults to the start of the current week
	DateTo   string // ISO date, defaults to the end of the current week
	Dances   []Dance
	Locs     template.JS // locationsJSON output, for the town-suggest source
	// #1436: deep-link geo filter from e.g. a venue page's "no upcoming
	// events, but N within X km" link. Each is "" when not set/invalid —
	// the page JS only pre-applies the geo filter when Lat/Lng/Radius are
	// all present, so a partially-malformed link just degrades to a normal
	// page load instead of filtering on broken data.
	InitialLat    string
	InitialLng    string
	InitialRadius string
	InitialLabel  string // optional display label for the geo origin (e.g. venue name)
}

// validFloatParam returns s unchanged if it parses as a float64, else "".
func validFloatParam(s string) string {
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		return ""
	}
	return s
}

// validRadiusParam returns s unchanged if it's one of searchRadiiKm, else "".
func validRadiusParam(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil {
		return ""
	}
	for _, r := range searchRadiiKm {
		if r == n {
			return s
		}
	}
	return ""
}

// defaultSearchDateRange is the search page's default date range on a
// fresh load (#1470): today through today+6 days (the next 7 days,
// including today), in the server process's local time. Deliberately not
// the current Monday-Sunday week (the previous behavior) -- that range is
// mostly or entirely in the past by Friday through Sunday, so the default
// result was nearly empty exactly on the days most visitors show up.
func defaultSearchDateRange() (string, string) {
	now := time.Now()
	to := now.AddDate(0, 0, 6)
	return now.Format("2006-01-02"), to.Format("2006-01-02")
}

func searchPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var dances []Dance
		var locs []Location
		err := fetchParallel(
			func() error {
				var err error
				dances, err = client.GetDances(r.Context())
				if err != nil {
					log.Printf("search: could not load dances: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				locs, err = client.GetLocations(r.Context())
				if err != nil {
					log.Printf("search: could not load locations: %v", err)
				}
				return nil
			},
		)
		if err != nil {
			logHTTPError(w, r, "could not load search data", http.StatusBadGateway)
			return
		}

		dateFrom, dateTo := defaultSearchDateRange()
		q := r.URL.Query()
		// #1436: a deep link (e.g. the venue-page nearby fallback) can carry
		// its own date range — only override the current-week default when
		// both are present and form a valid, non-inverted range.
		if from, ok := parseISODate(q.Get("from")); ok {
			if to, ok2 := parseISODate(q.Get("to")); ok2 && !to.Before(from) {
				dateFrom, dateTo = q.Get("from"), q.Get("to")
			}
		}

		title := i18n.T(r, "search_title")
		td := tmplData(r, cfg, i18n, title, SearchData{
			DateFrom:      dateFrom,
			DateTo:        dateTo,
			Dances:        dances,
			Locs:          tmplFuncMap["locationsJSON"].(func([]Location) template.JS)(locs),
			InitialLat:    validFloatParam(q.Get("lat")),
			InitialLng:    validFloatParam(q.Get("lng")),
			InitialRadius: validRadiusParam(q.Get("radius")),
			InitialLabel:  q.Get("label"),
		})
		td.Hreflang = true
		renderTemplate(w, tmpls.search, td)
	}
}

// searchResultsResponse is the payload for GET /search/results.
type searchResultsResponse struct {
	RowsHTML string     `json:"rows_html"`
	Geo      []geoEvent `json:"geo"`
	Total    int        `json:"total"`
	// TooMany is advisory (#1373): the response still carries RowsHTML and Geo
	// so the page can render what it has and invite the user to narrow down.
	TooMany bool `json:"too_many"`
	// Shown is how many events RowsHTML/Geo actually contain, i.e. the count
	// after the searchLimit fetch, so the banner can say "first Shown of Total".
	Shown int `json:"shown"`
}

// searchResultsHandler answers the /search page's date-range fetch. Events are
// matched by overlap with [from,to] -- end_time_after (event hasn't already
// ended before the range starts) combined with start_time_before (event
// hasn't started after the range ends) -- so multi-day events that only
// partially fall inside the selected range are still included. Town, geo
// radius, event type, dance, and map-viewport filtering all happen client-side
// against this one fetched batch (see discussion in #650-adjacent issues).
func searchResultsHandler(tmpls *Templates, i18n *I18n, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if searchThrottle.isBlocked(ip) {
			writeJSONResponse(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
			return
		}
		searchThrottle.record(ip)

		from, ok1 := parseISODate(r.URL.Query().Get("from"))
		to, ok2 := parseISODate(r.URL.Query().Get("to"))
		if !ok1 || !ok2 || to.Before(from) {
			http.Error(w, "from/to are required ISO dates with to >= from", http.StatusBadRequest)
			return
		}
		rangeStart := from.Unix()
		rangeEnd := to.AddDate(0, 0, 1).Unix() // end of the "to" day

		filter := EventFilter{
			IsPublished:     true,
			EndTimeAfter:    rangeStart - 1,
			StartTimeBefore: rangeEnd + 1,
			Limit:           searchLimit,
		}
		events, total, err := client.GetEventsFilteredWithTotal(r.Context(), filter.Values())
		if err != nil {
			logHTTPError(w, r, "could not load events", http.StatusBadGateway)
			return
		}

		// Past searchMaxResults we still render what we fetched rather than
		// bailing out with an empty payload (#1373). The page used to hide the
		// table, the empty state and the map entirely, so a wide date range on
		// a busy calendar produced a blank screen -- even though the client-side
		// town/type/dance filters that would narrow it need this very batch.
		_, rowsHTML, err := fetchAndRenderEventRows(r, tmpls.search, i18n, client, func() ([]Event, error) {
			return events, nil
		})
		if err != nil {
			logHTTPError(w, r, "could not render events", http.StatusInternalServerError)
			return
		}

		writeJSONResponse(w, http.StatusOK, searchResultsResponse{
			RowsHTML: rowsHTML,
			Geo:      eventsToGeo(events),
			Total:    total,
			TooMany:  total > searchMaxResults,
			Shown:    len(events),
		})
	}
}
