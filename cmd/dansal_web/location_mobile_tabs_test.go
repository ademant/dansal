package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLocationPhoneTabs covers #1671: the location page renders the phone tab
// bar with Info, Calendar, Events, and only the Map and Rooms tabs that have
// content. A tab with no content is marked empty.
func TestLocationPhoneTabs(t *testing.T) {
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

	render := func(data LocationPageData) string {
		rec := httptest.NewRecorder()
		renderTemplate(rec, tmpls.location, tmplData(req, cfg, i18n, data.Location.Location, data))
		body, _ := io.ReadAll(rec.Body)
		html := string(body)
		if strings.Contains(html, "template error") {
			t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
		}
		return html
	}

	lat, lon := 50.9, 6.9
	t.Run("bare location", func(t *testing.T) {
		html := render(LocationPageData{Location: Location{ID: 4, Location: "Bürgerhaus"}})
		for _, want := range []string{
			`id="loc-tabs" data-mtab="info"`,
			`data-tab="info" aria-controls="loc-tabs" aria-selected="true"`,
			`class="mtab is-empty" data-tab="calendar"`,
			`class="mtab is-empty" data-tab="events"`,
		} {
			if !strings.Contains(html, want) {
				t.Errorf("location page missing %s", want)
			}
		}
		for _, absent := range []string{`data-tab="map" aria-controls`, `data-tab="rooms" aria-controls`} {
			if strings.Contains(html, absent) {
				t.Errorf("location page without coordinates or rooms must not show %s", absent)
			}
		}
	})

	t.Run("building with map, rooms and events", func(t *testing.T) {
		html := render(LocationPageData{
			Location: Location{ID: 4, Location: "Bürgerhaus", Latitude: &lat, Longitude: &lon, Children: []Location{{ID: 56, Location: "Room B"}}},
			Events:   []Event{{ID: 1, Title: "Ball"}},
		})
		for _, want := range []string{
			`class="mtab" data-tab="calendar"`,
			`class="mtab" data-tab="events"`,
			`class="mtab" data-tab="map"`,
			`class="mtab" data-tab="rooms"`,
			`<div class="mtab-panel" data-tab="map">`,
			`<div class="mtab-panel" data-tab="rooms">`,
			`<div class="mtab-panel" data-tab="calendar">`,
			`<div class="mtab-phone-only">`,
			`<p class="loc-future-count">`,
			`dansalMtabs(root`,
		} {
			if !strings.Contains(html, want) {
				t.Errorf("location page missing %s", want)
			}
		}
	})
}

// TestLocationRoomsPanelOnlyWithRooms covers #1690: the rooms panel renders only
// when its rooms tab does, so the tab script never meets a panel without a button.
func TestLocationRoomsPanelOnlyWithRooms(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/location/788", nil)
	rec := httptest.NewRecorder()
	data := LocationPageData{Location: Location{ID: 788, Location: "Saal ohne Räume"}}
	renderTemplate(rec, tmpls.location, tmplData(req, cfg, i18n, data.Location.Location, data))
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if strings.Contains(html, `data-tab="rooms" aria-controls`) || strings.Contains(html, `<div class="mtab-panel" data-tab="rooms">`) {
		t.Error("a location without rooms or a site plan must not render a rooms tab or panel")
	}
}
