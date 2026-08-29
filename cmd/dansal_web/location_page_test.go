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

	// #1188: an explicit "no toilet" (false) renders its own warning badge,
	// distinct from a location that simply has no attributes recorded at all.
	render("open-air-no-toilet", LocationPageData{
		Location: Location{ID: 7, Location: "Elisenbrunnen", Town: "Aachen", Attributes: map[string]bool{"toilet": false}},
	})
	render("has-toilet", LocationPageData{
		Location: Location{ID: 8, Location: "Bürgerhaus Stollwerck", Attributes: map[string]bool{"toilet": true}},
	})
}

// TestLocationPageToiletBadge asserts the actual badge content (not just
// error-free rendering, unlike the smoke test above): explicit false shows
// the "no toilet" warning badge and not the "has toilet" one, and vice versa
// for explicit true (#1188).
func TestLocationPageToiletBadge(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/location/7", nil)

	render := func(attrs map[string]bool) string {
		rec := httptest.NewRecorder()
		data := LocationPageData{Location: Location{ID: 7, Location: "Elisenbrunnen", Attributes: attrs}}
		renderTemplate(rec, tmpls.location, tmplData(req, cfg, i18n, data.Location.Location, data))
		body, _ := io.ReadAll(rec.Body)
		return string(body)
	}

	t.Run("explicit no toilet", func(t *testing.T) {
		body := render(map[string]bool{"toilet": false})
		if !strings.Contains(body, "🚫🚻") {
			t.Error("expected the no-toilet warning badge (🚫🚻)")
		}
	})

	t.Run("explicit has toilet", func(t *testing.T) {
		body := render(map[string]bool{"toilet": true})
		if !strings.Contains(body, "🚻") {
			t.Error("expected the has-toilet badge (🚻)")
		}
		if strings.Contains(body, "🚫🚻") {
			t.Error("did not expect the no-toilet warning badge when toilet is explicitly true")
		}
	})

	t.Run("unset shows neither badge", func(t *testing.T) {
		body := render(nil)
		if strings.Contains(body, "🚻") {
			t.Error("did not expect any toilet badge when attribute is unset")
		}
	})
}
