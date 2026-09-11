package main

import (
	"strconv"
	"strings"
)

// eventTypeBucket maps an event's tags to the same three buckets
// has_ball/has_workshop/has_festival already use (see CLAUDE.md), for
// picking which webmin-configured default sentence (#1290) applies. An
// event matching more than one bucket uses festival > workshop > ball
// priority (broadest scope wins). An event matching none of these (only
// session/concert/open-air/music-course) gets no type sentence at all —
// deliberately no generic catch-all bucket.
func eventTypeBucket(tags []string) string {
	has := func(slugs ...string) bool {
		for _, t := range tags {
			for _, s := range slugs {
				if t == s {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("festival"):
		return "festival"
	case has("workshop", "dance-workshop", "musician-workshop", "music-course"):
		return "workshop"
	case has("bal-folk", "fest-noz"):
		return "ball"
	default:
		return ""
	}
}

// formatAmount renders a price as the shortest decimal representation
// (10, not 10.000000) followed by the currency code.
func formatAmount(amount float64, currency string) string {
	return strconv.FormatFloat(amount, 'f', -1, 64) + " " + currency
}

// pricingSentence renders p as a plain-text sentence ("Admission: Free."),
// reusing the existing evt_admission/evt_free/evt_donation i18n labels
// (already translated in all 12 languages for the visible admission block)
// rather than inventing new sentence-shaped keys. Mirrors the branches
// event.html's own admission display already has. Returns "" when p is nil
// or (for "multiple") carries no prices at all.
func pricingSentence(strs I18nStrings, p *Pricing) string {
	if p == nil {
		return ""
	}
	curr := p.Currency
	if curr == "" {
		curr = "EUR"
	}
	var value string
	switch p.Type {
	case "free":
		value = strs.T("evt_free")
	case "donation":
		if p.Amount > 0 {
			value = formatAmount(p.Amount, curr)
		} else {
			value = strs.T("evt_donation")
		}
	case "single":
		value = formatAmount(p.Amount, curr)
	case "multiple":
		if len(p.Prices) == 0 {
			return ""
		}
		parts := make([]string, 0, len(p.Prices))
		for _, pr := range p.Prices {
			parts = append(parts, pr.Label+": "+formatAmount(pr.Amount, curr))
		}
		value = strings.Join(parts, ", ")
	default:
		return ""
	}
	if value == "" {
		return ""
	}
	return strings.TrimSpace(strs.T("evt_admission")) + " " + value + "."
}

// defaultEventDescription composes a default description (#1290) for an
// event that has none of its own, from three parts in order: the event-type
// sentence (eventTypeBucket, via siteCfg's webmin-editable per-language
// defaults), each associated dance's own admin-entered description
// (danceDescByName, keyed by Event.DanceNames since the API doesn't expose
// dance IDs on the event itself), and a plain-text rendering of its pricing.
// Returns "" when none of the three parts produced anything (e.g. an
// untagged event with no dances and no pricing) — callers fall back to
// whatever their own generic placeholder is in that case.
func defaultEventDescription(strs I18nStrings, lang string, ev Event, danceDescByName map[string]string) string {
	var parts []string
	switch eventTypeBucket(ev.Tags) {
	case "festival":
		if s := siteCfg.DescFestival(lang); s != "" {
			parts = append(parts, s)
		}
	case "workshop":
		if s := siteCfg.DescWorkshop(lang); s != "" {
			parts = append(parts, s)
		}
	case "ball":
		if s := siteCfg.DescBall(lang); s != "" {
			parts = append(parts, s)
		}
	}
	for _, name := range ev.DanceNames {
		if d := danceDescByName[name]; d != "" {
			parts = append(parts, d)
		}
	}
	if s := pricingSentence(strs, ev.Pricing); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}
