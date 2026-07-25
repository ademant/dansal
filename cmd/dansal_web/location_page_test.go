package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSmokeRenderLocationPage(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/location/4", nil)

	render := func(name string, data LocationPageData) {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			renderTemplate(rec, tmpls.location, tmplData(req, cfg, i18n, data.Location.Location, data))
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

	render("plain-location", LocationPageData{
		Location: Location{ID: 4, Location: "Bürgerhaus Stollwerck", Address: "Dillenburger Str.", Town: "Köln"},
	})

	x, y := 0.42, 0.61
	render("building-with-siteplan-and-rooms", LocationPageData{
		Location: Location{ID: 4, Location: "Bürgerhaus Stollwerck", SitePlanURL: "/api/v1/location-images/4", Children: []Location{
			{ID: 55, Location: "Room A", PlanX: &x, PlanY: &y, FloorCondition: "parquet"},
			{ID: 56, Location: "Room B"},
		}},
	})

	buildingID := 4
	render("room-page", LocationPageData{
		Location: Location{ID: 55, Location: "Room A", ParentID: &buildingID},
	})
}
