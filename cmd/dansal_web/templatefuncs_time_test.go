package main

import (
	"encoding/json"
	"testing"
)

// TestTimetableEntriesForNextUpJSON verifies the entry-id -> {date,start,
// end,title,room} JSON object the client-side "Now/Next" indicator (#1179)
// is built from, including the entry_date-override case (multi-day
// festival) and an empty timetable producing an empty (not null/invalid)
// object.
func TestTimetableEntriesForNextUpJSON(t *testing.T) {
	entries := []TimetableEntry{
		{ID: 10, Title: "Opening bal", StartTime: "18:00", EndTime: "19:00", LocationName: "Main hall"},
		{ID: 11, Title: "Day 2 workshop", StartTime: "10:00", EndTime: "11:00", EntryDate: "2026-09-17", Room: "Studio"},
	}
	raw := timetableEntriesForNextUpJSON(entries, "2026-09-15T18:00:00+02:00")
	var data map[string]map[string]string
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
	if data["10"]["date"] != "2026-09-15" || data["10"]["room"] != "Main hall" || data["10"]["title"] != "Opening bal" {
		t.Fatalf("unexpected entry 10: %+v", data["10"])
	}
	if data["11"]["date"] != "2026-09-17" || data["11"]["room"] != "Studio" {
		t.Fatalf("expected entry_date to override the event's own date, got: %+v", data["11"])
	}

	if got := timetableEntriesForNextUpJSON(nil, "2026-09-15T18:00:00+02:00"); string(got) != "{}" {
		t.Fatalf("expected an empty object for no entries, got %s", got)
	}
}

// TestTimetableGridOverlapLanes verifies overlapping entries in the same
// room are laid out side-by-side (lanes) instead of stacked on top of each
// other (#888).
func TestTimetableGridOverlapLanes(t *testing.T) {
	room := "Main Hall"
	entries := []TimetableEntry{
		{Title: "A", Room: room, StartTime: "20:00", EndTime: "21:00"},
		{Title: "B", Room: room, StartTime: "20:30", EndTime: "21:30"}, // overlaps A
		{Title: "C", Room: room, StartTime: "21:30", EndTime: "22:00"}, // starts when B ends: no overlap
	}
	grid := timetableGrid(entries)
	if len(grid.Columns) != 1 {
		t.Fatalf("expected 1 column, got %d", len(grid.Columns))
	}
	panels := grid.Columns[0].Panels
	if len(panels) != 3 {
		t.Fatalf("expected 3 panels, got %d", len(panels))
	}
	byTitle := map[string]TimetablePanel{}
	for _, p := range panels {
		byTitle[p.Entry.Title] = p
	}
	a, b, c := byTitle["A"], byTitle["B"], byTitle["C"]

	if a.TotalLanes != 2 || b.TotalLanes != 2 {
		t.Errorf("A/B overlap: expected TotalLanes=2, got A=%d B=%d", a.TotalLanes, b.TotalLanes)
	}
	if a.Lane == b.Lane {
		t.Errorf("A/B overlap: expected distinct lanes, both got lane %d", a.Lane)
	}
	if a.WidthPct != 50 || b.WidthPct != 50 {
		t.Errorf("A/B overlap: expected WidthPct=50, got A=%v B=%v", a.WidthPct, b.WidthPct)
	}
	if c.TotalLanes != 1 || c.Lane != 0 {
		t.Errorf("C does not overlap anything: expected Lane=0/TotalLanes=1, got Lane=%d TotalLanes=%d", c.Lane, c.TotalLanes)
	}
}

// TestTimetableGridNoOverlapSingleLane checks non-overlapping entries in the
// same room stay full-width (TotalLanes==1) rather than being split.
func TestTimetableGridNoOverlapSingleLane(t *testing.T) {
	room := "Main Hall"
	entries := []TimetableEntry{
		{Title: "A", Room: room, StartTime: "20:00", EndTime: "21:00"},
		{Title: "B", Room: room, StartTime: "21:00", EndTime: "22:00"},
	}
	grid := timetableGrid(entries)
	for _, p := range grid.Columns[0].Panels {
		if p.TotalLanes != 1 {
			t.Errorf("entry %q: expected TotalLanes=1 for non-overlapping entries, got %d", p.Entry.Title, p.TotalLanes)
		}
	}
}
