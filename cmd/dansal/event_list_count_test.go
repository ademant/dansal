package main

import (
	"net/http/httptest"
	"testing"
)

// TestGetEventsTotalCountIgnoresPagination covers #1247: the pagination
// count query used to wrap the *entire* eventListSelect (correlated
// subqueries and dance-name join included) just to COUNT(*), which also
// risked the count silently diverging from reality if the split introduced
// a bug. Splitting the WHERE clause from the SELECT column list must still
// produce the same total regardless of LIMIT, including when a filter
// (country) that references the locations join is applied.
func TestGetEventsTotalCountIgnoresPagination(t *testing.T) {
	setupDedupTestDB(t)

	res, err := db.Exec(`INSERT INTO locations (location, country_code) VALUES ('Test Hall', 'DE')`)
	if err != nil {
		t.Fatalf("insert location: %v", err)
	}
	locID64, _ := res.LastInsertId()
	locID := int(locID64)

	// Spaced a day apart (not just an hour) so findExistingEvent's tier-3
	// dedup (same location + start_time ±3h, no title check) doesn't collapse
	// them into one event.
	const day = 24 * 3600
	for i := range 5 {
		if _, _, _, err := insertEvent(db, EventInput{
			Title: "Event", StartTime: 2000000000 + int64(i)*day, EndTime: 2000003600 + int64(i)*day,
			IsPublished: true, LocationID: int64(locID),
		}); err != nil {
			t.Fatalf("insert event %d: %v", i, err)
		}
	}
	db.Exec("UPDATE events SET email_verified = 1")

	req := httptest.NewRequest("GET", "/api/v1/events?limit=2&country=DE&include_past=true", nil)
	w := httptest.NewRecorder()
	getEvents(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Total-Count"); got != "5" {
		t.Errorf("X-Total-Count = %q, want %q (limit=2 must not affect the total)", got, "5")
	}
}
