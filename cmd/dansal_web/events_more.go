package main

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// eventsMoreResponse is the payload for GET /events-more.
type eventsMoreResponse struct {
	RowsHTML string     `json:"rows_html"`
	Geo      []geoEvent `json:"geo"`
	Total    int        `json:"total"`
	Done     bool       `json:"done"` // true when the server has no more events after this batch
}

// eventsMoreMaxBatches bounds fetchEventsUntil's loop so a pathological
// target far in the future (or a data anomaly) can't turn one request into
// an unbounded fetch loop -- mirrors GetAllFutureEvents's maxEventsPages
// (cmd/dansal_web/dansal.go).
const eventsMoreMaxBatches = 20

// fetchEventsUntil fetches successive 100-event batches (client.GetEvents'
// server-side page size) starting after, advancing the cursor to each
// batch's last (latest start_time) event, until an event at or past target
// is loaded, an empty batch is returned (no more events at all), or
// eventsMoreMaxBatches is hit. target == 0 means "just one batch" -- the
// Load More button's case, which doesn't know a target date in advance
// (matches ensureLoadedUntil's Infinity/single-step semantics client-side).
//
// This loop used to run client-side, one /events-more round trip per
// iteration over the browser<->server link. Moved server-side (#1325)
// because that link can be slow (real network RTT+TLS, measured 1-1.5s per
// hop against a real browser) while this loop's own client.GetEvents calls
// stay on the fast server<->backend-API loopback link (measured 9-32ms) --
// so collapsing N of the former into 1 of the former (wrapping N of the
// latter) is a straightforward, large latency win for exactly the
// background 3-month prefetch that used to trigger this chain on every
// initial page load.
func fetchEventsUntil(ctx context.Context, client *DansalClient, after, target int64) ([]Event, error) {
	var all []Event
	for i := 0; i < eventsMoreMaxBatches; i++ {
		batch, err := client.GetEvents(ctx, strconv.FormatInt(after, 10))
		if err != nil {
			return all, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if target == 0 {
			break
		}
		last, err := time.Parse(time.RFC3339, batch[len(batch)-1].StartTime)
		if err != nil {
			break
		}
		after = last.Unix()
		if after >= target {
			break
		}
	}
	return all, nil
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
		var target int64
		if t := r.URL.Query().Get("target"); t != "" {
			target, _ = strconv.ParseInt(t, 10, 64) // 0 on parse failure: single-batch fallback
		}

		events, rowsHTML, err := fetchAndRenderEventRows(r, tmpls.index, i18n, client, func() ([]Event, error) {
			return fetchEventsUntil(r.Context(), client, after, target)
		})
		if err != nil {
			logHTTPError(w, r, "could not load events", http.StatusBadGateway)
			return
		}

		// Refresh the shared total so pagination counters and the "all loaded"
		// cutoff reflect the current server state, not the page-load snapshot
		// (the after-path fetch alone doesn't update it; #1032).
		client.RefreshEventsTotal(r.Context())

		writeJSONResponse(w, http.StatusOK, eventsMoreResponse{
			RowsHTML: rowsHTML,
			Geo:      eventsToGeo(events),
			Total:    client.EventsTotal(),
			Done:     len(events) == 0,
		})
	}
}
