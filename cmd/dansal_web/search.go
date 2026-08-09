package main

import (
	"html/template"
	"log"
	"net/http"
	"time"
)

// searchMaxResults is the threshold past which /search/results asks the user
// to narrow their filters instead of rendering a huge map+list. Matches the
// API's default pagination limit (applyPagination in cmd/dansal/events.go),
// so it's consistent with the cap visitors already see on the main page.
const searchMaxResults = 100

// searchLimit caps how many events /search/results fetches in one shot. It sits
// just above searchMaxResults so the whole displayable page (up to the
// TooMany cutoff) always arrives in a single request.
const searchLimit = 150

// SearchData carries the initial-load defaults for the /search page.
type SearchData struct {
	DateFrom string // ISO date, defaults to the start of the current week
	DateTo   string // ISO date, defaults to the end of the current week
	Dances   []Dance
	Locs     template.JS // locationsJSON output, for the town-suggest source
}

func currentWeekRange() (string, string) {
	now := time.Now()
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7 // Monday-based week
	}
	monday := now.AddDate(0, 0, -(weekday - 1))
	sunday := monday.AddDate(0, 0, 6)
	return monday.Format("2006-01-02"), sunday.Format("2006-01-02")
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

		dateFrom, dateTo := currentWeekRange()
		title := i18n.T(r, "search_title")
		td := tmplData(r, cfg, i18n, title, SearchData{
			DateFrom: dateFrom,
			DateTo:   dateTo,
			Dances:   dances,
			Locs:     tmplFuncMap["locationsJSON"].(func([]Location) template.JS)(locs),
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
	TooMany  bool       `json:"too_many"`
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

		if total > searchMaxResults {
			writeJSONResponse(w, http.StatusOK, searchResultsResponse{Total: total, TooMany: true})
			return
		}

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
		})
	}
}
