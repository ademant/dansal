package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRecurringSeriesFromEvents exercises the org-page recurring-series
// derivation (#1185): dedup by series, skip events with no series or no
// cadence, and pick the first (soonest) occurrence per series since the
// input events slice is already chronologically ordered.
func TestRecurringSeriesFromEvents(t *testing.T) {
	s1, s2 := 1, 2
	events := []Event{
		{ID: 10, Title: "One-off event", StartTime: "2026-09-01T20:00:00"}, // no series
		{ID: 11, Title: "Balfolk am Elisenbrunnen", SeriesID: &s1, SeriesCadence: "every 2nd + 4th Thursday", StartTime: "2026-09-11T18:00:00", Location: &Location{ShortName: "Elisenbrunnen"}},
		{ID: 12, Title: "Balfolk am Elisenbrunnen", SeriesID: &s1, SeriesCadence: "every 2nd + 4th Thursday", StartTime: "2026-09-25T18:00:00"}, // later occurrence of the same series -- must be skipped
		{ID: 13, Title: "Session ohne Cadence", SeriesID: &s2, SeriesCadence: "", StartTime: "2026-09-05T20:00:00"},                             // series with no cadence set -- excluded
	}

	got := recurringSeriesFromEvents(events)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.SeriesID != s1 || e.NextEventID != 11 || e.Cadence != "every 2nd + 4th Thursday" || e.LocationName != "Elisenbrunnen" {
		t.Errorf("unexpected entry: %+v", e)
	}
}

func TestRecurringSeriesFromEventsEmpty(t *testing.T) {
	if got := recurringSeriesFromEvents(nil); got != nil {
		t.Errorf("recurringSeriesFromEvents(nil) = %v, want nil", got)
	}
}

// TestSmokeRenderOrgRecurringSection renders org.html with and without
// RecurringSeries data (#1185): the section must appear only when there is
// at least one entry, and link to that series' next occurrence.
func TestSmokeRenderOrgRecurringSection(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/org/test-org", nil)

	render := func(data OrgData) string {
		rec := httptest.NewRecorder()
		td := tmplData(req, cfg, i18n, "test", data)
		renderTemplate(rec, tmpls.org, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		if strings.Contains(string(body), "template error") {
			t.Fatalf("template execution error, body tail: %s", body[max(0, len(body)-500):])
		}
		return string(body)
	}

	t.Run("no recurring series: section absent", func(t *testing.T) {
		body := render(OrgData{Org: Organization{ID: 2, Name: "Test Org"}, Slug: "test-org"})
		if strings.Contains(body, `class="recurring-list"`) {
			t.Error("did not expect the recurring-events section when RecurringSeries is empty")
		}
	})

	t.Run("with a recurring series", func(t *testing.T) {
		// Deliberately no "+" in the cadence text here: html/template
		// numeric-entity-escapes "+" to "&#43;" in text nodes, which is
		// correct/harmless HTML but would make a literal substring match
		// against "+" fail.
		body := render(OrgData{
			Org: Organization{ID: 2, Name: "Test Org"}, Slug: "test-org",
			RecurringSeries: []SeriesCadenceEntry{
				{SeriesID: 1, Title: "Balfolk am Elisenbrunnen", Cadence: "every 2nd and 4th Thursday, except holidays", NextEventID: 42, LocationName: "Elisenbrunnen"},
			},
		})
		if !strings.Contains(body, `class="recurring-list"`) {
			t.Fatal("expected the recurring-events section to render")
		}
		if !strings.Contains(body, `href="/events/42"`) {
			t.Error("expected the series entry to link to its next occurrence")
		}
		if !strings.Contains(body, "every 2nd and 4th Thursday, except holidays") {
			t.Error("expected the cadence text to render")
		}
	})
}

// TestSmokeRenderEventCadence renders event.html with and without
// Event.SeriesCadence set (#1185): the series-nav block must render (to
// carry the cadence line) even when there is no prev/next sibling to link,
// and must stay absent entirely when there's neither siblings nor a cadence.
func TestSmokeRenderEventCadence(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/events/1", nil)

	render := func(ev Event) string {
		rec := httptest.NewRecorder()
		td := tmplData(req, cfg, i18n, ev.Title, EventData{Event: ev})
		renderTemplate(rec, tmpls.event, td)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, body)
		}
		if strings.Contains(string(body), "template error") {
			t.Fatalf("template execution error, body tail: %s", body[max(0, len(body)-500):])
		}
		return string(body)
	}

	sid := 1
	t.Run("with cadence, no siblings", func(t *testing.T) {
		// No "+" in the cadence text: html/template numeric-entity-escapes
		// it to "&#43;" in text nodes (correct/harmless HTML, but breaks a
		// literal substring match).
		body := render(Event{ID: 1, Title: "Balfolk am Elisenbrunnen", StartTime: "2026-09-11T18:00:00", SeriesID: &sid, SeriesCadence: "every 2nd and 4th Thursday, except holidays"})
		if !strings.Contains(body, `class="series-cadence"`) {
			t.Error("expected the cadence line to render even without prev/next siblings")
		}
		if !strings.Contains(body, "every 2nd and 4th Thursday, except holidays") {
			t.Error("expected the cadence text to render")
		}
	})

	t.Run("no cadence, no siblings: nothing renders", func(t *testing.T) {
		body := render(Event{ID: 2, Title: "One-off event", StartTime: "2026-09-11T18:00:00"})
		if strings.Contains(body, `class="series-cadence"`) || strings.Contains(body, `class="series-nav"`) {
			t.Error("did not expect any series UI for an event with no series")
		}
	})
}

// TestSmokeRenderAdminSeriesEditCadence renders admin_series_edit.html with
// Series.Cadence set, asserting the field pre-fills correctly (#1185).
func TestSmokeRenderAdminSeriesEditCadence(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/admin/series/1/edit", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	rec := httptest.NewRecorder()
	// No "+" in the cadence text: html/template numeric-entity-escapes it to
	// "&#43;" (correct/harmless HTML, but breaks a literal substring match).
	data := AdminSeriesEditData{
		Series:  EventSeries{ID: 1, Title: "Balfolk am Elisenbrunnen", Cadence: "every 2nd and 4th Thursday, except holidays"},
		IsAdmin: true,
	}
	renderTemplate(rec, tmpls.adminSeriesEdit, tmplData(req, cfg, i18n, "test", data))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}
	if strings.Contains(string(body), "template error") {
		t.Fatalf("template execution error, body tail: %s", body[max(0, len(body)-500):])
	}
	if !strings.Contains(string(body), `name="cadence"`) {
		t.Error("expected a cadence input field")
	}
	if !strings.Contains(string(body), `value="every 2nd and 4th Thursday, except holidays"`) {
		t.Error("expected the cadence field to pre-fill with the series' stored value")
	}
}
