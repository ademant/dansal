package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// #1588: the cities page order, day counts, ItemList, the city meta description,
// the banner fallback, and a render of the page.

var testNow = time.Date(2026, 10, 23, 10, 0, 0, 0, time.Local)

func TestCityTilesOrder(t *testing.T) {
	cities := []City{
		{Town: "Bonn", LocationCount: 2},
		{Town: "Köln", EventCount: 3, NextEventStart: "2026-10-26T19:00:00+02:00"},
		{Town: "Berlin", EventCount: 1, NextEventStart: "2026-10-24T20:00:00+02:00"},
		{Town: "Aachen"},
	}
	tiles := cityTiles(cities, testNow)
	var got []string
	for _, tl := range tiles {
		got = append(got, tl.Town)
	}
	want := []string{"Berlin", "Köln", "Aachen", "Bonn"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if tiles[0].NextDays != 1 || tiles[1].NextDays != 3 {
		t.Errorf("NextDays = %d,%d, want 1,3", tiles[0].NextDays, tiles[1].NextDays)
	}
	if tiles[2].HasNext || tiles[3].HasNext {
		t.Error("cities without a future event must not report a next event")
	}
}

func TestCalendarDaysBetween(t *testing.T) {
	for _, tc := range []struct {
		at   string
		want int
	}{
		{"2026-10-23T20:00:00+02:00", 0},
		{"2026-10-24T08:00:00+02:00", 1},
		{"2026-10-26T19:00:00+02:00", 3},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		if got := calendarDaysBetween(testNow, at); got != tc.want {
			t.Errorf("days to %s = %d, want %d", tc.at, got, tc.want)
		}
	}
}

func TestItemListJSON(t *testing.T) {
	if got := itemListJSON(nil); got != "" {
		t.Errorf("empty list rendered %q, want nothing", got)
	}
	out := string(itemListJSON([]listItem{{Name: "Bonn", URL: "https://example.test/city/bonn"}}))
	var doc struct {
		Type     string `json:"@type"`
		Elements []struct {
			Position int    `json:"position"`
			Name     string `json:"name"`
			URL      string `json:"url"`
		} `json:"itemListElement"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("ItemList is not valid JSON: %v", err)
	}
	if doc.Type != "ItemList" || len(doc.Elements) != 1 || doc.Elements[0].Position != 1 || doc.Elements[0].URL != "https://example.test/city/bonn" {
		t.Errorf("ItemList = %s", out)
	}
}

func TestCityMetaDescription(t *testing.T) {
	i18n := loadI18n("")
	got := i18n.Strings("de").TF("city_meta_desc", "Bonn", 3, 5)
	want := "Balfolk in Bonn: 3 Organisationen und 5 Veranstaltungsorte"
	if got != want {
		t.Errorf("city_meta_desc = %q, want %q", got, want)
	}
}

func TestCityBannerFallsBackToSiteBanner(t *testing.T) {
	two := 2
	for _, c := range []City{
		{Town: "Köln", OrgCount: 0},
		{Town: "Bonn", OrgCount: 2, SingleOrgID: &two},
	} {
		// client is not used on these paths, so nil is safe.
		if got := cityBanner(context.Background(), nil, c); got != "/banner.avif" {
			t.Errorf("%s: banner = %q, want the site banner", c.Town, got)
		}
	}
}

func TestCitiesPageRenders(t *testing.T) {
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
	cities := []City{
		{Town: "Köln", EventCount: 3, NextEventStart: "2026-10-26T19:00:00+02:00"},
		{Town: "Bonn"},
	}
	tiles := cityTiles(cities, testNow)
	items := []listItem{{Name: "Köln", URL: "https://example.test/city/koeln"}}

	req := httptest.NewRequest(http.MethodGet, "/cities", nil)
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.cities, tmplData(req, cfg, i18n, "Städte", CitiesData{
		Tiles:        tiles,
		MapJSON:      template.JS("[]"),
		ItemListJSON: itemListJSON(items),
	}))
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if strings.Contains(html, "template error") {
		t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
	}
	for _, want := range []string{
		`data-cities-sort="alpha"`,
		`class="city-card city-card-empty" data-name="Bonn"`,
		`"@type":"ItemList"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("cities page missing %s", want)
		}
	}
	if strings.Contains(html, "Bonn") && strings.Index(html, `data-name="Köln"`) > strings.Index(html, `data-name="Bonn"`) {
		t.Error("the city with a future event must come before the one without")
	}
}
