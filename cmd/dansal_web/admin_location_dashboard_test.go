package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSmokeRenderAdminLocationDashboard(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/admin/location/4", nil)
	req = withSessionUser(req, &SessionUser{ID: 1, Role: "admin"})

	render := func(name string, data AdminLocationDashboardData) {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			renderTemplate(rec, tmpls.adminLocationDashboard, tmplData(req, cfg, i18n, "test", data))
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

	render("no-rooms", AdminLocationDashboardData{
		Location: Location{ID: 4, Location: "Bürgerhaus Stollwerck"},
	})

	render("with-rooms", AdminLocationDashboardData{
		Location: Location{ID: 4, Location: "Bürgerhaus Stollwerck", Children: []Location{
			{ID: 55, Location: "Room A"}, {ID: 56, Location: "Room B"},
		}},
		RoomEventCounts: map[int]int{55: 3},
	})

	render("filtered-to-room", AdminLocationDashboardData{
		Location: Location{ID: 4, Location: "Bürgerhaus Stollwerck", Children: []Location{
			{ID: 55, Location: "Room A"}, {ID: 56, Location: "Room B"},
		}},
		RoomEventCounts: map[int]int{55: 3},
		FilterRoomID:    55,
	})

	buildingID := 4
	render("room-view-with-parent", AdminLocationDashboardData{
		Location: Location{ID: 55, Location: "Room A", ParentID: &buildingID},
		Parent:   &Location{ID: 4, Location: "Bürgerhaus Stollwerck"},
	})

	render("room-view-without-loaded-parent", AdminLocationDashboardData{
		Location: Location{ID: 55, Location: "Room A", ParentID: &buildingID},
	})
}
