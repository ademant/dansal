package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1469: "this event is over" notice + org/venue upcoming-event hints,
// visible to every visitor (not gated on a session), rendered server-side.

func TestEventOverNoticeAndHints(t *testing.T) {
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
	req.AddCookie(&http.Cookie{Name: "dsw_lang", Value: "en"})

	render := func(ev Event, data EventData) string {
		t.Helper()
		data.Event = ev
		td := tmplData(req, cfg, i18n, ev.Title, data)
		rec := httptest.NewRecorder()
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

	t.Run("upcoming event: no notice at all", func(t *testing.T) {
		ev := Event{ID: 1, Title: "Future Ball", StartTime: "2099-07-01T20:00:00+02:00", EndTime: "2099-07-01T23:00:00+02:00"}
		body := render(ev, EventData{})
		// Checks for the rendered element, not the bare substring -- the
		// <style> block's own .evt-over-notice{...} rule would also match
		// regardless of whether the element is actually on the page.
		if strings.Contains(body, `class="evt-over-notice"`) {
			t.Errorf("upcoming event must not show the 'over' notice, body tail: %s", body[max(0, len(body)-2000):])
		}
	})

	t.Run("past event: notice shown, no hints when neither has upcoming events", func(t *testing.T) {
		ev := Event{ID: 2, Title: "Past Ball", StartTime: "2020-07-01T20:00:00+02:00", EndTime: "2020-07-01T23:00:00+02:00"}
		body := render(ev, EventData{})
		if !strings.Contains(body, `class="evt-over-notice"`) {
			t.Fatalf("missing the 'over' notice, body tail: %s", body[max(0, len(body)-2000):])
		}
		if !strings.Contains(body, "took place on") {
			t.Errorf("notice text missing, body tail: %s", body[max(0, len(body)-2000):])
		}
		if strings.Contains(body, `class="evt-over-hints"`) {
			t.Errorf("no hints expected when both counts are 0")
		}
	})

	t.Run("past event: org hint only", func(t *testing.T) {
		ev := Event{ID: 3, Title: "Past Ball", StartTime: "2020-07-01T20:00:00+02:00", EndTime: "2020-07-01T23:00:00+02:00"}
		body := render(ev, EventData{
			Org: &Organization{Name: "Balfolk Chemnitz"}, OrgSlug: "balfolk-chemnitz",
			ShowOrgUpcoming: true, OrgUpcomingCount: 3,
		})
		if !strings.Contains(body, `href="/org/balfolk-chemnitz"`) {
			t.Errorf("missing org link, body tail: %s", body[max(0, len(body)-2000):])
		}
		if !strings.Contains(body, "3 upcoming event(s) by Balfolk Chemnitz") {
			t.Errorf("missing org hint text, body tail: %s", body[max(0, len(body)-2000):])
		}
		if strings.Contains(body, "/location/") {
			t.Errorf("no venue link expected, body tail: %s", body[max(0, len(body)-2000):])
		}
	})

	t.Run("past event: venue hint only, linking to the event's own location", func(t *testing.T) {
		locID := 42
		ev := Event{
			ID: 4, Title: "Past Ball", StartTime: "2020-07-01T20:00:00+02:00", EndTime: "2020-07-01T23:00:00+02:00",
			LocationID: &locID, Location: &Location{ID: locID, Location: "Fichtehaus Tübingen"},
		}
		body := render(ev, EventData{ShowVenueUpcoming: true, VenueUpcomingCount: 2})
		if !strings.Contains(body, `href="/location/42"`) {
			t.Errorf("missing venue link, body tail: %s", body[max(0, len(body)-2000):])
		}
		if !strings.Contains(body, "2 upcoming event(s) at Fichtehaus Tübingen") {
			t.Errorf("missing venue hint text, body tail: %s", body[max(0, len(body)-2000):])
		}
	})

	t.Run("past event: org and venue both shown when sets genuinely differ", func(t *testing.T) {
		locID := 42
		ev := Event{
			ID: 5, Title: "Past Ball", StartTime: "2020-07-01T20:00:00+02:00", EndTime: "2020-07-01T23:00:00+02:00",
			LocationID: &locID, Location: &Location{ID: locID, Location: "Fichtehaus Tübingen"},
		}
		body := render(ev, EventData{
			Org: &Organization{Name: "Balfolk Chemnitz"}, OrgSlug: "balfolk-chemnitz",
			ShowOrgUpcoming: true, OrgUpcomingCount: 3,
			ShowVenueUpcoming: true, VenueUpcomingCount: 2,
		})
		if !strings.Contains(body, "/org/balfolk-chemnitz") || !strings.Contains(body, "/location/42") {
			t.Errorf("expected both links, body tail: %s", body[max(0, len(body)-2000):])
		}
	})

	t.Run("past event: ShowVenueUpcoming=false (dedup decided upstream) hides the venue line even with a positive count", func(t *testing.T) {
		locID := 42
		ev := Event{
			ID: 6, Title: "Past Ball", StartTime: "2020-07-01T20:00:00+02:00", EndTime: "2020-07-01T23:00:00+02:00",
			LocationID: &locID, Location: &Location{ID: locID, Location: "Fichtehaus Tübingen"},
		}
		body := render(ev, EventData{
			Org: &Organization{Name: "Balfolk Chemnitz"}, OrgSlug: "balfolk-chemnitz",
			ShowOrgUpcoming: true, OrgUpcomingCount: 3,
			ShowVenueUpcoming: false, VenueUpcomingCount: 3,
		})
		// /location/42 legitimately appears elsewhere on the page regardless
		// (JSON-LD, breadcrumb, the venue detail sidebar link) -- check for
		// the hint text itself, not the bare href.
		if strings.Contains(body, "upcoming event(s) at") {
			t.Errorf("venue hint should be suppressed, body tail: %s", body[max(0, len(body)-2000):])
		}
		if !strings.Contains(body, "/org/balfolk-chemnitz") {
			t.Errorf("org line should still show, body tail: %s", body[max(0, len(body)-2000):])
		}
	})
}
