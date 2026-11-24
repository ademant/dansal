package main

// City page snippet and intro (#1891): the meta description names the next
// dates (today's or this weekend's events first), the page opens with
// "Heute" / "Dieses Wochenende" sections and a short generated intro. All of
// it is derived from the events and organisations the handler already has.

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cityIntroMonths is the look-ahead of the intro's event count.
const cityIntroMonths = 3

// cityDescMaxDates is how many dates the description lists.
const cityDescMaxDates = 3

// cityOrgRef is an organisation active in a city, for the intro links.
type cityOrgRef struct {
	Name string
	Slug string
}

// CityIntro is the generated top of a city page (#1891).
type CityIntro struct {
	Today      []Event // events taking place today
	Weekend    []Event // events on the coming (or current) weekend, without today's
	SoonCount  int     // upcoming events in the next cityIntroMonths months
	Orgs       []cityOrgRef
	Venues     []string // distinct venue names of the upcoming events
	AgendaURL  string   // /agenda filtered to the city's venues; "" when none
	HasContent bool     // false when there is nothing to say (no upcoming events)
}

// eventSpan returns the start and end of e; the end falls back to the start.
func eventSpan(e Event) (time.Time, time.Time, bool) {
	start, ok := parseTime(e.StartTime)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	end, ok := parseTime(e.EndTime)
	if !ok || end.Before(start) {
		end = start
	}
	return start, end, true
}

// notEndedEvents returns the events that have not ended yet, in
// their original (chronological) order. The city list may include past
// events when "show past events" is on.
func notEndedEvents(events []Event, now time.Time) []Event {
	var out []Event
	for _, e := range events {
		if _, end, ok := eventSpan(e); ok && !end.Before(now) {
			out = append(out, e)
		}
	}
	return out
}

// overlaps reports whether e takes place in [from, to).
func overlaps(e Event, from, to time.Time) bool {
	start, end, ok := eventSpan(e)
	return ok && start.Before(to) && !end.Before(from)
}

// weekendWindow returns the coming weekend, Friday 00:00 to Monday 00:00 in
// now's time zone. From Friday to Sunday it is the current weekend.
func weekendWindow(now time.Time) (time.Time, time.Time) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var offset int
	switch now.Weekday() {
	case time.Saturday:
		offset = -1
	case time.Sunday:
		offset = -2
	default: // Monday (1) … Friday (5)
		offset = int(time.Friday - now.Weekday())
	}
	from := day.AddDate(0, 0, offset)
	return from, from.AddDate(0, 0, 3)
}

// todayAndWeekend splits upcoming into today's events and the weekend's
// events that are not already today's.
func todayAndWeekend(upcoming []Event, now time.Time) (today, weekend []Event) {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	dayEnd := dayStart.AddDate(0, 0, 1)
	wFrom, wTo := weekendWindow(now)
	for _, e := range upcoming {
		if overlaps(e, dayStart, dayEnd) {
			today = append(today, e)
			continue
		}
		if overlaps(e, wFrom, wTo) {
			weekend = append(weekend, e)
		}
	}
	return today, weekend
}

// cityOrgs resolves the city's organisation IDs to names, sorted by name.
func cityOrgs(ids []int, orgs []Organization) []cityOrgRef {
	want := make(map[int]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []cityOrgRef
	for _, o := range orgs {
		if want[o.ID] && o.Name != "" {
			out = append(out, cityOrgRef{Name: o.Name, Slug: effectiveSlug(o)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// buildCityIntro assembles the generated intro of a city page.
func buildCityIntro(upcoming []Event, orgs []cityOrgRef, now time.Time) CityIntro {
	in := CityIntro{Orgs: orgs}
	in.Today, in.Weekend = todayAndWeekend(upcoming, now)
	limit := now.AddDate(0, cityIntroMonths, 0)
	seenVenue := map[string]bool{}
	seenLoc := map[int]bool{}
	q := url.Values{}
	for _, e := range upcoming {
		if start, _, ok := eventSpan(e); ok && start.Before(limit) {
			in.SoonCount++
		}
		if e.Location == nil {
			continue
		}
		if name := e.Location.Location; name != "" && !seenVenue[name] {
			seenVenue[name] = true
			in.Venues = append(in.Venues, name)
		}
		if id := e.Location.ID; id > 0 && !seenLoc[id] {
			seenLoc[id] = true
			q.Add("location", strconv.Itoa(id))
		}
	}
	if len(q) > 0 {
		in.AgendaURL = "/agenda?" + q.Encode()
	}
	in.HasContent = len(upcoming) > 0
	return in
}

// joinList joins items with ", " and the language's "and" before the last.
func joinList(strs I18nStrings, items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strs.TF("city_list_and", strings.Join(items[:len(items)-1], ", "), items[len(items)-1])
}

// cityOrgPhrase names one or two organisations, with "and others" when the
// city has more.
func cityOrgPhrase(strs I18nStrings, orgs []cityOrgRef) string {
	switch len(orgs) {
	case 0:
		return ""
	case 1:
		return orgs[0].Name
	case 2:
		return joinList(strs, []string{orgs[0].Name, orgs[1].Name})
	}
	return strs.TF("city_orgs_and_more", orgs[0].Name+", "+orgs[1].Name)
}

// eventLabel is "Title, Venue" for a description.
func eventLabel(e Event) string {
	if e.Location != nil && e.Location.Location != "" && !strings.Contains(e.Title, e.Location.Location) {
		return e.Title + ", " + e.Location.Location
	}
	return e.Title
}

// cityMetaDescription builds the city page description (#1891). Order of
// preference: today's events, this weekend's, the next dates (with the
// organisations named in front when they fit), and finally the count of
// organisations and venues for a city with no upcoming events.
func cityMetaDescription(strs I18nStrings, lang, dateFormat string, city City, upcoming, today, weekend []Event, orgs []cityOrgRef) string {
	fits := func(s string) bool { return len([]rune(s)) <= metaDescMaxLen }
	active := func(events []Event) []Event {
		var out []Event
		for _, e := range events {
			if !e.IsCancelled {
				out = append(out, e)
			}
		}
		return out
	}
	labels := func(events []Event) []string {
		var out []string
		for _, e := range events {
			out = append(out, eventLabel(e))
		}
		return out
	}

	if t := active(today); len(t) > 0 {
		return metaDesc(strs.TF("city_desc_today", city.Town, joinList(strs, labels(t))), metaDescMaxLen)
	}
	if wk := active(weekend); len(wk) > 0 {
		return metaDesc(strs.TF("city_desc_weekend", city.Town, joinList(strs, labels(wk))), metaDescMaxLen)
	}
	next := active(upcoming)
	if len(next) == 0 {
		return metaDesc(strs.TF("city_meta_desc", city.Town, city.OrgCount, city.LocationCount), metaDescMaxLen)
	}
	var dates []string
	seen := map[string]bool{}
	for _, e := range next {
		d := formatDateStr(lang, dateFormat, e.StartTime)
		if !seen[d] {
			seen[d] = true
			dates = append(dates, d)
		}
		if len(dates) == cityDescMaxDates {
			break
		}
	}
	dateList := joinList(strs, dates)
	// The organisation names are dropped before the dates when the text
	// would be too long.
	if p := cityOrgPhrase(strs, orgs); p != "" {
		if s := strs.TF("city_desc_dates_orgs", p, city.Town, dateList); fits(s) {
			return metaDesc(s, metaDescMaxLen)
		}
	}
	return metaDesc(strs.TF("city_desc_dates", city.Town, dateList), metaDescMaxLen)
}
