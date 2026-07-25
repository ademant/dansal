package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSmokeRenderEventPage(t *testing.T) {
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

	render := func(name string, ev Event) {
		t.Run(name, func(t *testing.T) {
			td := tmplData(req, cfg, i18n, ev.Title, EventData{Event: ev})
			rec := httptest.NewRecorder()
			renderTemplate(rec, tmpls.event, td)
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, body)
			}
			if strings.Contains(string(body), "template error") {
				t.Fatalf("template execution error, body tail: %s", body[max(0, len(body)-500):])
			}
			if !strings.Contains(string(body), "</html>") {
				t.Fatalf("truncated render (no closing </html>), body tail: %s", body[max(0, len(body)-300):])
			}
		})
	}

	render("no-location", Event{ID: 1, Title: "No venue", StartTime: "2026-07-01T20:00:00"})

	buildingID := 10
	render("building-location", Event{
		ID: 2, Title: "At a building", StartTime: "2026-07-01T20:00:00",
		Location: &Location{ID: buildingID, Location: "Big Hall", Address: "Main St 1", Town: "Testville"},
	})

	roomParent := buildingID
	render("room-location", Event{
		ID: 3, Title: "In a room", StartTime: "2026-07-01T20:00:00",
		Location: &Location{ID: 11, Location: "Studio 2", ParentID: &roomParent, Address: "Main St 1", Town: "Testville"},
	})
}
