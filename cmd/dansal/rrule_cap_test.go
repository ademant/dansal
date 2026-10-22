package main

import (
	"strings"
	"testing"
	"time"

	ics "github.com/arran4/golang-ical"
)

// A daily rule runs for three years on import, far past the cap (#1578).
// The expansion must stop at the cap, not keep going, and the capped
// occurrences must be the first ones in order.
func TestExpandRRuleStopsAtCap(t *testing.T) {
	prev := icalMaxOccurrences
	icalMaxOccurrences = 25
	t.Cleanup(func() { icalMaxOccurrences = prev })

	cal, err := ics.ParseCalendar(strings.NewReader(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//dansal//test//EN
BEGIN:VEVENT
UID:daily-forever
SUMMARY:Daily
DTSTART:20261101T200000Z
DTEND:20261101T220000Z
RRULE:FREQ=DAILY
END:VEVENT
END:VCALENDAR`))
	if err != nil {
		t.Fatal(err)
	}
	vevent := cal.Events()[0]
	base, err := time.Parse(time.RFC3339, "2026-11-01T20:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	occs, err := expandRRuleOccurrences(vevent, base, base.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(occs) != 25 {
		t.Fatalf("expanded to %d occurrences, want the cap of 25", len(occs))
	}
	if !occs[0][0].Equal(base) {
		t.Errorf("first occurrence %v, want the base %v", occs[0][0], base)
	}
}

func TestExpandRRuleDefaultCapIsGenerous(t *testing.T) {
	if defaultIcalMaxOccurrences < 365 {
		t.Errorf("default cap %d is too low for a yearly-visible series", defaultIcalMaxOccurrences)
	}
}
