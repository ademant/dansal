package main

import (
	"html"
	"regexp"
	"strings"
)

// metaDescMaxLen is the target length (in runes) for meta descriptions,
// truncated to the last word boundary before the limit.
const metaDescMaxLen = 155

// Meta description helpers — truncate is shared by metaDesc and eventMetaDesc,
// and eventMetaDesc reuses formatDateStr instead of reimplementing it (#1031).

// truncate shortens s to at most maxLen runes, breaking at the last space
// before the limit, appending "…". A single unbroken run longer than maxLen is
// hard-cut at maxLen (metaDesc's strings.Fields normally prevents this, but
// the helper must not panic on it).
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	cut := runes[:maxLen]
	if i := strings.LastIndex(string(cut), " "); i > 0 {
		cut = cut[:i]
	}
	return string(cut) + "…"
}

// metaDesc returns the first maxLen chars of s with markdown syntax stripped,
// suitable for use as a meta description or OG description.
func metaDesc(s string, maxLen int) string {
	return truncate(plainTextDesc(s), maxLen)
}

// plainTextDesc strips markdown syntax and decodes stray leftover HTML
// entities from s, collapsing the result to single-spaced plain text.
// Descriptions are stored as markdown, which sometimes carries a literal
// "&nbsp;" left over from pasting out of a rich-text editor (e.g. a lone
// "&nbsp;" on its own paragraph) — harmless on the rendered page (goldmark
// passes raw HTML through, so a browser shows a plain space), but every
// text-only consumer (meta/OG description, schema.org JSON-LD description,
// #1280) needs it decoded rather than surfaced as literal entity text.
// html.UnescapeString turns "&nbsp;" into an actual U+00A0 NBSP rune, which
// unicode.IsSpace (and so strings.Fields) does not treat as whitespace by
// design (it's non-*breaking*) — replaced with a plain space explicitly so
// it collapses like any other stray whitespace instead of surviving as an
// invisible character. Unlike metaDesc, this does not truncate — used
// directly by callers (like the JSON-LD description) that want the full
// plain-text description.
func plainTextDesc(s string) string {
	// Decode entities before stripping markdown: reMetaMD treats a bare '#'
	// as a markdown heading marker, which would otherwise mangle a numeric
	// entity like "&#8211;" into "&8211;" before UnescapeString ever sees it.
	s = html.UnescapeString(s)
	s = reMetaMD.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, " ", " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

var reMetaMD = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)|[*_~` + "`" + `#>]+`)

var tagDisplayNames = map[string]string{
	"bal-folk":          "Bal-folk",
	"fest-noz":          "Fest-noz",
	"session":           "Session",
	"concert":           "Concert",
	"festival":          "Festival",
	"open-air":          "Open-air",
	"workshop":          "Workshop",
	"music-course":      "Music course",
	"dance-workshop":    "Dance workshop",
	"musician-workshop": "Musician workshop",
}

// eventMetaDesc returns a concise meta description for an event page.
// If the event has a description, it is used (markdown-stripped). Otherwise
// a unique description is assembled from structured fields (type tag, date,
// location, musicians/instructors) so that every page gets a distinct value.
func eventMetaDesc(event Event, lang string) string {
	if event.Description != "" {
		return metaDesc(event.Description, metaDescMaxLen)
	}

	var parts []string

	// Lead with format/type tag if present.
	for _, tag := range event.Tags {
		if name, ok := tagDisplayNames[tag]; ok {
			// Append level qualifier if any.
			for _, t2 := range event.Tags {
				switch t2 {
				case "beginners", "intermediate", "advanced":
					name += " (" + t2 + ")"
				}
			}
			parts = append(parts, name)
			break
		}
	}
	if len(parts) == 0 {
		parts = append(parts, event.Title)
	}

	// Date.
	if d := formatDateStr(lang, siteCfg.DateFormat(), event.StartTime); d != event.StartTime {
		parts = append(parts, d)
	}

	// Location name + city.
	if event.Location != nil {
		loc := event.Location.Location
		if event.Location.Town != "" {
			loc += ", " + event.Location.Town
		}
		parts = append(parts, loc)
	}

	desc := strings.Join(parts, " · ")

	// Musicians (up to 3).
	if len(event.Musicians) > 0 {
		names := make([]string, 0, min(3, len(event.Musicians)))
		for i, m := range event.Musicians {
			if i >= 3 {
				break
			}
			names = append(names, m.Bandname)
		}
		suffix := ""
		if len(event.Musicians) > 3 {
			suffix = "…"
		}
		desc += ". " + strings.Join(names, ", ") + suffix
	}

	// Instructors (up to 3).
	if len(event.Instructors) > 0 {
		names := make([]string, 0, min(3, len(event.Instructors)))
		for i, inst := range event.Instructors {
			if i >= 3 {
				break
			}
			names = append(names, inst.Name)
		}
		suffix := ""
		if len(event.Instructors) > 3 {
			suffix = "…"
		}
		desc += ". " + strings.Join(names, ", ") + suffix
	}

	return truncate(desc, metaDescMaxLen)
}
