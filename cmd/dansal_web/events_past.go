package main

import (
	"net/http"
	"strconv"
)

// eventsPastResponse mirrors eventsMoreResponse's shape (rows_html + geo).
type eventsPastResponse struct {
	RowsHTML string     `json:"rows_html"`
	Geo      []geoEvent `json:"geo"`
}

// eventsPastHandler serves past events within a bounded [start, end] unix
// range for the index page's grayed-out weekly tiles, opt-in past-event
// list/map, and monthly heatmap (#1339). The client always asks for one
// calendar week or one calendar month at a time, so unlike eventsMoreHandler's
// open-ended forward pagination (capped at 100/batch, looping toward a
// target), a single bounded query is enough here -- no cursor/multi-batch
// looping needed. Rows are rendered server-side via the same "event-row"
// partial used everywhere else on the index page.
func eventsPastHandler(tmpls *Templates, i18n *I18n, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start, errS := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
		end, errE := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
		if errS != nil || errE != nil || end <= start {
			http.Error(w, "start and end are required unix timestamps with end > start", http.StatusBadRequest)
			return
		}

		events, rowsHTML, err := fetchAndRenderEventRows(r, tmpls.index, i18n, client, func() ([]Event, error) {
			// -1/+1: start_time_after/before are exclusive server-side, so
			// widen by a second to make [start, end] inclusive.
			return client.GetEventsFiltered(r.Context(), EventFilter{
				IsPublished:     true,
				IncludePast:     true,
				StartTimeAfter:  start - 1,
				StartTimeBefore: end + 1,
				Limit:           1000,
			}.Values())
		})
		if err != nil {
			logHTTPError(w, r, "could not load past events", http.StatusBadGateway)
			return
		}

		writeJSONResponse(w, http.StatusOK, eventsPastResponse{
			RowsHTML: rowsHTML,
			Geo:      eventsToGeo(events),
		})
	}
}
