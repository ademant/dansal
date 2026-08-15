package main

import "testing"

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
