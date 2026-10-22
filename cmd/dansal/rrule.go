package main

import (
	"log"
	"sort"
	"time"

	ics "github.com/arran4/golang-ical"
)

// icalLastModified returns the LAST-MODIFIED Unix timestamp from a vevent.
// Falls back to raw property parsing to handle non-standard date-only values
// produced by some generators (e.g. WordPress MECv7: "20260509" instead of
// "20260509T000000Z").
func icalLastModified(vevent *ics.VEvent) int64 {
	if lm, err := vevent.GetLastModifiedAt(); err == nil {
		return lm.Unix()
	}
	if p := vevent.GetProperty(ics.ComponentPropertyLastModified); p != nil {
		for _, layout := range []string{"20060102", "20060102T150405", "20060102T150405Z"} {
			if t, err := time.ParseInLocation(layout, p.Value, time.UTC); err == nil {
				return t.Unix()
			}
		}
	}
	return 0
}

// icalMaxOccurrences caps the occurrences one recurring VEVENT may expand to
// on import (#1578). Set from server.ical_max_occurrences at startup.
var icalMaxOccurrences = defaultIcalMaxOccurrences

// defaultIcalMaxOccurrences is the cap when the config doesn't set one.
const defaultIcalMaxOccurrences = 500

// expandRRuleOccurrences returns all (start, end) pairs for a recurring VEVENT,
// starting from and including the base occurrence. EXDATE entries and occurrences
// beyond UNTIL, COUNT, or 3 years from now are excluded.
// Returns nil when the VEVENT has no RRULE.
func expandRRuleOccurrences(vevent *ics.VEvent, baseStart, baseEnd time.Time) ([][2]time.Time, error) {
	rules, err := vevent.GetRRules()
	if err != nil || len(rules) == 0 {
		return nil, nil
	}

	exdates, _ := vevent.GetExDates()
	excluded := make(map[int64]bool, len(exdates))
	for _, ex := range exdates {
		excluded[ex.Truncate(time.Minute).Unix()] = true
	}

	dur := baseEnd.Sub(baseStart)
	ceiling := time.Now().AddDate(3, 0, 0)
	loc := baseStart.Location()

	var result [][2]time.Time
	capped := false
	for _, rule := range rules {
		if capped {
			break
		}
		until := ceiling
		if !rule.Until.IsZero() && rule.Until.Before(ceiling) {
			until = rule.Until
		}

		var starts []time.Time
		switch rule.Freq {
		case ics.FrequencyWeekly:
			starts = rruleWeekly(rule, baseStart, until, loc)
		case ics.FrequencyMonthly:
			starts = rruleMonthly(rule, baseStart, until, loc)
		case ics.FrequencyDaily:
			starts = rruleDaily(rule, baseStart, until, loc)
		case ics.FrequencyYearly:
			starts = rruleYearly(rule, baseStart, until, loc)
		default:
			continue
		}

		for _, t := range starts {
			if excluded[t.Truncate(time.Minute).Unix()] {
				continue
			}
			if len(result) >= icalMaxOccurrences {
				capped = true
				break
			}
			result = append(result, [2]time.Time{t, t.Add(dur)})
		}
	}
	if capped {
		uid := ""
		if p := vevent.GetProperty(ics.ComponentPropertyUniqueId); p != nil {
			uid = p.Value
		}
		log.Printf("ical import: %s expands to more than %d occurrences; stopped at the cap", uid, icalMaxOccurrences)
	}
	return result, nil
}

func rruleDaily(rule *ics.RecurrenceRule, base, until time.Time, loc *time.Location) []time.Time {
	baseLocal := base.In(loc)
	y, mo, d := baseLocal.Date()
	h, m, s := baseLocal.Clock()
	var result []time.Time
	for cur := time.Date(y, mo, d, h, m, s, 0, loc); !cur.After(until); cur = cur.AddDate(0, 0, rule.Interval) {
		result = append(result, cur)
		if rule.Count > 0 && len(result) >= rule.Count {
			break
		}
	}
	return result
}

func rruleWeekly(rule *ics.RecurrenceRule, base, until time.Time, loc *time.Location) []time.Time {
	wkst := rule.Wkst
	if wkst == "" {
		wkst = ics.WeekdayMonday
	}
	byday := rule.ByDay
	if len(byday) == 0 {
		byday = []ics.WeekdayNum{{Day: goToICSWeekday(base.In(loc).Weekday())}}
	}

	baseLocal := base.In(loc)
	h, m, s := baseLocal.Clock()
	goWkst := icsToGoWeekday(wkst)

	// Start-of-week (day only) for the week containing base.
	by, bmo, bd := baseLocal.Date()
	off := (int(baseLocal.Weekday()) - int(goWkst) + 7) % 7
	anchor := time.Date(by, bmo, bd-off, 0, 0, 0, 0, loc)

	var result []time.Time
	for ; !anchor.After(until); anchor = anchor.AddDate(0, 0, 7*rule.Interval) {
		ay, amo, ad := anchor.Date()
		for _, byd := range byday {
			dayOff := (int(icsToGoWeekday(byd.Day)) - int(goWkst) + 7) % 7
			t := time.Date(ay, amo, ad+dayOff, h, m, s, 0, loc)
			if !t.Before(base) && !t.After(until) {
				result = append(result, t)
				if rule.Count > 0 && len(result) >= rule.Count {
					return result
				}
			}
		}
	}
	return result
}

func rruleMonthly(rule *ics.RecurrenceRule, base, until time.Time, loc *time.Location) []time.Time {
	baseLocal := base.In(loc)
	h, m, s := baseLocal.Clock()
	year, month := baseLocal.Year(), baseLocal.Month()

	var result []time.Time
	for {
		var candidates []time.Time
		switch {
		case len(rule.ByDay) > 0:
			for _, byd := range rule.ByDay {
				candidates = append(candidates, nthWeekdayOfMonth(year, month, byd.OrdWeek, byd.Day, h, m, s, loc)...)
			}
		case len(rule.ByMonthDay) > 0:
			for _, dayNum := range rule.ByMonthDay {
				if t, ok := monthDayTime(year, month, dayNum, h, m, s, loc); ok {
					candidates = append(candidates, t)
				}
			}
		default:
			if t, ok := monthDayTime(year, month, baseLocal.Day(), h, m, s, loc); ok {
				candidates = append(candidates, t)
			}
		}

		sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })

		for _, t := range candidates {
			if !t.Before(base) && !t.After(until) {
				result = append(result, t)
				if rule.Count > 0 && len(result) >= rule.Count {
					return result
				}
			}
		}

		next := time.Date(year, month+time.Month(rule.Interval), 1, 0, 0, 0, 0, loc)
		year, month = next.Year(), next.Month()
		if time.Date(year, month, 1, 0, 0, 0, 0, loc).After(until) {
			break
		}
	}
	return result
}

func rruleYearly(rule *ics.RecurrenceRule, base, until time.Time, loc *time.Location) []time.Time {
	baseLocal := base.In(loc)
	y, mo, d := baseLocal.Date()
	h, m, s := baseLocal.Clock()
	var result []time.Time
	for cur := time.Date(y, mo, d, h, m, s, 0, loc); !cur.After(until); cur = time.Date(cur.Year()+rule.Interval, mo, d, h, m, s, 0, loc) {
		result = append(result, cur)
		if rule.Count > 0 && len(result) >= rule.Count {
			break
		}
	}
	return result
}

// nthWeekdayOfMonth returns occurrences of a given weekday in (year, month).
// ord=0 returns all; ord>0 returns the nth from start; ord<0 returns the nth from end.
func nthWeekdayOfMonth(year int, month time.Month, ord int, day ics.Weekday, h, m, s int, loc *time.Location) []time.Time {
	goWd := icsToGoWeekday(day)
	var all []time.Time
	for d := 1; d <= 31; d++ {
		t := time.Date(year, month, d, h, m, s, 0, loc)
		if t.Month() != month {
			break
		}
		if t.Weekday() == goWd {
			all = append(all, t)
		}
	}
	switch {
	case ord == 0:
		return all
	case ord > 0 && ord <= len(all):
		return []time.Time{all[ord-1]}
	case ord < 0:
		if idx := len(all) + ord; idx >= 0 {
			return []time.Time{all[idx]}
		}
	}
	return nil
}

// monthDayTime resolves a BYMONTHDAY value (positive or negative) in (year, month).
func monthDayTime(year int, month time.Month, dayNum, h, m, s int, loc *time.Location) (time.Time, bool) {
	var d int
	if dayNum > 0 {
		d = dayNum
	} else {
		d = time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day() + dayNum + 1
	}
	if d < 1 {
		return time.Time{}, false
	}
	t := time.Date(year, month, d, h, m, s, 0, loc)
	if t.Month() != month {
		return time.Time{}, false
	}
	return t, true
}

func goToICSWeekday(wd time.Weekday) ics.Weekday {
	switch wd {
	case time.Sunday:
		return ics.WeekdaySunday
	case time.Monday:
		return ics.WeekdayMonday
	case time.Tuesday:
		return ics.WeekdayTuesday
	case time.Wednesday:
		return ics.WeekdayWednesday
	case time.Thursday:
		return ics.WeekdayThursday
	case time.Friday:
		return ics.WeekdayFriday
	case time.Saturday:
		return ics.WeekdaySaturday
	}
	return ics.WeekdayMonday
}

func icsToGoWeekday(wd ics.Weekday) time.Weekday {
	switch wd {
	case ics.WeekdaySunday:
		return time.Sunday
	case ics.WeekdayMonday:
		return time.Monday
	case ics.WeekdayTuesday:
		return time.Tuesday
	case ics.WeekdayWednesday:
		return time.Wednesday
	case ics.WeekdayThursday:
		return time.Thursday
	case ics.WeekdayFriday:
		return time.Friday
	case ics.WeekdaySaturday:
		return time.Saturday
	}
	return time.Monday
}
