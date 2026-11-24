package main

import (
	"strings"
	"testing"
	"time"
)

func cityTestEvent(id int, title, venue string, start, end time.Time) Event {
	return Event{
		ID:        id,
		Title:     title,
		StartTime: start.Format(time.RFC3339),
		EndTime:   end.Format(time.RFC3339),
		Location:  &Location{ID: 100 + id, Location: venue, Town: "Köln"},
	}
}

func TestWeekendWindow(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	for _, tc := range []struct {
		now      time.Time
		wantFrom string
	}{
		{time.Date(2026, 11, 24, 15, 0, 0, 0, loc), "2026-11-27"}, // Tuesday → coming Friday
		{time.Date(2026, 11, 27, 9, 0, 0, 0, loc), "2026-11-27"},  // Friday → today
		{time.Date(2026, 11, 28, 9, 0, 0, 0, loc), "2026-11-27"},  // Saturday → yesterday
		{time.Date(2026, 11, 29, 23, 0, 0, 0, loc), "2026-11-27"}, // Sunday → current weekend
		{time.Date(2026, 11, 30, 1, 0, 0, 0, loc), "2026-12-04"},  // Monday → next Friday
	} {
		from, to := weekendWindow(tc.now)
		if got := from.Format("2006-01-02"); got != tc.wantFrom {
			t.Errorf("%s: from %s, want %s", tc.now.Weekday(), got, tc.wantFrom)
		}
		if to.Sub(from) != 72*time.Hour {
			t.Errorf("%s: window %v, want 72h", tc.now.Weekday(), to.Sub(from))
		}
	}
}

func TestTodayAndWeekend(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, loc) // Friday noon
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, loc) }
	events := []Event{
		cityTestEvent(1, "Ball tonight", "Hall A", at(27, 20), at(27, 23)),      // today
		cityTestEvent(2, "Festival", "Castle", at(26, 10), at(29, 18)),          // running today
		cityTestEvent(3, "Saturday ball", "Hall B", at(28, 20), at(28, 23)),     // weekend
		cityTestEvent(4, "Next Tuesday", "Hall A", at(31, 20), at(31, 22)),      // neither (Dec 1 normalises)
		cityTestEvent(5, "Ended this morning", "Hall C", at(27, 1), at(27, 3)),  // over
		cityTestEvent(6, "Sunday session", "Pub", at(29, 15), at(29, 18)),       // weekend
		cityTestEvent(7, "Monday night", "Pub", at(30, 20), at(30, 23)),         // after the weekend
		cityTestEvent(8, "Late Sunday", "Pub", at(29, 23), at(30, 1)),           // weekend (starts Sunday)
		cityTestEvent(9, "Thursday", "Pub", at(26, 18), at(26, 20)),             // past
		cityTestEvent(10, "Saturday morning", "Hall A", at(28, 10), at(28, 12)), // weekend
	}
	upcoming := notEndedEvents(events, now)
	today, weekend := todayAndWeekend(upcoming, now)
	ids := func(es []Event) []int {
		var out []int
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}
	if got := ids(today); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("today = %v, want [1 2]", got)
	}
	if got := ids(weekend); len(got) != 4 || got[0] != 3 || got[1] != 6 || got[2] != 8 || got[3] != 10 {
		t.Errorf("weekend = %v, want [3 6 8 10]", got)
	}
}

func TestCityMetaDescriptionVariants(t *testing.T) {
	strs := loadI18n("").Strings("de")
	loc := time.FixedZone("CET", 3600)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, loc) // Tuesday
	at := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 20, 0, 0, 0, loc) }
	city := City{Town: "Köln", OrgCount: 3, LocationCount: 5}
	orgs := []cityOrgRef{{Name: "Alpha"}, {Name: "Beta"}, {Name: "Gamma"}}

	// No upcoming events: the old count wording.
	if got := cityMetaDescription(strs, "de", "de", city, nil, nil, nil, orgs); !strings.Contains(got, "3 Organisationen") {
		t.Errorf("no events: %q", got)
	}

	// Next dates, organisations named in front.
	next := []Event{
		cityTestEvent(1, "Ball", "Hall", at(12, 12), at(12, 12)),
		cityTestEvent(2, "Ball", "Hall", at(12, 19), at(12, 19)),
		cityTestEvent(3, "Ball", "Hall", at(12, 19), at(12, 19)), // same day, listed once
		cityTestEvent(4, "Ball", "Hall", at(1, 9).AddDate(1, 0, 0), at(1, 9).AddDate(1, 0, 0)),
		cityTestEvent(5, "Ball", "Hall", at(1, 16).AddDate(1, 0, 0), at(1, 16).AddDate(1, 0, 0)),
	}
	got := cityMetaDescription(strs, "de", "de", city, next, nil, nil, orgs)
	want := "Balfolk-Termine von Alpha, Beta und weitere in Köln: nächste Termine am 12.12.2026, 19.12.2026 und 09.01.2027"
	if got != want {
		t.Errorf("dates:\n got %q\nwant %q", got, want)
	}

	// Organisation names too long: dropped, the dates stay.
	long := []cityOrgRef{{Name: strings.Repeat("Sehr langer Vereinsname ", 4)}, {Name: strings.Repeat("Noch ein langer Name ", 4)}}
	got = cityMetaDescription(strs, "de", "de", city, next, nil, nil, long)
	if want := "Balfolk in Köln: nächste Termine am 12.12.2026, 19.12.2026 und 09.01.2027"; got != want {
		t.Errorf("long orgs:\n got %q\nwant %q", got, want)
	}

	// Today first; a cancelled event is not advertised.
	today := []Event{cityTestEvent(6, "Tanzabend", "Bürgerhaus", now, now)}
	cancelled := cityTestEvent(7, "Abgesagt", "Saal", now, now)
	cancelled.IsCancelled = true
	got = cityMetaDescription(strs, "de", "de", city, next, append(today, cancelled), nil, orgs)
	if want := "Heute Balfolk in Köln: Tanzabend, Bürgerhaus"; got != want {
		t.Errorf("today:\n got %q\nwant %q", got, want)
	}

	// Only a cancelled event today: fall through to the weekend.
	weekend := []Event{cityTestEvent(8, "Bal", "Saal", at(11, 28), at(11, 28)), cityTestEvent(9, "Session", "Kneipe", at(11, 29), at(11, 29))}
	got = cityMetaDescription(strs, "de", "de", city, next, []Event{cancelled}, weekend, orgs)
	if want := "Dieses Wochenende Balfolk in Köln: Bal, Saal und Session, Kneipe"; got != want {
		t.Errorf("weekend:\n got %q\nwant %q", got, want)
	}
}

func TestBuildCityIntro(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, loc)
	soon := now.AddDate(0, 0, 10)
	later := now.AddDate(0, 4, 0)
	events := []Event{
		cityTestEvent(1, "A", "Hall", soon, soon),
		cityTestEvent(2, "B", "Hall", soon, soon), // same venue name, other location id
		cityTestEvent(3, "C", "Pub", later, later),
	}
	in := buildCityIntro(events, nil, now)
	if in.SoonCount != 2 {
		t.Errorf("SoonCount = %d, want 2", in.SoonCount)
	}
	if len(in.Venues) != 2 || in.Venues[0] != "Hall" || in.Venues[1] != "Pub" {
		t.Errorf("Venues = %v", in.Venues)
	}
	if in.AgendaURL != "/agenda?location=101&location=102&location=103" {
		t.Errorf("AgendaURL = %q", in.AgendaURL)
	}
	if !in.HasContent {
		t.Error("HasContent = false")
	}
	if empty := buildCityIntro(nil, nil, now); empty.HasContent || empty.AgendaURL != "" {
		t.Errorf("empty intro: %+v", empty)
	}
}

func TestCityOrgs(t *testing.T) {
	all := []Organization{{ID: 1, Name: "zeta"}, {ID: 2, Name: "Alpha"}, {ID: 3, Name: "Beta", ActorName: "beta-ev"}, {ID: 4, Name: "Other"}}
	got := cityOrgs([]int{1, 2, 3}, all)
	if len(got) != 3 || got[0].Name != "Alpha" || got[1].Name != "Beta" || got[2].Name != "zeta" {
		t.Fatalf("order = %+v", got)
	}
	if got[1].Slug != "beta-ev" {
		t.Errorf("slug = %q, want the actor name", got[1].Slug)
	}
}

func TestEventPageTitleTownInTitle(t *testing.T) {
	e := Event{Title: "Folkfest Hohnstein", StartTime: "2026-06-12T18:00:00Z", Location: &Location{Town: "Hohnstein"}}
	if got := eventPageTitle(e, "de"); strings.Count(got, "Hohnstein") != 1 {
		t.Errorf("town repeated: %q", got)
	}
	e.Location.Town = "Dresden"
	if got := eventPageTitle(e, "de"); !strings.Contains(got, "– Dresden") {
		t.Errorf("town missing: %q", got)
	}
}
