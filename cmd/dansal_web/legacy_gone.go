package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Legacy Gancio URLs stay 410 (#1433) — the identifiers belonged to another
// database — but a browser visitor gets "maybe you're looking for" links
// derived from the old slug instead of a bare dead end (#1661). Crawlers and
// API clients (no text/html in Accept) get the plain 410 without any lookup.

const (
	legacyGoneMaxEvents  = 5
	legacyGoneMaxVenues  = 3
	legacyGoneLookupTime = 2 * time.Second
)

// legacySlugWords turns a Gancio slug into title search words: numeric
// segments (Gancio's "-2" dedup counters, "2000-2200" time prefixes) are
// dropped, the rest joined with spaces. "tanzlernabend-10" → "tanzlernabend".
func legacySlugWords(slug string) string {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(slug), func(r rune) bool {
		return r == '-' || r == '_' || r == ' ' || r == ','
	}) {
		if strings.IndexFunc(w, func(r rune) bool { return !unicode.IsDigit(r) }) < 0 {
			continue
		}
		words = append(words, w)
		if len(words) == 6 {
			break
		}
	}
	return strings.Join(words, " ")
}

// longestWord returns the longest word of phrase if it has at least 5
// letters and differs from phrase — the fallback search when the whole
// phrase matches nothing.
func longestWord(phrase string) string {
	best := ""
	for _, w := range strings.Fields(phrase) {
		if len([]rune(w)) > len([]rune(best)) {
			best = w
		}
	}
	if len([]rune(best)) < 5 || best == phrase {
		return ""
	}
	return best
}

func legacyGoneHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !wantsHTML(r) || errorPageDeps.tmpls == nil {
			htmlError(w, r, http.StatusGone)
			return
		}
		i18n := errorPageDeps.i18n
		data := ErrorPageData{
			Title:    i18n.T(r, "legacy_gone_title"),
			Message:  i18n.T(r, "legacy_gone_message"),
			BackHome: i18n.T(r, "error_page_back_home"),
			Links:    []ErrorPageLink{{Label: i18n.T(r, "cities_title"), URL: "/cities"}},
		}
		ctx, cancel := context.WithTimeout(r.Context(), legacyGoneLookupTime)
		defer cancel()
		switch {
		case strings.HasPrefix(r.URL.Path, "/event/"):
			data.Suggestions = legacyEventSuggestions(ctx, client, legacySlugWords(r.PathValue("slug")))
		case strings.HasPrefix(r.URL.Path, "/place/"):
			data.Suggestions = legacyVenueSuggestions(ctx, client, r.PathValue("slug"))
		}
		if len(data.Suggestions) > 0 {
			data.SuggestHeading = i18n.T(r, "legacy_gone_maybe")
		}
		renderErrorPage(w, r, http.StatusGone, data)
	}
}

// legacyEventSuggestions finds events whose title contains phrase: upcoming
// first, else the most recent past ones; retried with the phrase's longest
// word when the whole phrase matches nothing.
func legacyEventSuggestions(ctx context.Context, client *DansalClient, phrase string) []ErrorPageSuggestion {
	for _, q := range []string{phrase, longestWord(phrase)} {
		if q == "" {
			continue
		}
		events, err := client.GetEventsFiltered(ctx, EventFilter{IsPublished: true, Title: q, Limit: legacyGoneMaxEvents}.Values())
		if err == nil && len(events) == 0 {
			past := EventFilter{IsPublished: true, Title: q, IncludePast: true, Limit: legacyGoneMaxEvents}.Values()
			past.Set("end_time_before", strconv.FormatInt(time.Now().Unix(), 10))
			past.Set("order", "desc")
			events, err = client.GetEventsFiltered(ctx, past)
		}
		if err != nil {
			return nil
		}
		if len(events) == 0 {
			continue
		}
		out := make([]ErrorPageSuggestion, 0, len(events))
		for _, e := range events {
			out = append(out, ErrorPageSuggestion{Title: e.Title, URL: "/events/" + strconv.Itoa(e.ID), StartTime: e.StartTime})
		}
		return out
	}
	return nil
}

// legacyVenueSuggestions matches the old place slug (the venue name, URL-
// decoded by the router) against current venues, with the same longest-word
// fallback as events.
func legacyVenueSuggestions(ctx context.Context, client *DansalClient, name string) []ErrorPageSuggestion {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	locs, err := client.GetLocations(ctx)
	if err != nil {
		return nil
	}
	for _, q := range []string{name, longestWord(strings.ToLower(name))} {
		if q == "" {
			continue
		}
		hits := matchLocations(q, locs)
		if len(hits) == 0 {
			continue
		}
		if len(hits) > legacyGoneMaxVenues {
			hits = hits[:legacyGoneMaxVenues]
		}
		out := make([]ErrorPageSuggestion, 0, len(hits))
		for _, h := range hits {
			out = append(out, ErrorPageSuggestion{Title: h.Label, URL: "/location/" + strconv.Itoa(h.ID), Sub: h.Town})
		}
		return out
	}
	return nil
}
