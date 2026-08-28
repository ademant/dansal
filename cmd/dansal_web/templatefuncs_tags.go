package main

import (
	"encoding/json"
	"html/template"
	"sort"
	"strings"
)

// Tag template functions — one slice of the merged tmplFuncMap, split out of
// frontend.go (#1031).

// tagDisplayLimit is the number of non-type tags shown before "+N more"
// truncation: 5 total slots minus one slot per home-page format-selector
// badge that will also be shown for this event (#1173 — previously a fixed
// 3-check formula hardcoded to bal-folk/fest-noz, the 4 workshop variants,
// and festival; now derived from the same HomeGroup list the badges
// themselves are rendered from, so a custom instance's own vocabulary gets
// the same reserved-slot treatment for free).
func tagDisplayLimit(tags []string, groups []HomeGroup) int {
	limit := 5
	for _, g := range groups {
		if tagsAnyOf(tags, g.Members) {
			limit--
		}
	}
	if limit < 0 {
		limit = 0
	}
	return limit
}

// tagI18nKeys maps a controlled-vocabulary tag slug to its i18n translation
// key, for tags whose public display name should be localized rather than
// the tag's static English tags.name in the DB (#1094). Slugs not listed
// here fall back to the DB name — which, per #1173, is also how a custom
// (non-balfolk) instance's own tags.yaml vocabulary is expected to work:
// only the shipped default vocabulary keeps these i18n overrides.
//
// bal-folk was the sole entry before #1173: the slug means "the Balfolk
// dance style" for matching purposes, but publicly it should read as the
// style-agnostic "Ball". workshop/festival/session/concert were added
// alongside #1173's dynamic home-page button row so those 4 buttons keep
// reading from the same translated i18n keys index.html's old hardcoded
// markup used (filter_type_workshop/filter_type_festival/tag_session/
// tag_concert), rather than silently falling back to the DB's untranslated
// English tags.name once the row started being generated from tag data.
var tagI18nKeys = map[string]string{
	"bal-folk": "tag_ball",
	"workshop": "filter_type_workshop",
	"festival": "filter_type_festival",
	"session":  "tag_session",
	"concert":  "tag_concert",
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

// HomeGroup is one home-page format-selector button (#1173): the union of
// every tag sharing a home_group, or a solo tag with its own emoji and no
// home_group (which defaults to its own slug). Ordered by the group's
// earliest member's sort_order — the declaration order tags.yaml seeds tags
// in — which is what makes both the button row order and the map-marker
// type priority (index.html's eventIcon() takes the first matching group)
// deterministic and, for the shipped default vocabulary, preserves the
// pre-#1173 hardcoded order and priority (festival > ball > concert >
// workshop > session) exactly.
type HomeGroup struct {
	Key     string
	Emoji   string
	Color   string
	Label   string
	Members []string
}

// homeGroups computes the ordered home-page format-selector buttons from
// the tags vocabulary. Only tags with a non-empty Emoji get a home-page
// presence at all — this is what makes a tag like open-air (no emoji, no
// home_group) invisible on the home page, matching its behavior before
// #1173. The first tag (by SortOrder) seen for a given group key supplies
// that button's emoji/color/label; later members of the same group only
// contribute their slug to Members.
func homeGroups(strs I18nStrings, tagMap map[string]Tag) []HomeGroup {
	tags := make([]Tag, 0, len(tagMap))
	for _, t := range tagMap {
		tags = append(tags, t)
	}
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].SortOrder < tags[j].SortOrder })

	var groups []HomeGroup
	idx := map[string]int{}
	for _, t := range tags {
		if t.Emoji == "" {
			continue
		}
		key := t.HomeGroup
		if key == "" {
			key = t.Slug
		}
		if i, ok := idx[key]; ok {
			groups[i].Members = append(groups[i].Members, t.Slug)
			continue
		}
		idx[key] = len(groups)
		groups = append(groups, HomeGroup{
			Key:     key,
			Emoji:   t.Emoji,
			Color:   t.Color,
			Label:   tagLabel(strs, t.Slug, t.Name),
			Members: []string{t.Slug},
		})
	}
	return groups
}

// homeGroupsJSONEntry is homeGroups()'s per-button JSON shape for
// index.html's client-side filter/map JS. Members is included so the map's
// per-marker group flags can be derived client-side from each geoEvent's
// raw Tags list (eventsToGeo deliberately doesn't precompute per-group
// booleans — index.html is the only eventsGeoJSON consumer that needs
// them, and changing eventsGeoJSON's signature to thread a TagMap through
// would ripple into every other page that calls it: instructor/musician/
// org/tag/festivals — none of which read event type at all).
type homeGroupsJSONEntry struct {
	Key     string   `json:"key"`
	Emoji   string   `json:"emoji"`
	Color   string   `json:"color"`
	Label   string   `json:"label"`
	Members []string `json:"members"`
}

// homeGroupsJSON emits homeGroups() as a JSON array, in the same
// declaration order, for index.html's HOME_GROUPS JS bootstrap.
func homeGroupsJSON(strs I18nStrings, tagMap map[string]Tag) template.JS {
	groups := homeGroups(strs, tagMap)
	out := make([]homeGroupsJSONEntry, 0, len(groups))
	for _, g := range groups {
		out = append(out, homeGroupsJSONEntry{Key: g.Key, Emoji: g.Emoji, Color: g.Color, Label: g.Label, Members: g.Members})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return template.JS("[]")
	}
	return template.JS(b)
}

// tagsAnyOf reports whether any of tags appears in members — used to test
// whether an event (its Tags) belongs to a HomeGroup (its Members).
func tagsAnyOf(tags, members []string) bool {
	for _, t := range tags {
		for _, m := range members {
			if t == m {
				return true
			}
		}
	}
	return false
}

var tmplFuncsTags = template.FuncMap{
	"limitTags": func(tags []string, groups []HomeGroup) []string {
		if limit := tagDisplayLimit(tags, groups); len(tags) > limit {
			return tags[:limit]
		}
		return tags
	},
	"hiddenTagCount": func(tags []string, groups []HomeGroup) int {
		if limit := tagDisplayLimit(tags, groups); len(tags) > limit {
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
	// homeGroups/homeGroupsJSON/tagsAnyOf power the home-page format-selector
	// button row (#1173) — see their doc comments above.
	"homeGroups":     homeGroups,
	"homeGroupsJSON": homeGroupsJSON,
	"tagsAnyOf":      tagsAnyOf,
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
