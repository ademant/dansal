package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSmokeRenderBoardPage renders board.html both with and without geocoded
// posts, to catch template balance / boardPostsGeoJSON regressions (#1078).
func TestSmokeRenderBoardPage(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/board", nil)

	render := func(name string, data BoardData) {
		t.Run(name, func(t *testing.T) {
			td := tmplData(req, cfg, i18n, "Board", data)
			rec := httptest.NewRecorder()
			renderTemplate(rec, tmpls.board, td)
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

	render("no-posts", BoardData{})

	lat, lon := 50.9375, 6.9603
	render("geocoded-posts", BoardData{
		Posts: []ContactPost{
			{
				ID: 1, EventID: 5, Type: "ride_offer", City: "Köln", Lat: &lat, Lon: &lon,
				Persons: 2, Nickname: "Alex", Event: &ContactPostEvent{ID: 5, Title: "Bal Folk Köln"},
			},
			{
				ID: 2, EventID: 5, Type: "ticket_offer", Persons: 1, Nickname: "Sam",
				Event: &ContactPostEvent{ID: 5, Title: "Bal Folk Köln"},
			},
		},
	})
}
