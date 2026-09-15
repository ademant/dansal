package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// TestFetchEventsUntil covers #1325: fetchEventsUntil loops server-side
// (fast loopback link) instead of the browser chaining one /events-more
// round trip per 100-event batch over a potentially slow connection.
func TestFetchEventsUntil(t *testing.T) {
	allEvents := []Event{
		{ID: 1, StartTime: "2024-01-01T10:00:00Z"},
		{ID: 2, StartTime: "2024-01-02T10:00:00Z"},
		{ID: 3, StartTime: "2024-01-05T10:00:00Z"},
		{ID: 4, StartTime: "2024-01-10T10:00:00Z"},
		{ID: 5, StartTime: "2024-02-01T10:00:00Z"},
	}
	const batchSize = 2
	var calls int
	// Mimics the real backend's start_time_after (">" not ">=") + LIMIT
	// pagination (cmd/dansal/events.go's applyEventFilters/applyListPagination),
	// ordered ascending by start_time.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		afterUnix, _ := strconv.ParseInt(r.URL.Query().Get("start_time_after"), 10, 64)
		var out []Event
		for _, e := range allEvents {
			ts, err := time.Parse(time.RFC3339, e.StartTime)
			if err != nil {
				t.Fatalf("bad fixture start_time %q: %v", e.StartTime, err)
			}
			if ts.Unix() > afterUnix {
				out = append(out, e)
				if len(out) == batchSize {
					break
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}))
	defer upstream.Close()

	client := &DansalClient{BaseURL: upstream.URL, HTTP: http.DefaultClient}

	unix := func(rfc3339 string) int64 {
		ts, err := time.Parse(time.RFC3339, rfc3339)
		if err != nil {
			t.Fatalf("parse %q: %v", rfc3339, err)
		}
		return ts.Unix()
	}

	t.Run("loops until an event at or past target is loaded", func(t *testing.T) {
		calls = 0
		events, err := fetchEventsUntil(context.Background(), client, 0, unix("2024-01-10T10:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 4 {
			t.Errorf("got %d events, want 4 (ids 1-4)", len(events))
		}
		if calls != 2 {
			t.Errorf("got %d upstream batches, want 2", calls)
		}
	})

	t.Run("target==0 fetches exactly one batch (Load More semantics)", func(t *testing.T) {
		calls = 0
		events, err := fetchEventsUntil(context.Background(), client, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != batchSize {
			t.Errorf("got %d events, want %d", len(events), batchSize)
		}
		if calls != 1 {
			t.Errorf("got %d upstream batches, want 1", calls)
		}
	})

	t.Run("stops when the backend runs out of events, target unreached", func(t *testing.T) {
		calls = 0
		events, err := fetchEventsUntil(context.Background(), client, 0, unix("2099-01-01T00:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != len(allEvents) {
			t.Errorf("got %d events, want all %d", len(events), len(allEvents))
		}
		// 3 batches of <=2 to exhaust 5 events (2+2+1), plus the empty batch
		// that reveals there's nothing left -- fetchEventsUntil only trusts a
		// literal empty batch as "done", not a short one, since it doesn't
		// know what limit the backend applied on this call.
		if calls != 4 {
			t.Errorf("got %d upstream batches, want 4", calls)
		}
	})

	t.Run("a batch overshooting target is still returned whole, not trimmed", func(t *testing.T) {
		calls = 0
		// after=event 2's time, target=event 3's time: the first batch fetched
		// is [3,4] (batchSize=2), and since batch's last event (4) already
		// reaches target, the loop stops after this one batch -- it does not
		// trim event 4 off just because only event 3 was strictly needed.
		events, err := fetchEventsUntil(context.Background(), client, unix("2024-01-02T10:00:00Z"), unix("2024-01-05T10:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 2 || events[0].ID != 3 || events[1].ID != 4 {
			t.Errorf("got %+v, want events id 3 and 4", events)
		}
		if calls != 1 {
			t.Errorf("got %d upstream batches, want 1", calls)
		}
	})
}
