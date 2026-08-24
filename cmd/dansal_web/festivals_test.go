package main

import "testing"

// TestGroupFoldedFestivals covers the wp-dansal-derived folding rule (#1144):
// a location with an edition in the selected year keeps it; a location with
// none falls back to its single most recent edition from any year.
func TestGroupFoldedFestivals(t *testing.T) {
	events := []Event{
		{ID: 1, LocationID: intPtr(10), StartTime: "2026-07-10T10:00:00Z"}, // venue 10, in-year
		{ID: 2, LocationID: intPtr(10), StartTime: "2025-07-10T10:00:00Z"}, // venue 10, past (superseded)
		{ID: 3, LocationID: intPtr(20), StartTime: "2024-08-01T10:00:00Z"}, // venue 20, only past edition
		{ID: 4, LocationID: intPtr(20), StartTime: "2023-08-01T10:00:00Z"}, // venue 20, older past edition
		{ID: 5, LocationID: intPtr(30), StartTime: "2026-09-01T10:00:00Z"}, // venue 30, in-year
	}

	yearEvents, folded := groupFoldedFestivals(events, 2026)

	if len(yearEvents) != 2 {
		t.Fatalf("expected 2 year events, got %d: %+v", len(yearEvents), yearEvents)
	}
	gotIDs := map[int]bool{}
	for _, e := range yearEvents {
		gotIDs[e.ID] = true
	}
	if !gotIDs[1] || !gotIDs[5] {
		t.Errorf("expected year events to include IDs 1 and 5, got %+v", yearEvents)
	}

	if len(folded) != 1 {
		t.Fatalf("expected 1 folded event (venue 20's latest), got %d: %+v", len(folded), folded)
	}
	if folded[0].ID != 3 {
		t.Errorf("expected folded event to be the most recent edition (ID 3), got ID %d", folded[0].ID)
	}
}

// TestGroupFoldedFestivalsSkipsMissingLocation ensures events with no
// location_id (which the map/calendar can't place anyway) don't panic and
// are simply excluded from both buckets.
func TestGroupFoldedFestivalsSkipsMissingLocation(t *testing.T) {
	events := []Event{
		{ID: 1, LocationID: nil, StartTime: "2026-07-10T10:00:00Z"},
	}
	yearEvents, folded := groupFoldedFestivals(events, 2026)
	if len(yearEvents) != 0 || len(folded) != 0 {
		t.Errorf("expected both buckets empty, got yearEvents=%+v folded=%+v", yearEvents, folded)
	}
}
