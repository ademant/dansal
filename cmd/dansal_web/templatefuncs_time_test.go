package main

import (
	"encoding/json"
	"testing"
	"time"
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

// TestIsPastDate covers #1416: the admin import preview hides past-dated
// feed entries by default, using this to decide which rows qualify.
func TestIsPastDate(t *testing.T) {
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1).Format(time.RFC3339)
	monthsAgo := now.AddDate(0, -6, 0).Format(time.RFC3339)
	tomorrow := now.AddDate(0, 0, 1).Format(time.RFC3339)
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 1, 0, 0, now.Location()).Format(time.RFC3339)

	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"yesterday", yesterday, true},
		{"six months ago", monthsAgo, true},
		{"tomorrow", tomorrow, false},
		{"earlier today is not \"the past\"", startOfToday, false},
		{"unparseable", "not a date", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPastDate(c.s); got != c.want {
				t.Errorf("isPastDate(%q) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

// formatDateRange (#festivals): one date for a single-day festival, a
// "start – end" span when the festival crosses midnight, and the bare start
// date when no end is given.
func TestFormatDateRange(t *testing.T) {
	cases := []struct {
		name             string
		start, end, want string
	}{
		{"single day", "2026-06-12T18:00:00+02:00", "2026-06-12T23:00:00+02:00", "12 Jun 2026"},
		{"crosses midnight", "2026-06-12T18:00:00+02:00", "2026-06-14T00:30:00+02:00", "12 Jun 2026 – 14 Jun 2026"},
		{"no end", "2026-06-12T18:00:00+02:00", "", "12 Jun 2026"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatDateRange("en", "", c.start, c.end); got != c.want {
				t.Errorf("formatDateRange(%q, %q) = %q, want %q", c.start, c.end, got, c.want)
			}
		})
	}
}
