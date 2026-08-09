package main

import (
	"fmt"
	"html/template"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Time, date, and timetable template functions — one slice of the merged
// tmplFuncMap, split out of frontend.go (#1031). These also carry the
// date/timetable helpers that templates and Go code share (formatDateStr,
// timetableDays, timetableGrid, ...).

var locMonths = map[string][12]string{
	"br": {"Gen.", "C'hwev.", "Meur.", "Ebr.", "Mae", "Mezh.", "Gouer.", "Eost", "Gwen.", "Here", "Du", "Kerz."},
	"de": {"Jan", "Feb", "Mär", "Apr", "Mai", "Jun", "Jul", "Aug", "Sep", "Okt", "Nov", "Dez"},
	"en": {"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
	"fr": {"jan.", "fév.", "mar.", "avr.", "mai", "juin", "juil.", "août", "sept.", "oct.", "nov.", "déc."},
	"es": {"Ene", "Feb", "Mar", "Abr", "May", "Jun", "Jul", "Ago", "Sep", "Oct", "Nov", "Dic"},
	"it": {"Gen", "Feb", "Mar", "Apr", "Mag", "Giu", "Lug", "Ago", "Set", "Ott", "Nov", "Dic"},
	"nl": {"Jan", "Feb", "Mrt", "Apr", "Mei", "Jun", "Jul", "Aug", "Sep", "Okt", "Nov", "Dec"},
}

var locWeekdays = map[string][7]string{
	"br": {"Sul.", "Lun.", "Meur.", "Merc'h.", "Yaou.", "Gwen.", "Sad."},
	"de": {"So.", "Mo.", "Di.", "Mi.", "Do.", "Fr.", "Sa."},
	"en": {"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	"fr": {"Dim.", "Lun.", "Mar.", "Mer.", "Jeu.", "Ven.", "Sam."},
	"es": {"Dom", "Lun", "Mar", "Mié", "Jue", "Vie", "Sáb"},
	"it": {"Dom", "Lun", "Mar", "Mer", "Gio", "Ven", "Sab"},
	"nl": {"Zo", "Ma", "Di", "Wo", "Do", "Vr", "Za"},
}

func locMonth(lang string, m time.Month) string {
	if names, ok := locMonths[lang]; ok {
		return names[m-1]
	}
	return locMonths["en"][m-1]
}

func locWeekday(lang string, w time.Weekday) string {
	if names, ok := locWeekdays[lang]; ok {
		return names[w]
	}
	return locWeekdays["en"][w]
}

func formatDateStr(lang, s string) string {
	t, ok := parseTime(s)
	if !ok {
		return s
	}
	mo := locMonth(lang, t.Month())
	if lang == "de" {
		return fmt.Sprintf("%02d. %s %d", t.Day(), mo, t.Year())
	}
	return fmt.Sprintf("%02d %s %d", t.Day(), mo, t.Year())
}

var parseLayouts = []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}

func parseTime(s string) (time.Time, bool) {
	for _, layout := range parseLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseISODate parses a "2006-01-02" calendar-date string (the ?from=/?to=
// query-param format), reporting ok=false for empty or malformed input. Handlers
// use it to validate date params instead of repeating time.Parse("2006-01-02", …).
func parseISODate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s)
	return t, err == nil
}

// splitUpcomingPast partitions events into upcoming (EndTime is now or later)
// and past (EndTime strictly before now). Events with an unparseable EndTime
// are treated as upcoming, matching the behaviour of the former hand-rolled
// loops. Both slices preserve input order.
func splitUpcomingPast(events []Event, now time.Time) (upcoming, past []Event) {
	for _, e := range events {
		if t, err := time.Parse(time.RFC3339, e.EndTime); err == nil && t.Before(now) {
			past = append(past, e)
		} else {
			upcoming = append(upcoming, e)
		}
	}
	return upcoming, past
}

// fmtClock formats an hour/minute pair in 24h ("13:00") or 12h ("1:00 PM") notation.
func fmtClock(timeFormat string, h, m int) string {
	if timeFormat == "12h" {
		ampm := "AM"
		if h >= 12 {
			ampm = "PM"
		}
		if h > 12 {
			h -= 12
		} else if h == 0 {
			h = 12
		}
		return fmt.Sprintf("%d:%02d %s", h, m, ampm)
	}
	return fmt.Sprintf("%02d:%02d", h, m)
}

// parseTimetableClock parses an "HH:MM" timetable time into minutes since
// midnight. Timetable start/end times are always validated to this exact
// format server-side (cmd/dansal/timetable.go, validTimeSlot), so failure
// here only happens for legacy/corrupt data.
func parseTimetableClock(s string) (int, bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// TimetableDay groups timetable entries under the calendar date they
// belong to, for multi-day events (festivals, workshop weekends, #894).
type TimetableDay struct {
	Date    string // YYYY-MM-DD
	Entries []TimetableEntry
}

// timetableDays splits entries into day buckets covering every calendar day
// of the event's own start/end range — not just the days that happen to
// have dated entries — so a multi-day event always shows a section per day
// (e.g. day 1 with everything still undated, day 2 empty until entries get
// assigned a date via the admin picker) rather than collapsing everything
// into a single block. An entry without its own EntryDate belongs to the
// event's start date; single-day events always yield exactly one day (no
// visible change from before #894).
func timetableDays(entries []TimetableEntry, eventStart, eventEnd string) []TimetableDay {
	startDate := eventStart
	if t, ok := parseTime(eventStart); ok {
		startDate = t.Format("2006-01-02")
	}
	endDate := startDate
	if t, ok := parseTime(eventEnd); ok {
		endDate = t.Format("2006-01-02")
	}

	byDate := map[string][]TimetableEntry{}
	for _, e := range entries {
		d := strings.TrimSpace(e.EntryDate)
		if d == "" {
			d = startDate
		}
		byDate[d] = append(byDate[d], e)
	}

	var order []string
	seen := map[string]bool{}
	st, errSt := time.Parse("2006-01-02", startDate)
	en, errEn := time.Parse("2006-01-02", endDate)
	if errSt == nil && errEn == nil && !en.Before(st) {
		for d := st; !d.After(en); d = d.AddDate(0, 0, 1) {
			ds := d.Format("2006-01-02")
			order = append(order, ds)
			seen[ds] = true
		}
	}
	// Entries dated outside the event's own range (shouldn't normally
	// happen — the admin picker only offers in-range dates — but must not
	// be silently dropped if it does) get appended as trailing days.
	var extra []string
	for d := range byDate {
		if !seen[d] {
			extra = append(extra, d)
		}
	}
	sort.Strings(extra)
	order = append(order, extra...)

	days := make([]TimetableDay, 0, len(order))
	for _, d := range order {
		days = append(days, TimetableDay{Date: d, Entries: byDate[d]})
	}
	return days
}

// timetableColumnKey returns the grouping key/label/other-flag for one
// timetable entry, shared by timetableGrid's column bucketing.
func timetableColumnKey(e TimetableEntry) (key, label string, isOther bool) {
	switch {
	case e.LocationID != nil:
		return fmt.Sprintf("loc:%d", *e.LocationID), e.LocationName, false
	case strings.TrimSpace(e.Room) != "":
		label := strings.TrimSpace(e.Room)
		return "room:" + strings.ToLower(label), label, false
	default:
		return "other", "", true
	}
}

func timetableGrid(entries []TimetableEntry) TimetableGrid {
	const minPxPerMin = 1.4
	const maxPxPerMin = 4.0
	const minTotalHeightPx = 220.0

	rangeMin, rangeMax := 0, 0
	haveRange := false
	type parsed struct {
		entry            TimetableEntry
		startMin, endMin int
	}
	var parsedEntries []parsed
	for _, e := range entries {
		start, ok1 := parseTimetableClock(e.StartTime)
		end, ok2 := parseTimetableClock(e.EndTime)
		if !ok1 || !ok2 {
			continue
		}
		if end <= start {
			end += 24 * 60 // crosses midnight (e.g. a fest-noz running past 00:00)
		}
		parsedEntries = append(parsedEntries, parsed{entry: e, startMin: start, endMin: end})
		if !haveRange || start < rangeMin {
			rangeMin = start
		}
		if !haveRange || end > rangeMax {
			rangeMax = end
		}
		haveRange = true
	}
	if !haveRange {
		return TimetableGrid{}
	}

	// Pick a mark step from the raw range before rounding, then round the
	// range itself out to that step so the axis starts/ends on a mark.
	step := 60
	if rangeMax-rangeMin <= 180 {
		step = 30
	}
	rangeMin -= rangeMin % step
	if r := rangeMax % step; r != 0 {
		rangeMax += step - r
	}
	totalMin := rangeMax - rangeMin
	if totalMin <= 0 {
		totalMin = step
		rangeMax = rangeMin + step
	}

	pxPerMin := minPxPerMin
	if h := float64(totalMin) * pxPerMin; h < minTotalHeightPx {
		pxPerMin = minTotalHeightPx / float64(totalMin)
	}
	if pxPerMin > maxPxPerMin {
		pxPerMin = maxPxPerMin
	}

	grid := TimetableGrid{HeightPx: float64(totalMin) * pxPerMin}
	for m := rangeMin; m <= rangeMax; m += step {
		grid.Marks = append(grid.Marks, TimetableGridMark{
			Label: fmt.Sprintf("%02d:%02d", (m/60)%24, m%60),
			TopPx: float64(m-rangeMin) * pxPerMin,
		})
	}

	colIdx := map[string]int{}
	for _, p := range parsedEntries {
		key, label, isOther := timetableColumnKey(p.entry)
		i, ok := colIdx[key]
		if !ok {
			grid.Columns = append(grid.Columns, TimetableGridColumn{Label: label, IsOther: isOther})
			i = len(grid.Columns) - 1
			colIdx[key] = i
		}
		grid.Columns[i].Panels = append(grid.Columns[i].Panels, TimetablePanel{
			Entry:    p.entry,
			TopPx:    float64(p.startMin-rangeMin) * pxPerMin,
			HeightPx: float64(p.endMin-p.startMin) * pxPerMin,
		})
	}
	return grid
}

var tmplFuncsTime = template.FuncMap{
	"formatTime": func(lang, timeFormat, s string) string {
		t, ok := parseTime(s)
		if !ok {
			return s
		}
		wd := locWeekday(lang, t.Weekday())
		mo := locMonth(lang, t.Month())
		clock := fmtClock(timeFormat, t.Hour(), t.Minute())
		if lang == "de" {
			return fmt.Sprintf("%s %02d. %s %d, %s", wd, t.Day(), mo, t.Year(), clock)
		}
		return fmt.Sprintf("%s %02d %s %d, %s", wd, t.Day(), mo, t.Year(), clock)
	},
	"formatDate": func(lang, s string) string {
		return formatDateStr(lang, s)
	},
	"isoDate": func(s string) string {
		if t, ok := parseTime(s); ok {
			return t.Format("2006-01-02")
		}
		return s
	},
	// isoEndDate is like isoDate but treats 00:00–04:59 end times as
	// belonging to the previous calendar day — but only when start and end
	// are already on different dates (an event starting and ending on the
	// same date should never be rolled back, even at 01:00).
	"isoEndDate": func(startS, endS string) string {
		if t, ok := parseTime(endS); ok {
			if t.Hour() < 5 {
				if ts, ok2 := parseTime(startS); ok2 {
					if t.Format("2006-01-02") != ts.Format("2006-01-02") {
						t = t.Add(-24 * time.Hour)
					}
				}
			}
			return t.Format("2006-01-02")
		}
		return endS
	},
	"fmtUnix": func(ts int64) string {
		if ts == 0 {
			return ""
		}
		return time.Unix(ts, 0).UTC().Format("2006-01-02")
	},
	"parseChangedAt": parseChangedAt,
	"isoTime": func(s string) string {
		if t, ok := parseTime(s); ok {
			return t.Format("15:04")
		}
		return ""
	},
	"formatHourMin": func(timeFormat, s string) string {
		if t, ok := parseTime(s); ok {
			return fmtClock(timeFormat, t.Hour(), t.Minute())
		}
		return ""
	},
	"sameDate": func(s1, s2 string) bool {
		t1, ok1 := parseTime(s1)
		t2, ok2 := parseTime(s2)
		if !ok1 || !ok2 {
			return false
		}
		return t1.Year() == t2.Year() && t1.Month() == t2.Month() && t1.Day() == t2.Day()
	},
	"timetableDays":  timetableDays,
	"usedRoomIDs": func(entries []TimetableEntry) map[int]bool {
		ids := map[int]bool{}
		for _, e := range entries {
			if e.LocationID != nil {
				ids[*e.LocationID] = true
			}
		}
		return ids
	},
	// timetableGrid groups timetable entries into per-room columns and
	// positions each entry in pixels against one shared time axis, for a
	// real day-view calendar layout on /event/{id} (#887, refines #886's
	// independent-per-column stacked lists). Rooms are grouped primarily by
	// LocationID (a stable reference, labeled by LocationName), falling back
	// to the free-text Room string (trimmed/case-insensitive key) when no
	// LocationID is set, and finally a single shared "other" column for
	// entries with neither. Column order follows first appearance, i.e. the
	// timetable's existing time order.
	//
	// The axis only spans the timetable's own earliest start to latest end
	// (rounded to a mark boundary), not a fixed 24h range. Entries ending
	// before they start (e.g. a fest-noz running past midnight) are treated
	// as ending the next day. Overlapping entries within the same room are a
	// known, deliberately deferred edge case (#888) — this only lays out
	// columns/time, it doesn't detect or resolve overlaps.
	"timetableGrid": timetableGrid,
}
