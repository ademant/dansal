package main

import (
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
)

// instanceLoc returns the zone every event time is anchored to.
//
// events.start_time is an INTEGER epoch and epochToLocal renders it through
// this zone, so an event always displays as its instance-local wall clock.
// Making the anchoring zone a package-level variable keeps it injectable in
// tests instead of forcing every helper to dereference a nil berlinLoc.
var instanceLoc = func() *time.Location {
	if berlinLoc != nil {
		return berlinLoc
	}
	// berlinLoc is populated from config during start-up. Fall back to UTC so
	// helpers stay safe (and testable) when they run before that or in a
	// bare test binary.
	return time.UTC
}

// icalTimeIsFloating reports whether a raw DTSTART/DTEND value is a floating
// timestamp in the RFC 5545 §3.3.5 sense: neither UTC (a trailing Z) nor
// attached to a zone (a TZID parameter). All-day DATE values have the same
// shape — a bare calendar date with no zone — and are floating too.
//
// The distinction has to be read off the raw property rather than inferred
// from the parsed time.Time, because golang-ical resolves both floating and
// zoned values into a concrete time.Time and erases the difference.
func icalTimeIsFloating(p *ics.IANAProperty) bool {
	if p == nil {
		// No property to inspect (e.g. DTEND derived from DURATION). Assume the
		// value is absolute; a wrong guess here is inert because the caller
		// falls back to the instance zone anyway.
		return false
	}
	if _, zoned := p.ICalParameters["TZID"]; zoned {
		return false
	}
	return !strings.HasSuffix(strings.TrimSpace(p.Value), "Z")
}

// icalOccurrenceTime resolves one parsed occurrence to the instant dansal
// stores for it.
//
// RFC 5545 gives DTSTART three forms and only two of them denote an instant:
//
//   - UTC (…Z) and zoned (;TZID=…) values are absolute. Re-expressing them in
//     the instance zone preserves the instant, so the event keeps the wall
//     clock its publisher intended.
//   - Floating values are a bare wall clock with no zone at all. golang-ical
//     resolves them in time.Local (see parseTimeValue in golang-ical's
//     components.go), so the resulting instant silently depends on the host's
//     TZ. Converting such a value — as the import path did with .UTC() — bakes
//     the host's offset into the stored epoch and shifts the event by that
//     much for every reader. A floating value must instead be re-anchored:
//     keep the wall-clock fields and interpret them in the instance zone.
//
// Re-anchoring is what makes imports reproducible: the same feed yields the
// same stored epoch on a UTC host, a Berlin host, or anywhere else.
//
// The zone-ness of DTSTART applies to every RRULE occurrence, so callers read
// the flags once per VEVENT and pass them down for each expansion.
func icalOccurrenceTime(t time.Time, floating bool, loc *time.Location) time.Time {
	if !floating {
		return t.In(loc)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc)
}

// icalTimeZones reports whether a VEVENT's DTSTART and DTEND are floating.
// DTEND may legally differ from DTSTART, and when it is absent the end is
// derived from DTSTART (or DURATION) and so inherits its zone-ness.
func icalTimeZones(vevent *ics.VEvent) (startFloating, endFloating bool) {
	if vevent == nil {
		return false, false
	}
	startFloating = icalTimeIsFloating(vevent.GetProperty(ics.ComponentPropertyDtStart))
	endP := vevent.GetProperty(ics.ComponentPropertyDtEnd)
	if endP == nil {
		return startFloating, startFloating
	}
	return startFloating, icalTimeIsFloating(endP)
}
