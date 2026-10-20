package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCleanWatchlistIDs(t *testing.T) {
	got, err := cleanWatchlistIDs([]int{12, 45, 12, 78})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 || got[0] != 12 || got[1] != 45 || got[2] != 78 {
		t.Errorf("got %v, want [12 45 78] (duplicates dropped, order kept)", got)
	}
	if _, err := cleanWatchlistIDs([]int{0}); err == nil {
		t.Error("id 0 must be refused")
	}
	if _, err := cleanWatchlistIDs([]int{-3}); err == nil {
		t.Error("negative id must be refused")
	}
	over := make([]int, watchlistMaxIDs+1)
	for i := range over {
		over[i] = i + 1
	}
	if _, err := cleanWatchlistIDs(over); err == nil {
		t.Errorf("%d ids must be refused (cap is %d)", len(over), watchlistMaxIDs)
	}
	atCap := over[:watchlistMaxIDs]
	if _, err := cleanWatchlistIDs(atCap); err != nil {
		t.Errorf("%d ids must be accepted: %v", len(atCap), err)
	}
}

func TestWatchlistEventEnded(t *testing.T) {
	now := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		ev   Event
		want bool
	}{
		{"future", Event{StartTime: "2030-07-01T19:00:00Z", EndTime: "2030-07-01T23:00:00Z"}, false},
		{"ended by end time", Event{StartTime: "2030-05-01T19:00:00Z", EndTime: "2030-05-01T23:00:00Z"}, true},
		{"no end time, start in past", Event{StartTime: "2030-05-01T19:00:00Z"}, true},
		{"unparseable time is not ended", Event{StartTime: "not a time"}, false},
	}
	for _, c := range cases {
		if got := watchlistEventEnded(c.ev, now); got != c.want {
			t.Errorf("%s: ended=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestWatchlistLookupRejectsBadBodies(t *testing.T) {
	geocodeThrottle = newSubmissionThrottle(100, time.Minute)
	watchlistLookupThrottle = newSubmissionThrottle(100, time.Minute)
	handler := watchlistLookupHandler(nil)

	cases := map[string]string{
		"not json":  `{"ids":`,
		"string id": `{"ids":["12"]}`,
		"zero id":   `{"ids":[0]}`,
		"too many":  `{"ids":[` + strings.Repeat("1,", watchlistMaxIDs) + `1]}`,
	}
	for name, body := range cases {
		req := httptest.NewRequest(http.MethodPost, "/watchlist/events", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
}
