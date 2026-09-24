package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTimetableTypeHelpers(t *testing.T) {
	strs := loadI18n("").Strings("en")
	tracks := []TimetableTrack{{Slug: "kaffee", Name: "Coffee break"}}
	for _, c := range []struct{ slug, label, badge, class string }{
		{"bal", "Bal", "bal", ""},
		{"workshop", "Workshop", "ws", "tt-workshop"},
		{"dance-workshop", "Dance workshop", "ws", "tt-workshop"},
		{"musician-workshop", "Musician workshop", "ws", "tt-workshop"},
		{"session", "Session", "bal", ""},
		{"break", "Break", "break", "tt-break"},
		{"meal", "Meal", "break", "tt-break"},
		{"kaffee", "Coffee break", "bal", ""}, // custom track: its own name, never "Bal"
		{"unknown-slug", "unknown-slug", "bal", ""},
	} {
		if got := ttTypeLabel(strs, tracks, c.slug); got != c.label {
			t.Errorf("ttTypeLabel(%q) = %q, want %q", c.slug, got, c.label)
		}
		if got := ttBadgeKind(c.slug); got != c.badge {
			t.Errorf("ttBadgeKind(%q) = %q, want %q", c.slug, got, c.badge)
		}
		if got := ttKindClass(c.slug); got != c.class {
			t.Errorf("ttKindClass(%q) = %q, want %q", c.slug, got, c.class)
		}
	}
}

// Event 973 on prod: a dance-workshop entry was rendered with a "Bal" badge.
func TestEmbedTimetableShowsEntryTypes(t *testing.T) {
	tmpls := loadTemplates()
	strs := loadI18n("").Strings("en")
	ev := Event{
		ID: 973, IsPublished: true, Title: "Schwedentanz",
		StartTime: "2026-10-31T15:00:00+01:00", EndTime: "2026-10-31T22:00:00+01:00",
		TimetableTracks: []TimetableTrack{{Slug: "kaffee", Name: "Coffee break"}},
		Timetable: []TimetableEntry{
			{ID: 1, StartTime: "15:00", EndTime: "18:00", Title: "Tanzworkshop", EntryType: "dance-workshop"},
			{ID: 2, StartTime: "18:00", EndTime: "19:00", Title: "Pause", EntryType: "kaffee"},
			{ID: 3, StartTime: "19:00", EndTime: "22:00", Title: "Tanzabend", EntryType: "bal"},
		},
	}
	rec := httptest.NewRecorder()
	renderEmbed(rec, tmpls.embedTimetable, map[string]any{
		"Lang": "en", "Nonce": "x", "Event": ev, "Strings": strs, "BaseURL": "https://example.test", "SiteName": "dansal",
	})
	body := rec.Body.String()
	if !strings.Contains(body, `<span class="tt-badge-ws">Dance workshop</span>`) {
		t.Errorf("dance-workshop entry must get the workshop badge and label:\n%s", body)
	}
	if !strings.Contains(body, `<span class="tt-badge-bal">Coffee break</span>`) {
		t.Errorf("custom track entry must show its own track name:\n%s", body)
	}
	if strings.Count(body, `>Bal</span>`) != 1 {
		t.Errorf("only the actual bal entry may be labelled Bal, got %d", strings.Count(body, `>Bal</span>`))
	}
	if !strings.Contains(body, "tt-workshop") {
		t.Error("workshop-family entry must get the tt-workshop class")
	}
}

// The event page's own row template (evt_tt_row): same regression on the
// public /events/N page, which is where event 973 was reported.
func TestEventPageTimetableRowShowsEntryType(t *testing.T) {
	tmpls := loadTemplates()
	strs := loadI18n("").Strings("de")
	render := func(entryType string) string {
		var sb strings.Builder
		err := tmpls.event.ExecuteTemplate(&sb, "evt_tt_row", map[string]any{
			"Entry":   TimetableEntry{ID: 165, StartTime: "15:00", EndTime: "18:00", Title: "Tanzworkshop", EntryType: entryType},
			"Strings": strs,
			"Tracks":  []TimetableTrack{{Slug: "kaffee", Name: "Kaffeepause"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return sb.String()
	}
	if got := render("dance-workshop"); !strings.Contains(got, `class="tt-badge-ws">Tanzworkshop</span>`) && !strings.Contains(got, `tt-badge-ws"`) || strings.Contains(got, `>Bal</span>`) {
		t.Errorf("dance-workshop row must not be a Bal:\n%s", got)
	}
	if got := render("dance-workshop"); !strings.Contains(got, "tt-workshop") {
		t.Errorf("dance-workshop row must get the tt-workshop class:\n%s", got)
	}
	if got := render("kaffee"); !strings.Contains(got, ">Kaffeepause</span>") {
		t.Errorf("custom track row must show its own name:\n%s", got)
	}
	if got := render("bal"); !strings.Contains(got, `tt-badge-bal">Bal</span>`) {
		t.Errorf("bal row must still be labelled Bal:\n%s", got)
	}
}
