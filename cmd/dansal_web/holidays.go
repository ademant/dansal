package main

import (
	"strings"
	"time"
)

// easter returns Easter Sunday for the given year using the Gregorian algorithm.
func easter(year int) time.Time {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := ((h + l - 7*m + 114) % 31) + 1
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

func addEasterBased(h map[string]struct{}, year int, offsets ...int) {
	e := easter(year)
	for _, off := range offsets {
		d := e.AddDate(0, 0, off)
		h[d.Format("2006-01-02")] = struct{}{}
	}
}

func addFixed(h map[string]struct{}, year, month, day int) {
	d := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	h[d.Format("2006-01-02")] = struct{}{}
}

// publicHolidays returns a set of public holiday date strings (YYYY-MM-DD) for
// the given ISO 3166-1 alpha-2 country code and year. Returns nil for unknown or empty codes.
func publicHolidays(country string, year int) map[string]struct{} {
	h := make(map[string]struct{})
	switch strings.ToUpper(country) {
	case "DE":
		addFixed(h, year, 1, 1)
		addEasterBased(h, year, -2, 1) // Good Friday, Easter Monday
		addFixed(h, year, 5, 1)
		addEasterBased(h, year, 39) // Ascension
		addEasterBased(h, year, 50) // Whit Monday
		addFixed(h, year, 10, 3)
		addFixed(h, year, 12, 25)
		addFixed(h, year, 12, 26)
	case "AT":
		addFixed(h, year, 1, 1)
		addFixed(h, year, 1, 6)
		addEasterBased(h, year, 1)
		addFixed(h, year, 5, 1)
		addEasterBased(h, year, 39)
		addEasterBased(h, year, 50)
		addEasterBased(h, year, 60) // Corpus Christi
		addFixed(h, year, 8, 15)
		addFixed(h, year, 10, 26)
		addFixed(h, year, 11, 1)
		addFixed(h, year, 12, 8)
		addFixed(h, year, 12, 25)
		addFixed(h, year, 12, 26)
	case "CH":
		addFixed(h, year, 1, 1)
		addEasterBased(h, year, -2, 1)
		addFixed(h, year, 5, 1)
		addEasterBased(h, year, 39)
		addEasterBased(h, year, 50)
		addFixed(h, year, 8, 1)
		addFixed(h, year, 12, 25)
		addFixed(h, year, 12, 26)
	case "FR":
		addFixed(h, year, 1, 1)
		addEasterBased(h, year, 1)
		addFixed(h, year, 5, 1)
		addFixed(h, year, 5, 8)
		addEasterBased(h, year, 39)
		addEasterBased(h, year, 50)
		addFixed(h, year, 7, 14)
		addFixed(h, year, 8, 15)
		addFixed(h, year, 11, 1)
		addFixed(h, year, 11, 11)
		addFixed(h, year, 12, 25)
	case "BE":
		addFixed(h, year, 1, 1)
		addEasterBased(h, year, 1)
		addFixed(h, year, 5, 1)
		addEasterBased(h, year, 39)
		addEasterBased(h, year, 50)
		addFixed(h, year, 7, 21)
		addFixed(h, year, 8, 15)
		addFixed(h, year, 11, 1)
		addFixed(h, year, 11, 11)
		addFixed(h, year, 12, 25)
	case "NL":
		addFixed(h, year, 1, 1)
		addEasterBased(h, year, -2, 0, 1)
		// King's Day: Apr 27, or Apr 26 when Apr 27 is a Sunday
		kd := time.Date(year, 4, 27, 0, 0, 0, 0, time.UTC)
		if kd.Weekday() == time.Sunday {
			kd = kd.AddDate(0, 0, -1)
		}
		h[kd.Format("2006-01-02")] = struct{}{}
		addFixed(h, year, 5, 5) // Liberation Day
		addEasterBased(h, year, 39)
		addEasterBased(h, year, 50)
		addFixed(h, year, 12, 25)
		addFixed(h, year, 12, 26)
	case "GB":
		addFixed(h, year, 1, 1)
		addEasterBased(h, year, -2, 1)
		// Early May Bank Holiday: first Monday in May
		d := time.Date(year, 5, 1, 0, 0, 0, 0, time.UTC)
		for d.Weekday() != time.Monday {
			d = d.AddDate(0, 0, 1)
		}
		h[d.Format("2006-01-02")] = struct{}{}
		// Spring Bank Holiday: last Monday in May
		d = time.Date(year, 5, 31, 0, 0, 0, 0, time.UTC)
		for d.Weekday() != time.Monday {
			d = d.AddDate(0, 0, -1)
		}
		h[d.Format("2006-01-02")] = struct{}{}
		// Summer Bank Holiday: last Monday in August
		d = time.Date(year, 8, 31, 0, 0, 0, 0, time.UTC)
		for d.Weekday() != time.Monday {
			d = d.AddDate(0, 0, -1)
		}
		h[d.Format("2006-01-02")] = struct{}{}
		addFixed(h, year, 12, 25)
		addFixed(h, year, 12, 26)
	case "LU":
		addFixed(h, year, 1, 1)
		addEasterBased(h, year, 1)
		addFixed(h, year, 5, 1)
		addEasterBased(h, year, 39)
		addEasterBased(h, year, 50)
		addFixed(h, year, 6, 23)
		addFixed(h, year, 8, 15)
		addFixed(h, year, 11, 1)
		addFixed(h, year, 12, 25)
		addFixed(h, year, 12, 26)
	default:
		return nil
	}
	return h
}

// holidayCountryLang maps an ISO 3166-1 alpha-2 country code to its dansal
// language code, for use as a last-resort detectLang() fallback (see
// cmd/dansal_web/i18n.go). Only unambiguous single-language countries are
// mapped — CH, BE and LU have multiple official languages, so a single
// guess would misrepresent the country; they return ok=false and fall
// through to the hardcoded i18n default instead.
func holidayCountryLang(country string) (lang string, ok bool) {
	switch strings.ToUpper(country) {
	case "DE", "AT":
		return "de", true
	case "FR":
		return "fr", true
	case "NL":
		return "nl", true
	case "GB":
		return "en", true
	default:
		return "", false
	}
}
