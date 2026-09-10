package main

import "testing"

// TestTagLabel verifies the bal-folk -> "Ball" override (#1094) and the
// fallback chain: i18n translation, then the DB name, then the bare slug.
func TestTagLabel(t *testing.T) {
	strs := I18nStrings{"tag_ball": "Ball"}

	if got := tagLabel(strs, "bal-folk", "Bal Folk"); got != "Ball" {
		t.Errorf("tagLabel(bal-folk) = %q, want %q (i18n override should win over DB name)", got, "Ball")
	}
	if got := tagLabel(I18nStrings{}, "bal-folk", "Bal Folk"); got != "Bal Folk" {
		t.Errorf("tagLabel(bal-folk, no translation) = %q, want %q (should fall back to DB name)", got, "Bal Folk")
	}
	if got := tagLabel(I18nStrings{}, "bal-folk", ""); got != "bal-folk" {
		t.Errorf("tagLabel(bal-folk, no translation, no DB name) = %q, want %q (should fall back to slug)", got, "bal-folk")
	}
	// "festival" is in tagI18nKeys (#1173, added alongside the dynamic
	// home-page button row) but strs here has no translation loaded for its
	// key (filter_type_festival) — same "translation missing" fallback path
	// as an unregistered slug, so this should still fall back to the DB name.
	if got := tagLabel(strs, "festival", "Festival"); got != "Festival" {
		t.Errorf("tagLabel(festival) = %q, want %q (tagI18nKeys entry present but untranslated here, should use DB name)", got, "Festival")
	}
	// A custom (non-balfolk) instance's own tag: no tagI18nKeys entry at
	// all, same fallback.
	if got := tagLabel(strs, "tango", "Tango"); got != "Tango" {
		t.Errorf("tagLabel(tango) = %q, want %q (no tagI18nKeys entry, should use DB name)", got, "Tango")
	}
	// The i18n override IS used when the key actually resolves.
	strsFest := I18nStrings{"filter_type_festival": "Festival Translated"}
	if got := tagLabel(strsFest, "festival", "Festival"); got != "Festival Translated" {
		t.Errorf("tagLabel(festival, translated) = %q, want %q (i18n override should win)", got, "Festival Translated")
	}
}

// TestEventKeywords covers #1295: dance names, translated tags, location
// town/region/country, and musician names combine into one deduplicated
// list, with blanks dropped and nil (not []) returned when nothing at all
// is known.
func TestEventKeywords(t *testing.T) {
	strs := I18nStrings{"tag_ball": "Ball"}
	tagMap := map[string]Tag{"tango": {Slug: "tango", Name: "Tango Argentino"}}

	t.Run("combines and translates every source", func(t *testing.T) {
		ev := Event{
			DanceNames: []string{"An Dro"},
			Tags:       []string{"bal-folk", "tango"},
			Location:   &Location{Town: "Rennes", Region: "Bretagne", CountryCode: "FR", Country: "France"},
			Musicians:  []Musician{{Bandname: "Duo Vague"}},
		}
		got := eventKeywords(strs, tagMap, ev)
		want := []string{"An Dro", "Ball", "Tango Argentino", "Rennes", "Bretagne", "France"}
		// Musician name is appended last.
		want = append(want, "Duo Vague")
		if len(got) != len(want) {
			t.Fatalf("eventKeywords() = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("eventKeywords()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
			}
		}
	})

	t.Run("nothing known returns nil, not empty slice", func(t *testing.T) {
		if got := eventKeywords(strs, tagMap, Event{}); got != nil {
			t.Errorf("eventKeywords(empty event) = %v, want nil", got)
		}
	})

	t.Run("blank/duplicate location fields are dropped", func(t *testing.T) {
		// A location with an empty town/region/country (and no alias) must
		// not inject blank entries; a dance name identical to a tag label
		// must not be duplicated.
		ev := Event{
			DanceNames: []string{"Ball"},
			Tags:       []string{"bal-folk"},
			Location:   &Location{},
		}
		got := eventKeywords(strs, tagMap, ev)
		want := []string{"Ball"}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("eventKeywords() = %v, want %v (dedup + blank location fields dropped)", got, want)
		}
	})
}

// TestHomeGroups verifies the default balfolk vocabulary's grouping
// (bal-folk+fest-noz -> one "ball" button, the 4 workshop variants -> one
// "workshop" button, festival/session/concert each solo), that a tag
// without an emoji gets no home-page presence at all (open-air, matching
// its pre-#1173 behavior), and that group order follows SortOrder.
func TestHomeGroups(t *testing.T) {
	strs := I18nStrings{}
	tagMap := map[string]Tag{
		"festival":  {Slug: "festival", Name: "Festival", Emoji: "🎪", HomeGroup: "festival", Color: "#7c3aed", SortOrder: 0},
		"bal-folk":  {Slug: "bal-folk", Name: "Ball", Emoji: "💃", HomeGroup: "ball", Color: "#1a6eb5", SortOrder: 1},
		"fest-noz":  {Slug: "fest-noz", Name: "Fest Noz", Emoji: "💃", HomeGroup: "ball", Color: "#1a6eb5", SortOrder: 2},
		"workshop":  {Slug: "workshop", Name: "Workshop", Emoji: "🎓", HomeGroup: "workshop", Color: "#d97706", SortOrder: 4},
		"session":   {Slug: "session", Name: "Session", Emoji: "🎶", HomeGroup: "session", Color: "#0a5a9c", SortOrder: 8},
		"open-air":  {Slug: "open-air", Name: "Open Air", SortOrder: 9},
		"beginners": {Slug: "beginners", Name: "Beginners", Category: "level", SortOrder: 10},
	}

	groups := homeGroups(strs, tagMap)
	if len(groups) != 4 {
		t.Fatalf("expected 4 home groups (festival, ball, workshop, session), got %d: %+v", len(groups), groups)
	}
	if groups[0].Key != "festival" || groups[1].Key != "ball" || groups[2].Key != "workshop" || groups[3].Key != "session" {
		t.Fatalf("expected group order festival,ball,workshop,session (by SortOrder), got %v", []string{groups[0].Key, groups[1].Key, groups[2].Key, groups[3].Key})
	}
	ball := groups[1]
	if len(ball.Members) != 2 || !tagsAnyOf(ball.Members, []string{"bal-folk"}) || !tagsAnyOf(ball.Members, []string{"fest-noz"}) {
		t.Fatalf("expected the ball group to have both bal-folk and fest-noz as members, got %v", ball.Members)
	}
	if ball.Emoji != "💃" || ball.Color != "#1a6eb5" {
		t.Fatalf("expected the ball group's emoji/color to come from its first (SortOrder-lowest) member bal-folk, got emoji=%q color=%q", ball.Emoji, ball.Color)
	}
}

func TestTagsAnyOf(t *testing.T) {
	if !tagsAnyOf([]string{"a", "b"}, []string{"b", "c"}) {
		t.Error("expected an overlap to be found")
	}
	if tagsAnyOf([]string{"a", "b"}, []string{"c", "d"}) {
		t.Error("expected no overlap to be found")
	}
	if tagsAnyOf(nil, []string{"a"}) {
		t.Error("expected no overlap with an empty tags slice")
	}
}
