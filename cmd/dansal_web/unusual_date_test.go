package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// #1413: unusual-date warnings around publishing.

func TestUnusualDate(t *testing.T) {
	now := time.Now()
	cases := map[string]string{
		now.AddDate(0, 0, -1).Format(time.RFC3339): "past",
		now.Add(time.Minute).Format(time.RFC3339):  "",
		now.AddDate(1, 0, 0).Format(time.RFC3339):  "",
		now.AddDate(2, 0, 3).Format(time.RFC3339):  "far",
		"not a date": "",
	}
	for in, want := range cases {
		if got := unusualDate(in); got != want {
			t.Errorf("unusualDate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPublishFlashRedirect(t *testing.T) {
	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://example.test/admin/events/7/publish", nil)
		r.Header.Set("Referer", "http://example.test/admin/events?unpublished=1&pubmsg=stale")
		return r
	}

	t.Run("no unusual events: plain redirect, stale pubmsg dropped", func(t *testing.T) {
		rec := httptest.NewRecorder()
		publishFlashRedirect(rec, newReq(), "/admin/events", nil)
		if loc := rec.Header().Get("Location"); loc != "/admin/events?unpublished=1" {
			t.Errorf("Location = %q", loc)
		}
	})

	t.Run("unusual events: one fresh pubmsg carrying them", func(t *testing.T) {
		rec := httptest.NewRecorder()
		items := []PublishedUnusualEvent{{ID: 7, Title: "Bal", Kind: "past"}}
		publishFlashRedirect(rec, newReq(), "/admin/events", items)
		u, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		toks := u.Query()["pubmsg"]
		if len(toks) != 1 || toks[0] == "stale" {
			t.Fatalf("pubmsg = %q, want one fresh token (Location %s)", toks, u)
		}
		got := flashTake(toks[0]).PublishedUnusual
		if len(got) != 1 || got[0] != items[0] {
			t.Errorf("flash = %+v, want %+v", got, items)
		}
	})
}

func TestUnusualDateUIRenders(t *testing.T) {
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

	tok := newErrorID()
	flashRedirectParam(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "/", "pubmsg", tok,
		FlashMsg{PublishedUnusual: []PublishedUnusualEvent{{ID: 7, Title: "Old Bal", Kind: "past"}, {ID: 8, Title: "Typo Year", Kind: "far"}}})
	req := withSessionUser(httptest.NewRequest(http.MethodGet, "/admin/events/7/edit?pubmsg="+tok, nil), &SessionUser{ID: 1, Role: "admin"})

	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.adminEventForm, tmplData(req, cfg, i18n, "test", AdminEventFormData{
		Event: Event{ID: 7, Title: "Old Bal", StartTime: time.Now().AddDate(0, -3, 0).Format(time.RFC3339)},
	}))
	body := rec.Body.String()
	checkInlineJS(t, body)
	for _, want := range []string{
		`class="publish-unusual-flash"`,
		`Old Bal — <a href="/admin/events/7/edit">`,
		`Typo Year — <a href="/admin/events/8/edit">`,
		`id="unusual-date-dialog"`,
		`id="date-warn-note"`,
		`markPast: true`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("admin event form missing %s", want)
		}
	}
	if strings.Contains(body, "published_unusual_") || strings.Contains(body, "unusual_date_") {
		t.Error("untranslated unusual-date i18n key in page")
	}

	// The flash is one-time: a second render of the same URL is clean.
	rec = httptest.NewRecorder()
	renderTemplate(rec, tmpls.adminEventForm, tmplData(req, cfg, i18n, "test", AdminEventFormData{Event: Event{ID: 7}}))
	if strings.Contains(rec.Body.String(), `class="publish-unusual-flash"`) {
		t.Error("publish flash shown twice")
	}
}

func TestUnusualDateListPages(t *testing.T) {
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
	req := withSessionUser(httptest.NewRequest(http.MethodGet, "/admin/events", nil), &SessionUser{ID: 1, Role: "admin"})
	past := Event{ID: 5, Title: "Old", StartTime: time.Now().AddDate(0, -7, 0).Format(time.RFC3339), EmailVerified: true}

	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.adminEvents, tmplData(req, cfg, i18n, "test", AdminEventsData{
		Events:               []Event{past},
		UnpublishedPastCount: 3,
		UnpublishedPastURL:   "/admin/events?unpublished=1&include_past=1&date_to=2026-09-30",
	}))
	body := rec.Body.String()
	if !strings.Contains(body, `class="unpublished-past-notice"`) || !strings.Contains(body, "date_to=2026-09-30") {
		t.Error("admin events: missing unpublished-past notice")
	}
	if !strings.Contains(body, `class="badge-unusual-date"`) {
		t.Error("admin events: missing unusual-date badge on unpublished past row")
	}

	rec = httptest.NewRecorder()
	renderTemplate(rec, tmpls.adminEventsMaintenance, tmplData(req, cfg, i18n, "test", AdminEventsMaintenanceData{Events: []Event{past}}))
	body = rec.Body.String()
	checkInlineJS(t, body)
	for _, want := range []string{`data-date-check-bulk=".em-cb"`, `class="em-cb" data-date="`, `class="badge-unusual-date"`} {
		if !strings.Contains(body, want) {
			t.Errorf("maintenance: missing %s", want)
		}
	}
}
