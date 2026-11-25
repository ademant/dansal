package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// renderEventPage renders the event template the way eventHandler does,
// including the OG type and locale it sets (#1898).
func renderEventPage(t *testing.T, ev Event, lang string) string {
	t.Helper()
	siteCfg = testSiteSettings(t)
	tmpls := loadTemplates()
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test"}
	req := httptest.NewRequest(http.MethodGet, "/events/1", nil)
	rec := httptest.NewRecorder()
	td := tmplData(req, cfg, i18n, "test", EventData{Event: ev})
	td.OGType = "article"
	td.OGLocale = ogLocale(lang)
	renderTemplate(rec, tmpls.event, td)
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}
	return string(body)
}

// #1898: event pages declare og:type article and the page language's locale;
// other pages keep og:type website.
func TestEventPageOGTypeAndLocale(t *testing.T) {
	body := renderEventPage(t, Event{
		ID:        7,
		Title:     "Bal Test",
		StartTime: "2026-08-05T20:00:00Z",
		EndTime:   "2026-08-05T23:00:00Z",
	}, "de")
	if n := strings.Count(body, `property="og:type"`); n != 1 {
		t.Errorf("event page has %d og:type tags, want 1", n)
	}
	if !strings.Contains(body, `<meta property="og:type" content="article">`) {
		t.Error("event page og:type is not article")
	}
	if !strings.Contains(body, `<meta property="og:locale" content="de_DE">`) {
		t.Error("event page og:locale missing for the German default")
	}
	if got := ogLocale("xx"); got != "" {
		t.Errorf("ogLocale(unknown) = %q, want empty", got)
	}
}

// #1901: shared styles are linked, not inlined, in the head.
func TestBaseStylesheetIsLinked(t *testing.T) {
	body := renderEventPage(t, Event{ID: 8, Title: "CSS", StartTime: "2026-08-05T20:00:00Z"}, "de")
	if !strings.Contains(body, `<link rel="stylesheet" href="/static/base.css?v=`) {
		t.Error("base.css is not linked")
	}
	if !strings.Contains(body, `<link rel="stylesheet" href="/static/event.css?v=`) {
		t.Error("event.css is not linked")
	}
	if strings.Contains(body, ":root{") {
		t.Error("shared CSS is still inlined in the page")
	}
}

// #1894: organisation, venue, musician and instructor descriptions name the
// place and the next dates, and fall back to the entity's own text.
func TestEntityTitlesAndDescriptions(t *testing.T) {
	strs := loadI18n("").Strings("de")
	at := func(days int) string { return time.Now().AddDate(0, 0, days).UTC().Format(time.RFC3339) }
	events := []Event{
		{ID: 1, Title: "A", StartTime: at(30), Location: &Location{Location: "Hall", Town: "Frankfurt"}},
		{ID: 2, Title: "B", StartTime: at(37), Location: &Location{Location: "Hall", Town: "Frankfurt"}},
		{ID: 3, Title: "C", StartTime: at(38), IsCancelled: true, Location: &Location{Location: "Hall", Town: "Frankfurt"}},
	}
	if got := orgSEOTitle(strs, "Folkclub XY", events); got != "Folkclub XY – Balfolk in Frankfurt" {
		t.Errorf("org title = %q", got)
	}
	other := append(events, Event{ID: 4, Location: &Location{Location: "Elsewhere", Town: "Kassel"}})
	if got := orgSEOTitle(strs, "Folkclub XY", other); got != "Folkclub XY – Balfolk" {
		t.Errorf("org title with two towns = %q", got)
	}
	if got := venueSEOTitle(strs, "KFZ", "Marburg"); got != "KFZ Marburg – Balfolk" {
		t.Errorf("venue title = %q", got)
	}
	if got := venueSEOTitle(strs, "Kulturhaus Marburg", "Marburg"); got != "Kulturhaus Marburg" {
		t.Errorf("venue title repeats the town: %q", got)
	}

	desc := entityDescription(strs, "seo_entity_desc", "Folkclub XY", events, "de", "", "Der Verein seit 1990.")
	if !strings.HasPrefix(desc, "Folkclub XY: nächste Termine am ") || !strings.Contains(desc, " in Frankfurt") {
		t.Errorf("org description = %q", desc)
	}
	if !strings.Contains(desc, "Der Verein seit 1990.") {
		t.Errorf("own text missing from description: %q", desc)
	}
	cancelledDate := formatDateStr("de", "", events[2].StartTime)
	if strings.Contains(desc, cancelledDate) {
		t.Errorf("cancelled event listed in description: %q", desc)
	}
	if n := len([]rune(desc)); n > metaDescMaxLen+1 {
		t.Errorf("description has %d runes, limit %d", n, metaDescMaxLen)
	}

	// Without upcoming events the own text stands alone.
	if got := entityDescription(strs, "seo_entity_desc", "Akleija", nil, "de", "", "Band aus Hamburg."); got != "Band aus Hamburg." {
		t.Errorf("no-events description = %q", got)
	}
}
