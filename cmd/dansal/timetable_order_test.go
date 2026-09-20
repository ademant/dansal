package main

import "testing"

// A single-day event mixes entries saved from the inline editor (no
// entry_date) with ones saved from the dedicated timetable editor (dated);
// the earlier start time must still come first.
func TestSortTimetableEntriesMixedDatedUndated(t *testing.T) {
	entries := []TimetableEntry{
		{ID: 164, StartTime: "15:45", EntryDate: ""},
		{ID: 163, StartTime: "14:30", EntryDate: "2026-09-20"},
	}
	sortTimetableEntries(entries, "2026-09-20")
	if entries[0].ID != 163 || entries[1].ID != 164 {
		t.Fatalf("order = %d,%d; want 163,164", entries[0].ID, entries[1].ID)
	}
}

func TestSortTimetableEntriesMultiDay(t *testing.T) {
	entries := []TimetableEntry{
		{ID: 3, StartTime: "10:00", EntryDate: "2026-09-21"},
		{ID: 2, StartTime: "20:00", EntryDate: ""},           // event's first day
		{ID: 1, StartTime: "09:00", EntryDate: "2026-09-20"}, // same day, earlier
	}
	sortTimetableEntries(entries, "2026-09-20")
	got := []int{entries[0].ID, entries[1].ID, entries[2].ID}
	want := []int{1, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v; want %v", got, want)
		}
	}
}
