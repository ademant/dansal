package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// eventsMoreResponse is the payload for GET /api/events-more.
type eventsMoreResponse struct {
	RowsHTML string     `json:"rows_html"`
	Geo      []geoEvent `json:"geo"`
	Total    int        `json:"total"`
	Done     bool       `json:"done"` // true when the server has no more events after this batch
}

// eventsMoreHandler serves additional future events past the initial page-load's
// 100-event cap (see "Index page data flow" in CLAUDE.md). The index page's JS
// calls this when Load More, a date filter, or week/calendar navigation needs
// events beyond what was already fetched. Rows are rendered server-side via the
// same "event-row" template partial used on initial page load, so they can never
// drift from it; only the map-marker projection (geoEvent) crosses as JSON.
func eventsMoreHandler(tmpls *Templates, i18n *I18n, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		afterStr := r.URL.Query().Get("after")
		after, err := strconv.ParseInt(afterStr, 10, 64)
		if afterStr == "" || err != nil {
			http.Error(w, "after is required and must be a unix timestamp", http.StatusBadRequest)
			return
		}

		var events []Event
		var orgs []Organization
		var tagMap map[string]Tag
		var fetchErr error
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			events, fetchErr = client.GetEvents(r.Context(), strconv.FormatInt(after, 10))
		}()
		go func() { defer wg.Done(); orgs, _ = client.GetOrganizations(r.Context()) }()
		go func() { defer wg.Done(); tagMap, _ = client.GetTagMap(r.Context()) }()
		wg.Wait()
		if fetchErr != nil {
			logHTTPError(w, r, "could not load events", http.StatusBadGateway)
			return
		}

		orgMap := make(map[int]Organization, len(orgs))
		for _, o := range orgs {
			orgMap[o.ID] = o
		}
		strs := i18n.Strings(i18n.detectLang(r))

		var rowsHTML strings.Builder
		for _, e := range events {
			tmpls.index.ExecuteTemplate(&rowsHTML, "event-row", map[string]any{
				"Event": e, "OrgMap": orgMap, "TagMap": tagMap, "Strings": strs,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(eventsMoreResponse{
			RowsHTML: rowsHTML.String(),
			Geo:      eventsToGeo(events),
			Total:    client.EventsTotal(),
			Done:     len(events) == 0,
		})
	}
}
