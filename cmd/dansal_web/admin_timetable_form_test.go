package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSmokeRenderAdminTimetable exercises the timetable editor template with
// both a default-palette event (no custom timetable_tracks) and a
// custom-palette one (#1174), catching template execution errors that a Go
// build alone wouldn't (e.g. a bad range/field reference inside the new
// per-event track <option> loop).
func TestSmokeRenderAdminTimetable(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/admin/events/1/timetable", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	render := func(name string, data TimetablePageData) {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			renderTemplate(rec, tmpls.adminTimetable, tmplData(req, cfg, i18n, "test", data))
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, body)
			}
			if len(body) == 0 {
				t.Fatal("empty body")
			}
		})
	}

	baseEvent := Event{ID: 1, Title: "Test Festival", StartTime: "2026-09-15T18:00:00Z", EndTime: "2026-09-16T02:00:00Z"}

	render("default-palette", TimetablePageData{
		Event: func() Event {
			e := baseEvent
			e.TimetableTracks = []TimetableTrack{
				{Slug: "bal", Name: "Bal", Color: "rgba(74,127,203,.78)"},
				{Slug: "workshop", Name: "Workshop", Color: "rgba(201,80,10,.78)"},
			}
			return e
		}(),
	})

	render("custom-palette", TimetablePageData{
		Event: func() Event {
			e := baseEvent
			e.TimetableTracks = []TimetableTrack{
				{Slug: "keynote", Name: "Keynote", Color: "#123456"},
			}
			return e
		}(),
	})
}
