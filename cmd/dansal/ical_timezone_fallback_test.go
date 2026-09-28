package main

// Events with a TZID that Go cannot resolve used to be dropped from iCal/jCal
// imports with no error, no warning and no counter, so a feed shipping a custom
// VTIMEZONE imported as if it were empty or partial (#1392).

import (
	"strings"
	"testing"
	"time"

	ics "github.com/arran4/golang-ical"
)

const customTZFeed = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//dansal//test//EN
CALSCALE:GREGORIAN
BEGIN:VTIMEZONE
TZID:Customized Time Zone
BEGIN:STANDARD
DTSTART:19700101T000000
TZOFFSETFROM:+0530
TZOFFSETTO:+0530
TZNAME:Customized Time Zone
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:custom-tz-1
SUMMARY:Custom TZ event
DTSTAMP:20260926T194910Z
DTSTART;TZID=Customized Time Zone:20311101T200000
DTEND;TZID=Customized Time Zone:20311101T230000
END:VEVENT
BEGIN:VEVENT
UID:iana-tz-1
SUMMARY:IANA TZ event
DTSTAMP:20260926T194910Z
DTSTART;TZID=Europe/Berlin:20311101T200000
DTEND;TZID=Europe/Berlin:20311101T230000
END:VEVENT
END:VCALENDAR`

func TestICalVTimezoneLocs(t *testing.T) {
	cal, err := ics.ParseCalendar(strings.NewReader(customTZFeed))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	locs := icalVTimezoneLocs(cal)
	loc, ok := locs["Customized Time Zone"]
	if !ok {
		t.Fatalf("VTIMEZONE not resolved, got %v", locs)
	}
	if _, want := time.FixedZone("x", 5*3600+30*60), true; loc.String() == "" || !want {
		t.Fatal("unreachable")
	}
	// +05:30 -> 20:00 local is 14:30 UTC.
	got := time.Date(2031, 11, 1, 20, 0, 0, 0, loc).UTC()
	if got.Format(time.RFC3339) != "2031-11-01T14:30:00Z" {
		t.Errorf("offset = %s, want 2031-11-01T14:30:00Z", got.Format(time.RFC3339))
	}
}

func TestParseICalUTCOffset(t *testing.T) {
	for _, tc := range []struct {
		in   string
		secs int
		ok   bool
	}{
		{"+0530", 19800, true},
		{"+05:30", 19800, true},
		{"-0800", -28800, true},
		{"+0000", 0, true},
		{"+2400", 0, false},
		{"+0560", 0, false},
		{"nonsense", 0, false},
		{"", 0, false},
	} {
		secs, ok := parseICalUTCOffset(tc.in)
		if ok != tc.ok || secs != tc.secs {
			t.Errorf("parseICalUTCOffset(%q) = (%d, %v), want (%d, %v)", tc.in, secs, ok, tc.secs, tc.ok)
		}
	}
}

// The regression: a feed whose only event uses a custom TZID imported 0 events
// and reported success.
func TestICalCustomTZIDIsImportedNotDropped(t *testing.T) {
	withInstanceZone(t)
	src := FetchSource{URL: "https://example.org/cal.ics", ID: 1}
	rep := &icalParseReport{}

	entries, err := parseICalBody([]byte(customTZFeed), src, rep)
	if err != nil {
		t.Fatalf("parseICalBody: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (custom + IANA)", len(entries))
	}
	if rep.TimezoneFallback != 1 {
		t.Errorf("TimezoneFallback = %d, want 1", rep.TimezoneFallback)
	}
	if rep.Unparsed != 0 {
		t.Errorf("Unparsed = %d, want 0", rep.Unparsed)
	}

	byUID := map[string]string{}
	for _, e := range entries {
		byUID[e.req.UID] = e.req.StartTime
	}
	// 20:00 at +05:30 is 14:30 UTC, which renders as 15:30 in the instance
	// zone — the same string form the IANA event uses, since both are stored as
	// absolute instants and rendered in the instance zone.
	if got, want := byUID["custom-tz-1"], "2031-11-01T15:30:00+01:00"; got != want {
		t.Errorf("custom TZID start = %s, want %s", got, want)
	}
	if got, want := byUID["iana-tz-1"], "2031-11-01T20:00:00+01:00"; got != want {
		t.Errorf("IANA TZID start = %s, want %s", got, want)
	}
}

// An unknown TZID with no VTIMEZONE to fall back on must not be invented: the
// event is dropped, but now visibly.
func TestICalUnknownTZIDIsCountedNotSilentlyDropped(t *testing.T) {
	body := strings.Replace(customTZFeed, "Customized Time Zone:20311101", "No Such Zone:20311101", 1)
	src := FetchSource{URL: "https://example.org/cal.ics", ID: 1}
	rep := &icalParseReport{}

	entries, err := parseICalBody([]byte(body), src, rep)
	if err != nil {
		t.Fatalf("parseICalBody: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (IANA only)", len(entries))
	}
	if rep.Unparsed != 1 {
		t.Errorf("Unparsed = %d, want 1 (previously silent)", rep.Unparsed)
	}
	if rep.TimezoneFallback != 0 {
		t.Errorf("TimezoneFallback = %d, want 0", rep.TimezoneFallback)
	}
}

// An IANA TZID that the feed also defines must still resolve through the system
// database, not the feed's offset.
func TestICalResolveTZIDPrefersSystem(t *testing.T) {
	feed := strings.Replace(customTZFeed, "TZID:Customized Time Zone", "TZID:Europe/Berlin", 1)
	feed = strings.Replace(feed, ";TZID=Customized Time Zone:", ";TZID=Europe/Berlin:", -1)
	cal, err := ics.ParseCalendar(strings.NewReader(feed))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	loc, ok := icalResolveTZID("Europe/Berlin", icalVTimezoneLocs(cal))
	if !ok {
		t.Fatal("Europe/Berlin should resolve")
	}
	// A system location knows its DST rules; a fixed one would not.
	if loc.String() == "Europe/Berlin" {
		return
	}
	t.Errorf("resolved to fixed zone %q; the system database should win", loc)
}

// The preview must flag exactly the events whose time was guessed, and agree
// with what the import would store.
func TestICalPreviewFlagsFallbackAndMatchesImport(t *testing.T) {
	withInstanceZone(t)
	src := FetchSource{URL: "https://example.org/cal.ics", ID: 1}

	cal, err := ics.ParseCalendar(strings.NewReader(customTZFeed))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rep := &icalParseReport{}
	preview := parseICalToRequests(cal, src, rep)

	entries, err := parseICalBody([]byte(customTZFeed), src, &icalParseReport{})
	if err != nil {
		t.Fatalf("parseICalBody: %v", err)
	}
	if len(preview) != len(entries) {
		t.Fatalf("preview %d vs import %d", len(preview), len(entries))
	}

	flagged := 0
	for i := range preview {
		if preview[i].TimezoneFallback {
			flagged++
		}
		if preview[i].StartTime != entries[i].req.StartTime {
			t.Errorf("%s: preview %s != import %s", preview[i].UID, preview[i].StartTime, entries[i].req.StartTime)
		}
	}
	if flagged != 1 {
		t.Errorf("flagged %d preview events, want 1", flagged)
	}
	if rep.TimezoneFallback != 1 {
		t.Errorf("preview report TimezoneFallback = %d, want 1", rep.TimezoneFallback)
	}
}

func TestICalParseReportNilSafe(t *testing.T) {
	var rep *icalParseReport
	rep.fallback("u", "tz")
	rep.unparsed("u", "boom")
	var counts ImportCounts
	rep.fold(&counts)
	if counts.TimezoneFallback != 0 || counts.Unparsed != 0 {
		t.Error("nil report should be a no-op")
	}

	real := &icalParseReport{TimezoneFallback: 2, Unparsed: 3}
	real.fold(&counts)
	if counts.TimezoneFallback != 2 || counts.Unparsed != 3 {
		t.Errorf("fold = %+v, want 2/3", counts)
	}
}

// A VTIMEZONE that only defines DAYLIGHT must still yield an offset.
func TestICalVTimezoneDaylightOnly(t *testing.T) {
	feed := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//dansal//test//EN
BEGIN:VTIMEZONE
TZID:Daylight Only Zone
BEGIN:DAYLIGHT
DTSTART:19700329T020000
TZOFFSETFROM:+0000
TZOFFSETTO:+0200
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU
END:DAYLIGHT
END:VTIMEZONE
BEGIN:VEVENT
UID:do-1
SUMMARY:Daylight only
DTSTART;TZID=Daylight Only Zone:20311101T200000
END:VEVENT
END:VCALENDAR`
	cal, err := ics.ParseCalendar(strings.NewReader(feed))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	loc, ok := icalVTimezoneLocs(cal)["Daylight Only Zone"]
	if !ok {
		t.Fatal("daylight-only VTIMEZONE not resolved")
	}
	if got := time.Date(2031, 11, 1, 20, 0, 0, 0, loc).UTC().Format(time.RFC3339); got != "2031-11-01T18:00:00Z" {
		t.Errorf("offset = %s, want 2031-11-01T18:00:00Z", got)
	}
}
