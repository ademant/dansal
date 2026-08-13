package main

import (
	"html/template"
	"strings"
)

// Tag template functions — one slice of the merged tmplFuncMap, split out of
// frontend.go (#1031).

// tagDisplayLimit is the number of non-type tags shown before "+N more"
// truncation: 5 total slots minus the slots reserved for type tags
// (bal-folk/fest-noz, workshops, festival) so those always stay visible.
func tagDisplayLimit(tags []string) int {
	limit := 5
	if sliceContains(tags, "bal-folk") || sliceContains(tags, "fest-noz") {
		limit--
	}
	if sliceContains(tags, "workshop") || sliceContains(tags, "dance-workshop") || sliceContains(tags, "musician-workshop") || sliceContains(tags, "music-course") {
		limit--
	}
	if sliceContains(tags, "festival") {
		limit--
	}
	if limit < 0 {
		limit = 0
	}
	return limit
}

// tagI18nKeys maps a controlled-vocabulary tag slug to its i18n translation
// key, for tags whose public display name should be localized rather than
// the tag's static English tags.name in the DB (#1094). bal-folk is the only
// entry today: the slug means "the Balfolk dance style" for matching
// purposes, but publicly it should read as the style-agnostic "Ball" — the
// generic term for a partnered social-dance event, as dansal opens to other
// dance styles. Slugs not listed here fall back to the DB name.
var tagI18nKeys = map[string]string{
	"bal-folk": "tag_ball",
}

// tagLabel resolves a tag's public display name: the i18n translation for
// slug if one is registered in tagI18nKeys, else dbName (the DB's
// tags.name), else the bare slug as a last resort.
func tagLabel(strs I18nStrings, slug, dbName string) string {
	if key, ok := tagI18nKeys[slug]; ok {
		if s := strs.T(key); s != key {
			return s
		}
	}
	if dbName != "" {
		return dbName
	}
	return slug
}

var tmplFuncsTags = template.FuncMap{
	"limitTags": func(tags []string) []string {
		if limit := tagDisplayLimit(tags); len(tags) > limit {
			return tags[:limit]
		}
		return tags
	},
	"hiddenTagCount": func(tags []string) int {
		if limit := tagDisplayLimit(tags); len(tags) > limit {
			return len(tags) - limit
		}
		return 0
	},
	"tagName": func(tagMap map[string]Tag, slug string) string {
		if t, ok := tagMap[slug]; ok {
			return t.Name
		}
		return slug
	},
	// tagLabel is tagName plus the i18n override from tagI18nKeys (#1094) —
	// use this instead of tagName wherever a tag is shown to end users.
	"tagLabel": func(strs I18nStrings, tagMap map[string]Tag, slug string) string {
		return tagLabel(strs, slug, tagMap[slug].Name)
	},
	// tagLabelOf is tagLabel for a Tag struct already in hand (no TagMap
	// lookup needed), e.g. the current tag on its own /tags/{slug} page.
	"tagLabelOf": func(strs I18nStrings, tag Tag) string {
		return tagLabel(strs, tag.Slug, tag.Name)
	},
	"tagKey": func(slug string) string {
		return "tag_" + strings.ReplaceAll(slug, "-", "_")
	},
	"tagCatKey": func(cat string) string {
		return "tag_cat_" + cat
	},
	"hasTag": func(tags []string, slug string) bool {
		for _, t := range tags {
			if t == slug {
				return true
			}
		}
		return false
	},
	// eventAudienceType derives the schema.org audienceType for an event's
	// JSON-LD from a controlled tag mapping (#1063): events with a
	// musician-oriented tag address musicians as well as dancers.
	"eventAudienceType": eventAudienceType,
}

// eventAudienceType maps an event's tags to a schema.org audienceType. The
// mapping is deterministic so the rendered JSON-LD does not depend on
// template-internal magic strings (#1063).
func eventAudienceType(tags []string) string {
	for _, t := range tags {
		switch t {
		case "musician-workshop", "session", "music-course":
			return "dancers, musicians"
		}
	}
	return "dancers"
}
