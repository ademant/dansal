package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSmokeRenderAdminLocationEdit(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/admin/locations/1/edit", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	render := func(name string, data AdminLocationEditData) {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			renderTemplate(rec, tmpls.adminLocationEdit, tmplData(req, cfg, i18n, "test", data))
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
			// Syntax-only (node --check): catches a template interpolation
			// producing invalid JS, not a runtime ReferenceError like
			// #1283's "L is not defined" (leaflet.js loading with `defer`,
			// L.map(...) called before it's ready) — that class of bug needs
			// an actual browser executing the script, which is what the
			// "with-coords" case below exists to make it possible to add
			// later (a real e2e assertion that #map actually renders a
			// Leaflet map, not just that the page contains valid JS).
			checkInlineJS(t, string(body))
		})
	}

	render("new-blank", AdminLocationEditData{})

	// #1283: the reported-live regression — a location that already has
	// saved coordinates hits the hasCoords branch in the inline script,
	// which calls L.map(...). No case here exercised that branch before.
	lat, lon := 50.9375, 6.9603
	render("with-coords", AdminLocationEditData{
		Location: Location{ID: 260, Location: "Bürgerhaus Stollwerck", ShortName: "Stollwerck",
			Latitude: &lat, Longitude: &lon},
	})

	render("building", AdminLocationEditData{
		Location: Location{ID: 4, Location: "Bürgerhaus Stollwerck", Address: "Dillenburger Str.", Town: "Köln",
			Children: []Location{{ID: 55, Location: "Room A"}, {ID: 56, Location: "Room B"}}},
	})

	buildingID := 4
	render("room-with-parent", AdminLocationEditData{
		Location: Location{ID: 55, Location: "Room A", ParentID: &buildingID, Address: "Dillenburger Str.", Town: "Köln"},
		Parent:   &Location{ID: 4, Location: "Bürgerhaus Stollwerck"},
	})

	render("room-without-loaded-parent", AdminLocationEditData{
		Location: Location{ID: 55, Location: "Room A", ParentID: &buildingID},
	})

	x, y := 0.42, 0.61
	render("building-with-siteplan", AdminLocationEditData{
		Location: Location{ID: 4, Location: "Bürgerhaus Stollwerck", SitePlanURL: "/api/v1/location-images/4", Children: []Location{
			{ID: 55, Location: "Room A", PlanX: &x, PlanY: &y},
			{ID: 56, Location: "Room B"},
		}},
	})

	// #1188: tri-state attributes -- an explicit "no" (false) must render
	// distinctly from an unset key, for every attribute including the new
	// "toilet" one, without a template error.
	render("tristate-attributes", AdminLocationEditData{
		Location: Location{ID: 7, Location: "Elisenbrunnen (open air)", Attributes: map[string]bool{
			"wheelchair": true,
			"toilet":     false,
			"bar":        false,
		}},
	})
}
