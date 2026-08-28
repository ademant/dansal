package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
)

// timetableEntryStartEnd resolves one timetable entry's absolute start/end
// instants by combining its own HH:MM clock (and optional entry_date) with
// the parent event's own StartTime for both the date fallback (same rule
// timetableDays in templatefuncs_time.go already uses for day-grouping) and
// the timezone/offset — entries only carry wall-clock time, no zone of
// their own. An end time not after the start is treated as crossing
// midnight (e.g. a fest-noz running 22:00–02:00).
func timetableEntryStartEnd(entry TimetableEntry, eventStartRFC3339 string) (start, end time.Time, ok bool) {
	ref, err := time.Parse(time.RFC3339, eventStartRFC3339)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	dateStr := strings.TrimSpace(entry.EntryDate)
	if dateStr == "" {
		dateStr = ref.Format("2006-01-02")
	}
	sMin, sOk := parseTimetableClock(entry.StartTime)
	eMin, eOk := parseTimetableClock(entry.EndTime)
	if !sOk || !eOk {
		return time.Time{}, time.Time{}, false
	}
	base, err := time.ParseInLocation("2006-01-02", dateStr, ref.Location())
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	endMin := eMin
	if endMin <= sMin {
		endMin += 24 * 60
	}
	start = base.Add(time.Duration(sMin) * time.Minute)
	end = base.Add(time.Duration(endMin) * time.Minute)
	return start, end, true
}

// addTimetableEntryToCalendar appends one VEVENT for a single timetable
// entry — the per-entry counterpart to feedAddEventToCalendar's
// whole-event VEVENT. Shared by the starred-entries export (#1177) and,
// per #1178, the flat timetable export family.
func addTimetableEntryToCalendar(cal *ics.Calendar, domain string, event Event, entry TimetableEntry) {
	start, end, ok := timetableEntryStartEnd(entry, event.StartTime)
	if !ok {
		return
	}
	vevent := cal.AddEvent(fmt.Sprintf("event-%d-tt-%d@%s", event.ID, entry.ID, domain))
	vevent.SetSummary(entry.Title)
	if entry.Description != "" {
		vevent.SetDescription(entry.Description)
	}
	vevent.SetProperty(ics.ComponentPropertyDtStart, start.UTC().Format("20060102T150405Z"))
	vevent.SetProperty(ics.ComponentPropertyDtEnd, end.UTC().Format("20060102T150405Z"))
	loc := entry.LocationName
	if loc == "" {
		loc = entry.Room
	}
	if loc != "" {
		vevent.SetLocation(loc)
	}
	vevent.SetProperty(ics.ComponentPropertyUrl, fmt.Sprintf("https://%s/events/%d", domain, event.ID))
}

// filterTimetableEntries returns entries whose ID appears in a
// comma-separated "entries" query param (e.g. "12,45,67"), or entries
// unchanged when the param is absent/empty — used both by the starred-entries
// export (#1177, always filtered) and the flat timetable export (#1178,
// filter optional).
func filterTimetableEntries(entries []TimetableEntry, raw string) []TimetableEntry {
	if raw == "" {
		return entries
	}
	want := make(map[int]bool)
	for _, s := range strings.Split(raw, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			want[n] = true
		}
	}
	filtered := make([]TimetableEntry, 0, len(entries))
	for _, e := range entries {
		if want[e.ID] {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// feedEventTimetableICSHandler serves GET /events/{id}/timetable.ics — one
// VEVENT per timetable entry. With no ?entries= param, every entry is
// included; with one, only the listed entry IDs are (the starred-entries
// export, #1177, built/updated client-side from the visitor's own
// localStorage star selection — dansal never learns which entries were
// starred). Same published-event visibility as the whole-event .ics export.
func feedEventTimetableICSHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		event, err := client.GetEvent(r.Context(), id)
		if err != nil || !event.IsPublished {
			http.NotFound(w, r)
			return
		}

		entries := filterTimetableEntries(event.Timetable, r.URL.Query().Get("entries"))

		cal := ics.NewCalendar()
		cal.SetMethod(ics.MethodPublish)
		cal.SetName(event.Title)
		for _, e := range entries {
			addTimetableEntryToCalendar(cal, cfg.Domain, event, e)
		}
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="event-%d-timetable.ics"`, id))
		w.Write([]byte(cal.Serialize()))
	}
}
