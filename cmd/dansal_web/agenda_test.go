package main

import (
	"testing"
	"time"
)

func agendaEvent(id int, start time.Time, cancelled bool) Event {
	return Event{ID: id, Title: "E", StartTime: start.Format(time.RFC3339), IsCancelled: cancelled}
}

func TestAgendaDaysWindowAndGrouping(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, loc)
	events := []Event{
		agendaEvent(1, time.Date(2026, 10, 21, 20, 0, 0, 0, loc), false), // yesterday: out
		agendaEvent(2, time.Date(2026, 10, 22, 19, 0, 0, 0, loc), false), // today evening
		agendaEvent(3, time.Date(2026, 10, 22, 11, 0, 0, 0, loc), false), // today, earlier: sorted first
		agendaEvent(4, time.Date(2026, 10, 25, 18, 0, 0, 0, loc), false), // inside the window
		agendaEvent(5, time.Date(2026, 10, 29, 10, 0, 0, 0, loc), false), // day 7 (today+7): out
		agendaEvent(6, time.Date(2026, 10, 23, 10, 0, 0, 0, loc), true),  // cancelled: out
	}
	days := agendaDays(events, nil, now, "en", "")
	if len(days) != 2 {
		t.Fatalf("got %d days, want 2 (22 and 25 October)", len(days))
	}
	if days[0].Date != "2026-10-22" || len(days[0].Entries) != 2 {
		t.Fatalf("first day %s with %d entries", days[0].Date, len(days[0].Entries))
	}
	if days[0].Entries[0].ID != 3 || days[0].Entries[1].ID != 2 {
		t.Errorf("today's entries not sorted by time: %+v", days[0].Entries)
	}
	if days[1].Date != "2026-10-25" || len(days[1].Entries) != 1 || days[1].Entries[0].ID != 4 {
		t.Errorf("second day wrong: %+v", days[1])
	}
}

func TestAgendaDaysEmpty(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	if days := agendaDays(nil, nil, now, "en", ""); len(days) != 0 {
		t.Errorf("no events gave %d days", len(days))
	}
}

func TestFilterEventsByOrgLoc(t *testing.T) {
	org1, org2, loc1 := 1, 2, 10
	events := []Event{
		{ID: 1, OrganizationID: &org1, LocationID: &loc1},
		{ID: 2, OrganizationID: &org2, LocationID: &loc1},
		{ID: 3, OrganizationID: &org1},
	}
	if got := filterEventsByOrgLoc(events, nil, nil); len(got) != 3 {
		t.Errorf("no filters kept %d, want 3", len(got))
	}
	if got := filterEventsByOrgLoc(events, map[int]bool{1: true}, nil); len(got) != 2 {
		t.Errorf("org filter kept %d, want 2", len(got))
	}
	if got := filterEventsByOrgLoc(events, map[int]bool{1: true}, map[int]bool{10: true}); len(got) != 1 || got[0].ID != 1 {
		t.Errorf("org+location filter kept %+v, want only event 1", got)
	}
}
