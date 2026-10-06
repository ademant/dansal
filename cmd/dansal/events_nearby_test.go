package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func insertTestLocation(t *testing.T, lat, lon float64) int {
	t.Helper()
	res, err := db.Exec(`INSERT INTO locations (location, country_code, latitude, longitude) VALUES ('Test Hall', 'DE', ?, ?)`, lat, lon)
	if err != nil {
		t.Fatalf("insert location: %v", err)
	}
	id, _ := res.LastInsertId()
	return int(id)
}

// TestGetEventsNearbyCounts covers #1436: cumulative per-radius counts
// around an origin point, computed with exact (haversine) distance rather
// than the bbox pre-filter alone — an event just outside the smaller radii
// but inside the bbox square must not be counted there.
func TestGetEventsNearbyCounts(t *testing.T) {
	setupDedupTestDB(t)

	// Origin: 50.73, 7.10.
	// ~5 km away — inside every radius.
	near := insertTestLocation(t, 50.77, 7.10)
	// ~80 km away — inside 100/200/500 km, outside 10/50 km.
	mid := insertTestLocation(t, 51.45, 7.10)
	// ~1000 km away — outside every radius (also outside the 500 km bbox).
	far := insertTestLocation(t, 41.0, 7.10)

	const start = 2000000000
	const day = 24 * 3600
	for i, locID := range []int{near, mid, far} {
		if _, _, _, err := insertEvent(db, EventInput{
			Title: "Event", StartTime: start + int64(i)*day, EndTime: start + int64(i)*day + 3600,
			IsPublished: true, LocationID: int64(locID),
		}); err != nil {
			t.Fatalf("insert event %d: %v", i, err)
		}
	}
	// An unpublished event at the near location must not be counted.
	if _, _, _, err := insertEvent(db, EventInput{
		Title: "Draft", StartTime: start, EndTime: start + 3600,
		IsPublished: false, LocationID: int64(near),
	}); err != nil {
		t.Fatalf("insert draft event: %v", err)
	}
	// A cancelled event at the near location must not be counted either.
	if _, _, _, err := insertEvent(db, EventInput{
		Title: "Cancelled", StartTime: start, EndTime: start + 3600,
		IsPublished: true, IsCancelled: true, LocationID: int64(near),
	}); err != nil {
		t.Fatalf("insert cancelled event: %v", err)
	}
	db.Exec("UPDATE events SET email_verified = 1")

	req := httptest.NewRequest("GET", "/api/v1/events/nearby-counts?lat=50.73&lon=7.10&from=2033-05-18&to=2033-05-21", nil)
	w := httptest.NewRecorder()
	getEventsNearbyCounts(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got nearbyCountsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v, body=%s", err, w.Body.String())
	}
	want := map[string]int{"10": 1, "50": 1, "100": 2, "200": 2, "500": 2}
	for radius, n := range want {
		if got.Counts[radius] != n {
			t.Errorf("counts[%s] = %d, want %d (full response: %+v)", radius, got.Counts[radius], n, got.Counts)
		}
	}
}

func TestGetEventsNearbyCountsValidation(t *testing.T) {
	setupDedupTestDB(t)

	for _, url := range []string{
		"/api/v1/events/nearby-counts?lat=50.73&lon=7.10&from=2033-05-21&to=2033-05-18", // to before from
		"/api/v1/events/nearby-counts?lat=50.73&lon=7.10&from=not-a-date&to=2033-05-21",
		"/api/v1/events/nearby-counts?lon=7.10&from=2033-05-18&to=2033-05-21", // missing lat
	} {
		req := httptest.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		getEventsNearbyCounts(w, req)
		if w.Code != 400 {
			t.Errorf("%s: status = %d, want 400", url, w.Code)
		}
	}
}

// TestGetEventsOrderDesc covers #1437's newest-first ordering, needed for a
// venue page's "most recent N past events" (plain ascending + a small limit
// would otherwise return the OLDEST N instead).
func TestGetEventsOrderDesc(t *testing.T) {
	setupDedupTestDB(t)
	locID := insertTestLocation(t, 50.73, 7.10)

	const day = 24 * 3600
	titles := []string{"Oldest", "Middle", "Newest"}
	for i, title := range titles {
		if _, _, _, err := insertEvent(db, EventInput{
			Title: title, StartTime: 2000000000 + int64(i)*day, EndTime: 2000003600 + int64(i)*day,
			IsPublished: true, LocationID: int64(locID),
		}); err != nil {
			t.Fatalf("insert event %q: %v", title, err)
		}
	}
	db.Exec("UPDATE events SET email_verified = 1")

	req := httptest.NewRequest("GET", "/api/v1/events?include_past=true&order=desc&limit=2", nil)
	w := httptest.NewRecorder()
	getEvents(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var events []Event
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(events) != 2 || events[0].Title != "Newest" || events[1].Title != "Middle" {
		got := make([]string, len(events))
		for i, e := range events {
			got[i] = e.Title
		}
		t.Errorf("order=desc&limit=2 titles = %v, want [Newest Middle]", got)
	}
}
