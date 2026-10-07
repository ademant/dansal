package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// #1469: loadEventPageData's org/venue upcoming-count fetch and same-set
// dedup decision, against a mocked API.

// mockEventPageAPI serves just enough of the API for loadEventPageData to
// run without logging spurious errors: tags, one org, no contact posts, and
// /api/v1/events?... counts driven by counts (keyed by exactly which query
// params are present, since that's how the three #1469 calls differ).
func mockEventPageAPI(t *testing.T, org Organization, counts map[string]int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/tags":
			json.NewEncoder(w).Encode([]Tag{})
		case r.URL.Path == "/api/v1/organizations":
			json.NewEncoder(w).Encode([]Organization{org})
		case r.URL.Path == "/api/v1/events" && r.URL.Query().Get("limit") == "1":
			q := r.URL.Query()
			key := "org=" + q.Get("organization_id") + "&loc=" + q.Get("location_id")
			w.Header().Set("X-Total-Count", strconv.Itoa(counts[key]))
			json.NewEncoder(w).Encode([]Event{})
		case r.URL.Path == "/api/v1/events/1/contact-posts":
			json.NewEncoder(w).Encode([]ContactPost{})
		default:
			json.NewEncoder(w).Encode([]any{})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLoadEventPageDataUpcomingCountsDifferentSets(t *testing.T) {
	setupSiteCfg(t)
	org := Organization{ID: 7, Name: "Balfolk Chemnitz"}
	locID := 42
	// org=7 has 3 upcoming events total; venue 42 has 2; of those 2, only 1
	// is by org 7 -- genuinely different sets, both lines should show.
	counts := map[string]int{
		"org=7&loc=":   3,
		"org=&loc=42":  2,
		"org=7&loc=42": 1,
	}
	srv := mockEventPageAPI(t, org, counts)
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	req := httptest.NewRequest(http.MethodGet, "/events/1", nil)

	event := Event{
		ID: 1, Title: "Past Ball", StartTime: "2020-07-01T20:00:00+02:00", EndTime: "2020-07-01T23:00:00+02:00",
		OrganizationID: &org.ID, LocationID: &locID, Location: &Location{ID: locID, Location: "Fichtehaus"},
	}
	data := loadEventPageData(req, client, event, nil)

	if !data.showOrgUpcoming || data.orgUpcomingCount != 3 {
		t.Errorf("showOrgUpcoming=%v orgUpcomingCount=%d, want true/3", data.showOrgUpcoming, data.orgUpcomingCount)
	}
	if !data.showVenueUpcoming || data.venueUpcomingCount != 2 {
		t.Errorf("showVenueUpcoming=%v venueUpcomingCount=%d, want true/2 (sets genuinely differ)", data.showVenueUpcoming, data.venueUpcomingCount)
	}
}

func TestLoadEventPageDataUpcomingCountsSameSetDedup(t *testing.T) {
	setupSiteCfg(t)
	org := Organization{ID: 7, Name: "Balfolk Chemnitz"}
	locID := 42
	// org 7's only upcoming events (2) are both at venue 42, and venue 42
	// has no other org's events -- identical sets, venue line suppressed.
	counts := map[string]int{
		"org=7&loc=":   2,
		"org=&loc=42":  2,
		"org=7&loc=42": 2,
	}
	srv := mockEventPageAPI(t, org, counts)
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	req := httptest.NewRequest(http.MethodGet, "/events/1", nil)

	event := Event{
		ID: 1, Title: "Past Ball", StartTime: "2020-07-01T20:00:00+02:00", EndTime: "2020-07-01T23:00:00+02:00",
		OrganizationID: &org.ID, LocationID: &locID, Location: &Location{ID: locID, Location: "Fichtehaus"},
	}
	data := loadEventPageData(req, client, event, nil)

	if !data.showOrgUpcoming || data.orgUpcomingCount != 2 {
		t.Errorf("showOrgUpcoming=%v orgUpcomingCount=%d, want true/2", data.showOrgUpcoming, data.orgUpcomingCount)
	}
	if data.showVenueUpcoming {
		t.Errorf("showVenueUpcoming=true, want false (identical sets -- org line alone covers it)")
	}
}

// Sanity check (#1469's explicit "no extra API calls on upcoming event
// pages"): for an event that hasn't happened yet, none of the three
// upcoming-count fetches run at all.
func TestLoadEventPageDataSkipsUpcomingCountsForFutureEvent(t *testing.T) {
	setupSiteCfg(t)
	org := Organization{ID: 7, Name: "Balfolk Chemnitz"}
	var countRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/tags":
			json.NewEncoder(w).Encode([]Tag{})
		case r.URL.Path == "/api/v1/organizations":
			json.NewEncoder(w).Encode([]Organization{org})
		case r.URL.Path == "/api/v1/events" && r.URL.Query().Get("limit") == "1":
			countRequests++
			json.NewEncoder(w).Encode([]Event{})
		default:
			json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()
	client := &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}
	req := httptest.NewRequest(http.MethodGet, "/events/1", nil)

	locID := 42
	event := Event{
		ID: 1, Title: "Future Ball", StartTime: "2099-07-01T20:00:00+02:00", EndTime: "2099-07-01T23:00:00+02:00",
		OrganizationID: &org.ID, LocationID: &locID, Location: &Location{ID: locID, Location: "Fichtehaus"},
	}
	data := loadEventPageData(req, client, event, nil)

	if countRequests != 0 {
		t.Errorf("made %d upcoming-count request(s) for a future event, want 0", countRequests)
	}
	if data.showOrgUpcoming || data.showVenueUpcoming {
		t.Errorf("future event must not show any upcoming-count hint")
	}
}
