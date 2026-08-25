package main

import (
	"encoding/json"
	"strings"
	"testing"

	ics "github.com/arran4/golang-ical"
)

// TestJCalEncodeRoundTrip mirrors what getEvents/getEvent actually build
// via buildEventsCalendar: a simple VEVENT with UID/SUMMARY/DTSTART/DTEND/
// LOCATION. icalTextToJCal's output must, once fed back through
// jcalToICalText + ics.ParseCalendar, reproduce the same fields.
func TestJCalEncodeRoundTrip(t *testing.T) {
	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	vevent := cal.AddEvent("event-42@go-calendar")
	vevent.SetSummary("Wuppertaler Balfolk-Herbst")
	vevent.SetDescription("weitere Termininfos, siehe: https://example.com")
	vevent.SetProperty(ics.ComponentPropertyDtStart, "20260925T190000Z")
	vevent.SetProperty(ics.ComponentPropertyDtEnd, "20260925T233000Z")
	vevent.SetLocation("Färberei, Peter-Hansen-Platz 1, 42275 Wuppertal")

	jcal, err := icalTextToJCal(cal.Serialize())
	if err != nil {
		t.Fatalf("icalTextToJCal: %v", err)
	}

	// Must be valid, well-shaped jCal: ["vcalendar", [...props], [components]].
	var root []any
	if err := json.Unmarshal(jcal, &root); err != nil {
		t.Fatalf("jCal output is not valid JSON: %v", err)
	}
	if len(root) != 3 || root[0] != "vcalendar" {
		t.Fatalf("expected top-level [\"vcalendar\", props, components], got %v", root)
	}

	icsText, err := jcalToICalText(jcal)
	if err != nil {
		t.Fatalf("jcalToICalText: %v", err)
	}
	roundTripped, err := ics.ParseCalendar(strings.NewReader(icsText))
	if err != nil {
		t.Fatalf("ics.ParseCalendar on round-tripped text: %v\n---\n%s", err, icsText)
	}
	events := roundTripped.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 VEVENT after round-trip, got %d", len(events))
	}
	ev := events[0]

	if got := ev.GetProperty(ics.ComponentPropertySummary).Value; got != "Wuppertaler Balfolk-Herbst" {
		t.Errorf("SUMMARY = %q", got)
	}
	if got := ev.GetProperty(ics.ComponentPropertyDescription).Value; got != "weitere Termininfos, siehe: https://example.com" {
		t.Errorf("DESCRIPTION = %q", got)
	}
	if got := ev.GetProperty(ics.ComponentPropertyLocation).Value; got != "Färberei, Peter-Hansen-Platz 1, 42275 Wuppertal" {
		t.Errorf("LOCATION = %q", got)
	}
	start, err := ev.GetStartAt()
	if err != nil {
		t.Fatalf("GetStartAt: %v", err)
	}
	if got := start.UTC().Format("20060102T150405Z"); got != "20260925T190000Z" {
		t.Errorf("DTSTART = %q", got)
	}
	end, err := ev.GetEndAt()
	if err != nil {
		t.Fatalf("GetEndAt: %v", err)
	}
	if got := end.UTC().Format("20060102T150405Z"); got != "20260925T233000Z" {
		t.Errorf("DTEND = %q", got)
	}
}

// TestJCalDecodeFromExternalProducer parses a hand-written jCal document, as
// an external client (not dansal itself) would send it to
// POST /api/v1/events with Content-Type: application/calendar+json, and
// checks it converts into iCal text that ics.ParseCalendar can consume with
// the expected values — including CATEGORIES (a text-list property) and
// GEO (the float-pair special case).
func TestJCalDecodeFromExternalProducer(t *testing.T) {
	jcal := []byte(`["vcalendar",
		[
			["prodid", {}, "text", "-//Example//EN"],
			["version", {}, "text", "2.0"]
		],
		[
			["vevent",
				[
					["uid", {}, "text", "abc-123"],
					["summary", {}, "text", "Fest-noz à Rennes"],
					["dtstart", {}, "date-time", "2026-10-03T20:00:00Z"],
					["dtend", {}, "date-time", "2026-10-04T02:00:00Z"],
					["categories", {}, "text", "fest-noz", "concert"],
					["geo", {}, "float", [48.117, -1.677]]
				],
				[]
			]
		]
	]`)

	icsText, err := jcalToICalText(jcal)
	if err != nil {
		t.Fatalf("jcalToICalText: %v", err)
	}
	cal, err := ics.ParseCalendar(strings.NewReader(icsText))
	if err != nil {
		t.Fatalf("ics.ParseCalendar: %v\n---\n%s", err, icsText)
	}
	events := cal.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 VEVENT, got %d", len(events))
	}
	ev := events[0]

	if got := ev.GetProperty(ics.ComponentPropertySummary).Value; got != "Fest-noz à Rennes" {
		t.Errorf("SUMMARY = %q", got)
	}
	if got := ev.GetProperty(ics.ComponentPropertyUniqueId).Value; got != "abc-123" {
		t.Errorf("UID = %q", got)
	}
	cats := parseICalCategories(ev)
	if len(cats) != 2 || cats[0] != "fest-noz" || cats[1] != "concert" {
		t.Errorf("CATEGORIES = %v", cats)
	}
	geo := ev.GetProperty(ics.ComponentPropertyGeo)
	if geo == nil {
		t.Fatalf("GEO property missing")
	}
	lat, lon := parseICalGeo(geo.Value)
	if lat == nil || lon == nil || *lat != 48.117 || *lon != -1.677 {
		t.Errorf("GEO = %v", geo.Value)
	}
	start, err := ev.GetStartAt()
	if err != nil {
		t.Fatalf("GetStartAt: %v", err)
	}
	if got := start.UTC().Format("20060102T150405Z"); got != "20261003T200000Z" {
		t.Errorf("DTSTART = %q", got)
	}
}

// TestJCalDecodeRejectsMalformed checks jcalToICalText surfaces a clean
// error (used to produce a 400, not a panic) on structurally invalid input.
func TestJCalDecodeRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"not json", `not json at all`},
		{"not an array", `{"foo":"bar"}`},
		{"wrong arity", `["vcalendar", []]`},
		{"property not array", `["vcalendar", ["not-an-array"], []]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := jcalToICalText([]byte(c.body)); err == nil {
				t.Errorf("expected an error for %q, got nil", c.body)
			}
		})
	}
}

// TestJCalTextListRoundTrip checks CATEGORIES (a multi-valued text
// property) survives icalTextToJCal -> jcalToICalText with each value kept
// distinct.
func TestJCalTextListRoundTrip(t *testing.T) {
	vevent := ics.NewEvent("cat-test@go-calendar")
	vevent.SetSummary("Categories test")
	vevent.SetProperty(ics.ComponentPropertyDtStart, "20260101T000000Z")
	vevent.SetProperty(ics.ComponentPropertyCategories, "bal-folk,workshop,session")

	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	cal.Components = append(cal.Components, vevent)

	jcal, err := icalTextToJCal(cal.Serialize())
	if err != nil {
		t.Fatalf("icalTextToJCal: %v", err)
	}
	icsText, err := jcalToICalText(jcal)
	if err != nil {
		t.Fatalf("jcalToICalText: %v", err)
	}
	roundTripped, err := ics.ParseCalendar(strings.NewReader(icsText))
	if err != nil {
		t.Fatalf("ics.ParseCalendar: %v\n---\n%s", err, icsText)
	}
	cats := parseICalCategories(roundTripped.Events()[0])
	if len(cats) != 3 || cats[0] != "bal-folk" || cats[1] != "workshop" || cats[2] != "session" {
		t.Errorf("CATEGORIES round-trip = %v", cats)
	}
}
