package main

import (
	"strings"
	"testing"
	"time"

	ics "github.com/arran4/golang-ical"
)

// Floating iCal timestamps are bare wall clocks, but golang-ical resolves them
// in time.Local, so the stored instant used to depend on the host's TZ and the
// preview and import paths disagreed (#1391).

// withHostTZ points time.Local at loc for the duration of the test, mimicking a
// dansal instance running in a different zone from the developer machine.
func withHostTZ(t *testing.T, name string) {
	t.Helper()
	prev := time.Local
	tl, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	time.Local = tl
	t.Cleanup(func() { time.Local = prev })
}

func berlin() *time.Location {
	l, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		panic(err)
	}
	return l
}

func TestICalTimeIsFloating(t *testing.T) {
	// Three DTSTART forms from RFC 5545 §3.3.5 plus an all-day DATE.
	cal, err := ics.ParseCalendar(strings.NewReader(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//dansal//test//EN
BEGIN:VEVENT
UID:floating
SUMMARY:Floating
DTSTART:20261101T200000
END:VEVENT
BEGIN:VEVENT
UID:utc
SUMMARY:UTC
DTSTART:20261101T200000Z
END:VEVENT
BEGIN:VEVENT
UID:zoned
SUMMARY:Zoned
DTSTART;TZID=Europe/Berlin:20261101T200000
END:VEVENT
BEGIN:VEVENT
UID:allday
SUMMARY:All day
DTSTART;VALUE=DATE:20261101
END:VEVENT
END:VCALENDAR`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	want := map[string]bool{
		"floating": true,
		"utc":      false,
		"zoned":    false,
		"allday":   true,
	}

	seen := 0
	for _, ev := range cal.Events() {
		uid := ev.GetProperty(ics.ComponentPropertyUniqueId).Value
		got, _ := icalTimeZones(ev)
		if got != want[uid] {
			t.Errorf("%s: icalTimeZones start floating = %v, want %v", uid, got, want[uid])
		}
		seen++
	}
	if seen != len(want) {
		t.Fatalf("saw %d events, want %d", seen, len(want))
	}
}

// A floating value must land on the same instant regardless of the host TZ.
func TestICalOccurrenceTimeFloatingIsHostIndependent(t *testing.T) {
	const layout = "20060102T150405"

	// The dependency is the Location golang-ical attaches to a floating value,
	// so rebuild the parse per simulated host zone.
	var first int64
	for i, host := range []string{"UTC", "Europe/Berlin", "America/New_York", "Asia/Tokyo"} {
		withHostTZ(t, host)
		loc, err := time.LoadLocation(host)
		if err != nil {
			t.Fatalf("load %s: %v", host, err)
		}
		hostParsed, err := time.ParseInLocation(layout, "20261101T200000", loc)
		if err != nil {
			t.Fatalf("parse in %s: %v", host, err)
		}

		got := icalOccurrenceTime(hostParsed, true, berlin())
		if got.Format(layout) != "20261101T200000" {
			t.Errorf("host %s: wall clock = %s, want 20261101T200000", host, got.Format(layout))
		}
		if i == 0 {
			first = got.Unix()
		} else if got.Unix() != first {
			t.Errorf("host %s: epoch = %d, want %d (host-dependent import)", host, got.Unix(), first)
		}
	}
}

// Absolute values denote an instant already: re-expressing them in the
// instance zone must not shift the event.
func TestICalOccurrenceTimeAbsoluteIsPreserved(t *testing.T) {
	loc := berlin()

	utc, err := time.Parse(time.RFC3339, "2026-11-01T20:00:00Z")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := icalOccurrenceTime(utc, false, loc); !got.Equal(utc) {
		t.Errorf("UTC value shifted: got %s, want %s", got, utc)
	}
	if got := icalOccurrenceTime(utc, false, loc); got.Hour() != 21 {
		t.Errorf("UTC value should render as 21:00 Berlin (CET), got %02d:00", got.Hour())
	}

	// A 19:00 Berlin event is 18:00Z; the stored instant must stay 19:00 local.
	berlinStart := time.Date(2026, 11, 1, 19, 0, 0, 0, loc)
	got := icalOccurrenceTime(berlinStart, false, loc)
	if !got.Equal(berlinStart) {
		t.Errorf("Berlin value shifted: got %s, want %s", got, berlinStart)
	}
	if got.Hour() != 19 {
		t.Errorf("Berlin value should stay 19:00, got %02d:00", got.Hour())
	}
}

// The bug that made a floating 20:00 event land at 19:00: summer CEST is
// +02:00, winter CET is +01:00. Re-anchoring must follow DST, not a fixed
// offset, or events slip by an hour twice a year.
func TestICalOccurrenceTimeFloatingRespectsDST(t *testing.T) {
	loc := berlin()
	for _, tc := range []struct {
		name     string
		wall     string
		wantHour int
	}{
		{"summer CEST +02:00", "20260601T200000", 20},
		{"winter CET +01:00", "20261101T200000", 20},
	} {
		// Parse the way golang-ical does: in the host's local zone.
		hostParsed, err := time.ParseInLocation("20060102T150405", tc.wall, time.UTC)
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		got := icalOccurrenceTime(hostParsed, true, loc)
		if got.Hour() != tc.wantHour {
			t.Errorf("%s: hour = %d, want %d", tc.name, got.Hour(), tc.wantHour)
		}
		if got.Format(time.RFC3339) != "2026-06-01T20:00:00+02:00" && got.Format(time.RFC3339) != "2026-11-01T20:00:00+01:00" {
			t.Errorf("%s: formatted as %s, want a +02:00/+01:00 offset", tc.name, got.Format(time.RFC3339))
		}
	}
}

func TestICalTimeZonesDTENDInheritsDTSTART(t *testing.T) {
	cal, err := ics.ParseCalendar(strings.NewReader(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//dansal//test//EN
BEGIN:VEVENT
UID:nodtend
SUMMARY:Duration only
DTSTART:20261101T200000
DURATION:PT2H
END:VEVENT
BEGIN:VEVENT
UID:mixed
SUMMARY:Mixed zones
DTSTART;TZID=Europe/Berlin:20261101T200000
DTEND:20261101T230000
END:VEVENT
END:VCALENDAR`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	want := map[string][2]bool{
		"nodtend": {true, true},  // end derived from DURATION inherits DTSTART
		"mixed":   {false, true}, // DTEND may be floating while DTSTART is zoned
	}
	for _, ev := range cal.Events() {
		uid := ev.GetProperty(ics.ComponentPropertyUniqueId).Value
		s, e := icalTimeZones(ev)
		if s != want[uid][0] || e != want[uid][1] {
			t.Errorf("%s: got (start=%v end=%v), want (start=%v end=%v)",
				uid, s, e, want[uid][0], want[uid][1])
		}
	}
}

// withInstanceZone points the instance zone at Europe/Berlin for the test.
// instanceTimezone is normally populated from config during start-up.
func withInstanceZone(t *testing.T) {
	t.Helper()
	prev := instanceTimezone
	instanceTimezone = berlin()
	t.Cleanup(func() { instanceTimezone = prev })
}

// The regression itself: parseICalToRequests (preview) and parseICalBody
// (import) resolved the same VEVENT differently, so the time an admin approved
// in the preview was not the time that got stored.
func TestICalPreviewAndImportAgreeOnFloatingTime(t *testing.T) {
	const body = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//dansal//test//EN
CALSCALE:GREGORIAN
BEGIN:VEVENT
UID:floating-2031
SUMMARY:Floating ball
DTSTAMP:20260926T194910Z
DTSTART:20311101T200000
DTEND:20311101T230000
LOCATION:Saal
END:VEVENT
BEGIN:VEVENT
UID:utc-2031
SUMMARY:UTC ball
DTSTAMP:20260926T194910Z
DTSTART:20311101T200000Z
DTEND:20311101T230000Z
END:VEVENT
END:VCALENDAR`

	withInstanceZone(t)
	src := FetchSource{URL: "https://example.org/cal.ics", ID: 1}

	cal, err := ics.ParseCalendar(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	preview := parseICalToRequests(cal, src, nil)

	entries, err := parseICalBody([]byte(body), src, nil)
	if err != nil {
		t.Fatalf("parseICalBody: %v", err)
	}

	if len(preview) != 2 || len(entries) != 2 {
		t.Fatalf("got %d preview / %d import entries, want 2 each", len(preview), len(entries))
	}

	// Floating: 20:00 wall clock anchored in Berlin (CET, +01:00) in November.
	// UTC: the same instant rendered in the instance zone, i.e. 21:00 Berlin —
	// what the event will actually display. The stored epoch is unchanged
	// either way, since parseTimeToUnix normalises the offset away.
	want := map[string]string{
		"floating-2031": "2031-11-01T20:00:00+01:00",
		"utc-2031":      "2031-11-01T21:00:00+01:00",
	}
	for i, uid := range []string{"floating-2031", "utc-2031"} {
		p, im := preview[i].StartTime, entries[i].req.StartTime
		if p != im {
			t.Errorf("%s: preview %s != import %s", uid, p, im)
		}
		if p != want[uid] {
			t.Errorf("%s: start = %s, want %s", uid, p, want[uid])
		}
		if pEnd, iEnd := preview[i].EndTime, entries[i].req.EndTime; pEnd != iEnd {
			t.Errorf("%s: end preview %s != import %s", uid, pEnd, iEnd)
		}
	}
}

// A floating event must store the same epoch no matter where the instance runs.
func TestICalFloatingImportIsHostIndependent(t *testing.T) {
	const body = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//dansal//test//EN
BEGIN:VEVENT
UID:floating-2031
SUMMARY:Floating ball
DTSTAMP:20260926T194910Z
DTSTART:20311101T200000
DTEND:20311101T230000
END:VEVENT
END:VCALENDAR`

	withInstanceZone(t)
	src := FetchSource{URL: "https://example.org/cal.ics", ID: 1}

	var first string
	for i, host := range []string{"UTC", "Europe/Berlin", "America/New_York", "Asia/Tokyo"} {
		withHostTZ(t, host)
		entries, err := parseICalBody([]byte(body), src, nil)
		if err != nil {
			t.Fatalf("%s: parseICalBody: %v", host, err)
		}
		if len(entries) != 1 {
			t.Fatalf("%s: got %d entries, want 1", host, len(entries))
		}
		got := entries[0].req.StartTime
		if got != "2031-11-01T20:00:00+01:00" {
			t.Errorf("host %s: start = %s, want 2031-11-01T20:00:00+01:00", host, got)
		}
		if i == 0 {
			first = got
		} else if got != first {
			t.Errorf("host %s: start = %s, want %s (host-dependent import)", host, got, first)
		}
	}
}
