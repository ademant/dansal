package main

import (
	"log"
	"regexp"
	"strconv"
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

// icalVTimezoneLocs maps the TZIDs a feed defines in its own VTIMEZONE
// components to fixed-offset locations.
//
// golang-ical resolves a TZID with time.LoadLocation and ignores VTIMEZONE
// entirely, so a feed using a custom identifier (Outlook/Exchange exports, some
// CMS plugins) fails to parse times that the feed itself fully specified. The
// data is reachable without forking: cal.Components holds every component, and
// *ics.VTimezone embeds ComponentBase so GetProperty is available on it.
//
// The offset is taken from the STANDARD sub-component only. A custom zone that
// observes DST will be an hour off for part of the year; handling that
// properly means evaluating each sub-component's RRULE, which re-implements
// timezone rule evaluation. Accepted for now because the alternative is
// dropping the event entirely.
func icalVTimezoneLocs(cal *ics.Calendar) map[string]*time.Location {
	if cal == nil {
		return nil
	}
	locs := map[string]*time.Location{}
	for _, c := range cal.Components {
		tz, ok := c.(*ics.VTimezone)
		if !ok {
			continue
		}
		prop := tz.GetProperty(ics.ComponentPropertyTzid)
		if prop == nil || prop.Value == "" {
			continue
		}
		// Prefer STANDARD; a DAYLIGHT-only VTIMEZONE still gives us an offset.
		var off string
		for _, sub := range tz.SubComponents() {
			g, ok := sub.(interface {
				GetProperty(ics.ComponentProperty) *ics.IANAProperty
			})
			if !ok {
				continue
			}
			p := g.GetProperty(ics.ComponentProperty(ics.PropertyTzoffsetto))
			if p == nil || p.Value == "" {
				continue
			}
			if _, isDaylight := sub.(*ics.Daylight); isDaylight && off != "" {
				continue
			}
			off = p.Value
		}
		if off == "" {
			continue
		}
		if secs, ok := parseICalUTCOffset(off); ok {
			locs[prop.Value] = time.FixedZone(prop.Value, secs)
		}
	}
	return locs
}

// icalUTCOffsetRe matches the RFC 5545 UTC-OFFSET form, e.g. "+0530" or "-08:00".
var icalUTCOffsetRe = regexp.MustCompile(`^([+-])(\d{1,2}):?(\d{2})$`)

// parseICalUTCOffset converts an RFC 5545 UTC-OFFSET value to seconds.
func parseICalUTCOffset(s string) (int, bool) {
	m := icalUTCOffsetRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	hh, err1 := strconv.Atoi(m[2])
	mm, err2 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || hh > 23 || mm > 59 {
		return 0, false
	}
	secs := hh*3600 + mm*60
	if m[1] == "-" {
		secs = -secs
	}
	return secs, true
}

// icalResolveTZID resolves a TZID to a location: the system IANA database first,
// so a feed that defines a zone Go already knows is unaffected, then the feed's
// own VTIMEZONE. ok is false when neither knows the identifier.
func icalResolveTZID(tzid string, vt map[string]*time.Location) (*time.Location, bool) {
	if tzid == "" {
		return nil, false
	}
	if loc, err := time.LoadLocation(tzid); err == nil {
		return loc, true
	}
	if loc, ok := vt[tzid]; ok {
		return loc, true
	}
	return nil, false
}

// icalWallClockLayouts are the shapes a DTSTART/DTEND value can take once the
// zone has been stripped off, mirroring what golang-ical's own parser accepts
// (with and without seconds, and bare all-day dates).
var icalWallClockLayouts = []string{
	"20060102T150405",
	"20060102T1504",
	"20060102",
}

// icalFallbackTime recovers a start/end time for an event whose TZID
// golang-ical could not resolve. The raw value is a wall clock plus an
// identifier, so the wall clock is re-anchored in the resolved location rather
// than converted — the same rule as a floating time in #1391, since a guessed
// instant must not inherit the host's offset either.
//
// usedFallback is true whenever the library could not have produced this itself,
// so callers can count and flag it.
func icalFallbackTime(p *ics.IANAProperty, vt map[string]*time.Location) (t time.Time, usedFallback bool, ok bool) {
	if p == nil {
		return time.Time{}, false, false
	}
	var tzid string
	if v, found := p.ICalParameters["TZID"]; found && len(v) > 0 {
		tzid = v[0]
	}
	loc, resolved := icalResolveTZID(tzid, vt)
	if !resolved {
		return time.Time{}, false, false
	}
	val := strings.TrimSpace(p.Value)
	for _, layout := range icalWallClockLayouts {
		if parsed, err := time.ParseInLocation(layout, val, loc); err == nil {
			return parsed, true, true
		}
	}
	return time.Time{}, false, false
}

// icalParseReport tallies events a parse could not turn into a request, so a
// degraded feed is visible instead of importing as if it were empty (#1392).
// Pass nil where the caller does not care; every method is nil-safe.
type icalParseReport struct {
	// TimezoneFallback counts events imported after their TZID was resolved
	// from the feed's VTIMEZONE or the instance zone.
	TimezoneFallback int
	// Unparsed counts events dropped for a reason other than time — a
	// malformed DTSTART, say. They are still dropped; there is no defensible
	// instant to invent, but they are no longer silent.
	Unparsed int
}

func (r *icalParseReport) fallback(uid, tzid string) {
	if r == nil {
		return
	}
	r.TimezoneFallback++
	if tzid != "" {
		log.Printf("iCal: event %q has unresolvable TZID %q; anchored its start time in the feed's VTIMEZONE or the instance zone", uid, tzid)
	} else {
		log.Printf("iCal: event %q has an unresolvable timezone; anchored its start time in the feed's VTIMEZONE or the instance zone", uid)
	}
}

func (r *icalParseReport) unparsed(uid string, reason any) {
	if r == nil {
		return
	}
	r.Unparsed++
	log.Printf("iCal: skipping event %q: %v", uid, reason)
}

// fold adds the report into the import counters returned to the admin.
func (r *icalParseReport) fold(c *ImportCounts) {
	if r == nil || c == nil {
		return
	}
	c.TimezoneFallback += r.TimezoneFallback
	c.Unparsed += r.Unparsed
}

// icalTZIDOf returns the TZID parameter of a raw DTSTART/DTEND property, or ""
// when the value carries no zone identifier.
func icalTZIDOf(p *ics.IANAProperty) string {
	if p == nil {
		return ""
	}
	if v, ok := p.ICalParameters["TZID"]; ok && len(v) > 0 {
		return v[0]
	}
	return ""
}
