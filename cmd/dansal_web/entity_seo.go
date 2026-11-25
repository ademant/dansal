package main

// Titles and descriptions for organisation, musician, instructor and venue
// pages (#1894). They reuse the city-page helpers in city_seo.go (#1891): the
// next dates come first, then the page's own text when it still fits.

import (
	"net/http"
	"strings"
	"time"
)

// nonCancelled returns the events that are not cancelled.
func nonCancelled(events []Event) []Event {
	var out []Event
	for _, e := range events {
		if !e.IsCancelled {
			out = append(out, e)
		}
	}
	return out
}

// singleTown returns the one town all events' venues share, or "" when there
// is none or more than one.
func singleTown(events []Event) string {
	town := ""
	for _, e := range events {
		if e.Location == nil || e.Location.Town == "" {
			continue
		}
		if town == "" {
			town = e.Location.Town
		} else if town != e.Location.Town {
			return ""
		}
	}
	return town
}

// nextDatesPhrase joins the next distinct start dates of events (at most
// cityDescMaxDates), in the order given.
func nextDatesPhrase(strs I18nStrings, lang, dateFormat string, events []Event) string {
	var dates []string
	seen := map[string]bool{}
	for _, e := range events {
		d := formatDateStr(lang, dateFormat, e.StartTime)
		if seen[d] {
			continue
		}
		seen[d] = true
		dates = append(dates, d)
		if len(dates) == cityDescMaxDates {
			break
		}
	}
	return joinList(strs, dates)
}

// entityDescription builds "<name>: next dates on <dates> in <town>" from the
// entity's non-cancelled upcoming events, then appends own text when it fits
// within metaDescMaxLen. key picks the wording ("seo_entity_desc" or
// "seo_venue_desc"). It returns "" when there are no upcoming events and no
// own text, so the caller can fall back.
func entityDescription(strs I18nStrings, key, name string, upcoming []Event, lang, dateFormat, own string) string {
	own = plainTextDesc(own)
	// Only events that have not ended: a past date must never be named.
	active := nonCancelled(notEndedEvents(upcoming, time.Now()))
	if len(active) == 0 {
		return metaDesc(own, metaDescMaxLen)
	}
	dates := nextDatesPhrase(strs, lang, dateFormat, active)
	phrase := strs.TF("seo_next_dates", dates)
	if town := singleTown(active); town != "" {
		phrase = strs.TF("seo_next_dates_town", dates, town)
	}
	s := strs.TF(key, name, phrase)
	if own != "" {
		s += " – " + own
	}
	return metaDesc(s, metaDescMaxLen)
}

// orgSEOTitle is "<Name> – Balfolk in <Town>" when the organisation's events
// take place in one town, else "<Name> – Balfolk".
func orgSEOTitle(strs I18nStrings, name string, events []Event) string {
	if town := singleTown(events); town != "" {
		return strs.TF("seo_title_in", name, town)
	}
	return strs.TF("seo_title_plain", name)
}

// venueSEOTitle is "<Name> <Town> – Balfolk" unless the name already names
// the town.
func venueSEOTitle(strs I18nStrings, name, town string) string {
	if town == "" || strings.Contains(name, town) {
		return name
	}
	return strs.TF("seo_venue_title", name, town)
}

// entitySEOLang returns the language and date format used for the dates in
// entity descriptions, matching the event meta descriptions.
func entitySEOLang(i18n *I18n, r *http.Request) (I18nStrings, string, string) {
	lang := i18n.detectLang(r)
	return i18n.Strings(lang), lang, siteCfg.DateFormat()
}
