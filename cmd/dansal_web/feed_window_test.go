package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// #1590: iCal feed windowing. Covers request parsing and bounds, the window
// itself, the config checks, the 400 response, the ICS output, and the paged
// fetch that replaced the 100-event truncation.

func TestParseFeedWindow(t *testing.T) {
	q := func(s string) url.Values { v, _ := url.ParseQuery(s); return v }
	cases := []struct {
		name    string
		query   string
		window  int // ical_feed_window_days
		want    feedWindow
		wantErr bool
	}{
		{"no parameters, no default", "", 0, feedWindow{}, false},
		{"no parameters, operator default", "", 14, feedWindow{Days: 14}, false},
		{"days overrides the default", "days=30", 14, feedWindow{Days: 30}, false},
		{"count keeps the default days", "count=5", 14, feedWindow{Days: 14, Count: 5}, false},
		{"days at the bound", "days=3650", 0, feedWindow{Days: 3650}, false},
		{"count at the bound", "count=1000", 0, feedWindow{Count: 1000}, false},
		{"days zero is invalid", "days=0", 0, feedWindow{}, true},
		{"days negative is invalid", "days=-3", 0, feedWindow{}, true},
		{"days not a number", "days=soon", 0, feedWindow{}, true},
		{"days over the bound", "days=3651", 0, feedWindow{}, true},
		{"count over the bound", "count=1001", 0, feedWindow{}, true},
		{"days given twice", "days=3&days=4", 0, feedWindow{}, true},
		{"empty days", "days=", 0, feedWindow{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{ICalFeedWindowDays: tc.window}
			got, err := parseFeedWindow(q(tc.query), cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("window = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestApplyFeedWindow(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	at := func(days int) string { return now.AddDate(0, 0, days).Format(time.RFC3339) }
	events := []Event{
		{ID: 4, Title: "far", StartTime: at(400)},
		{ID: 2, Title: "three", StartTime: at(3)},
		{ID: 9, Title: "undated", StartTime: ""},
		{ID: 1, Title: "one", StartTime: at(1)},
		{ID: 3, Title: "ten", StartTime: at(10)},
	}
	ids := func(evs []Event) string {
		var s []string
		for _, e := range evs {
			s = append(s, strconv.Itoa(e.ID))
		}
		return strings.Join(s, ",")
	}

	if got := ids(applyFeedWindow(events, feedWindow{}, now)); got != "1,2,3,4,9" {
		t.Errorf("no window = %s, want soonest first with undated last", got)
	}
	if got := ids(applyFeedWindow(events, feedWindow{Days: 30}, now)); got != "1,2,3,9" {
		t.Errorf("days=30 = %s, want 1,2,3 and the undated event", got)
	}
	if got := ids(applyFeedWindow(events, feedWindow{Count: 2}, now)); got != "1,2" {
		t.Errorf("count=2 = %s, want the two soonest", got)
	}
	if got := ids(applyFeedWindow(events, feedWindow{Days: 5, Count: 1}, now)); got != "1" {
		t.Errorf("days=5&count=1 = %s, want the restrictive one", got)
	}
	if events[0].ID != 4 {
		t.Error("applyFeedWindow must not reorder the input")
	}
}

func TestValidateICalWindow(t *testing.T) {
	ok := &Config{ICalMaxDays: 3650, ICalMaxCount: 1000, ICalFeedWindowDays: 30}
	if err := ok.validateICalWindow(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	if err := (&Config{ICalMaxDays: 0, ICalMaxCount: 1000}).validateICalWindow(); err == nil {
		t.Error("ical_max_days 0 must be rejected")
	}
	if err := (&Config{ICalMaxDays: 30, ICalMaxCount: 1000, ICalFeedWindowDays: 60}).validateICalWindow(); err == nil {
		t.Error("a default window above ical_max_days must be rejected")
	}
}

func TestICalFeedAppliesWindowAndRejectsBadValues(t *testing.T) {
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test"}
	events := []Event{
		{ID: 1, Title: "a", StartTime: "2099-01-01T10:00:00+01:00", EndTime: "2099-01-01T12:00:00+01:00"},
		{ID: 2, Title: "b", StartTime: "2099-01-02T10:00:00+01:00", EndTime: "2099-01-02T12:00:00+01:00"},
		{ID: 3, Title: "c", StartTime: "2099-01-03T10:00:00+01:00", EndTime: "2099-01-03T12:00:00+01:00"},
	}
	serve := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/feed/events.ics?"+query, nil)
		r.SetPathValue("format", "ics")
		rec := httptest.NewRecorder()
		serveEventFeed(rec, r, cfg, "t", events, i18n)
		return rec
	}

	rec := serve("count=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("count=1 status = %d", rec.Code)
	}
	if n := strings.Count(rec.Body.String(), "BEGIN:VEVENT"); n != 1 {
		t.Errorf("count=1 gave %d events, want 1", n)
	}

	if rec := serve("days=0"); rec.Code != http.StatusBadRequest {
		t.Errorf("days=0 status = %d, want 400", rec.Code)
	}
}

// The feed fetch follows the API's pages, so no feed stops at the first page.
func TestFeedEventsQueryFollowsPages(t *testing.T) {
	const total = 2500
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))
		if q.Get("organization_id") != "7" || q.Get("is_published") != "true" {
			http.Error(w, "unexpected query "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		var page []Event
		for i := offset; i < offset+limit && i < total; i++ {
			page = append(page, Event{ID: i + 1})
		}
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
		json.NewEncoder(w).Encode(page)
	}))
	defer api.Close()

	client := &DansalClient{BaseURL: api.URL, HTTP: api.Client()}
	events, err := client.feedEventsQuery(context.Background(), "is_published=true&organization_id=7")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != total {
		t.Fatalf("got %d events, want %d", len(events), total)
	}
	if events[total-1].ID != total {
		t.Errorf("last id = %d, want %d", events[total-1].ID, total)
	}
}
