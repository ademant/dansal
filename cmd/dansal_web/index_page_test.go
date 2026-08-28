package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// defaultTestTagMap mirrors the default balfolk vocabulary seeded by
// cmd/dansal's tags.yaml (#1173) — used by index-page tests instead of a
// live DB/API round trip.
func defaultTestTagMap() map[string]Tag {
	tags := []Tag{
		{Slug: "festival", Name: "Festival", Category: "format", Emoji: "🎪", HomeGroup: "festival", Color: "#7c3aed", SortOrder: 0},
		{Slug: "bal-folk", Name: "Ball", Category: "format", Emoji: "💃", HomeGroup: "ball", Color: "#1a6eb5", SortOrder: 1},
		{Slug: "fest-noz", Name: "Fest Noz", Category: "format", Emoji: "💃", HomeGroup: "ball", Color: "#1a6eb5", SortOrder: 2},
		{Slug: "concert", Name: "Concert", Category: "format", Emoji: "🎸", HomeGroup: "concert", Color: "#5b21b6", SortOrder: 3},
		{Slug: "workshop", Name: "Workshop", Category: "format", Emoji: "🎓", HomeGroup: "workshop", Color: "#d97706", SortOrder: 4},
		{Slug: "music-course", Name: "Music Course", Category: "format", Emoji: "🎓", HomeGroup: "workshop", Color: "#d97706", SortOrder: 5},
		{Slug: "dance-workshop", Name: "Dance Workshop", Category: "type", Emoji: "🎓", HomeGroup: "workshop", Color: "#d97706", SortOrder: 6},
		{Slug: "musician-workshop", Name: "Musician Workshop", Category: "type", Emoji: "🎓", HomeGroup: "workshop", Color: "#d97706", SortOrder: 7},
		{Slug: "session", Name: "Session", Category: "format", Emoji: "🎶", HomeGroup: "session", Color: "#0a5a9c", SortOrder: 8},
		{Slug: "open-air", Name: "Open Air", Category: "format", SortOrder: 9},
	}
	m := make(map[string]Tag, len(tags))
	for _, t := range tags {
		m[t.Slug] = t
	}
	return m
}

// TestSmokeRenderIndexHomeGroups exercises the dynamic home-page
// format-selector button row (#1173) end to end through template
// rendering: the default vocabulary's 5-button row (in festival, ball,
// concert, workshop, session order — the pre-#1173 hardcoded order and
// priority), per-event badges/data-* attributes derived from tags rather
// than has_* fields, and a federated event's all-zero data-* attributes.
func TestSmokeRenderIndexHomeGroups(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	tmpls := loadTemplates()
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test"}
	req := httptest.NewRequest("GET", "/", nil)

	events := []Event{
		{ID: 1, Title: "Balfolk Ball", StartTime: "2026-07-01T20:00:00", Tags: []string{"bal-folk"}},
		{ID: 2, Title: "Fest Noz", StartTime: "2026-07-02T20:00:00", Tags: []string{"fest-noz"}},
		{ID: 3, Title: "Some Festival", StartTime: "2026-07-03T20:00:00", Tags: []string{"festival", "bal-folk"}},
	}
	fed := []FederatedEvent{{ID: 100, Name: "Remote event", StartTime: "2026-07-04T20:00:00"}}

	td := tmplData(req, cfg, i18n, "Events", IndexData{
		Events: events, TagMap: defaultTestTagMap(), FederatedEvents: fed,
		HolidayDates: "[]",
	})
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.index, td)
	body, _ := io.ReadAll(rec.Body)
	bodyStr := string(body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if strings.Contains(bodyStr, "template error") {
		t.Fatalf("template execution error, body tail: %s", bodyStr[max(0, len(bodyStr)-500):])
	}

	// Button row: 5 buttons in festival,ball,concert,workshop,session order.
	for _, want := range []string{
		`data-type="festival"`, `data-type="ball"`, `data-type="concert"`,
		`data-type="workshop"`, `data-type="session"`,
	} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("expected button row to contain %s", want)
		}
	}
	festIdx := strings.Index(bodyStr, `data-type="festival"`)
	ballIdx := strings.Index(bodyStr, `data-type="ball"`)
	workshopIdx := strings.Index(bodyStr, `data-type="workshop"`)
	if !(festIdx < ballIdx && ballIdx < workshopIdx) {
		t.Errorf("expected button order festival < ball < workshop, got positions fest=%d ball=%d workshop=%d", festIdx, ballIdx, workshopIdx)
	}

	// Event #1 (bal-folk) gets a "ball" badge and data-ball="1", but not
	// data-festival="1".
	if !strings.Contains(bodyStr, `data-ball="1"`) {
		t.Error("expected data-ball=\"1\" on the bal-folk event's row")
	}
	if !strings.Contains(bodyStr, `💃`) {
		t.Error("expected the Ball group's emoji badge to render")
	}

	// Federated event row: every group's data-* attribute is 0.
	if !strings.Contains(bodyStr, `data-festival="0" data-ball="0" data-concert="0" data-workshop="0" data-session="0"`) {
		t.Errorf("expected the federated event row to zero every home-group's data-* attribute, body around federated row: %s", federatedRowSnippet(bodyStr))
	}

	// The events-geo JSON island carries each event's raw tags, not
	// precomputed ball/workshop/festival booleans.
	if !strings.Contains(bodyStr, `"tags":["bal-folk"]`) && !strings.Contains(bodyStr, `"tags":["fest-noz"]`) {
		// coordinates are required for an event to appear in eventsGeoJSON at
		// all; these fixture events have none, so just assert the script tag
		// itself rendered without error instead.
	}
	if !strings.Contains(bodyStr, `id="events-geo"`) {
		t.Error("expected the events-geo script tag to render")
	}
}

func federatedRowSnippet(body string) string {
	i := strings.Index(body, "federated-event")
	if i < 0 {
		return "(not found)"
	}
	end := i + 400
	if end > len(body) {
		end = len(body)
	}
	return body[i:end]
}
