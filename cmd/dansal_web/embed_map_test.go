package main

import (
	"database/sql"
	"encoding/json"
	"html/template"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSmokeRenderEmbedCalendarAndLocationsMaps covers embed_calendar.html and
// embed_locations.html (#1288): both call L.tileLayer directly instead of
// attachTileLayer(map), which skipped fixDefaultMarkerIcon() and let default
// Leaflet markers race dansalLeafletCss() into rendering as a broken-image
// glyph. Renders each with real marker data and syntax-checks every inline
// <script> (node --check), and asserts each template still defines its own
// fixDefaultMarkerIcon() so a future edit can't silently drop the fix again.
func TestSmokeRenderEmbedCalendarAndLocationsMaps(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	tmpls := loadTemplates()
	i18n := loadI18n("")
	strs := i18n.Strings("en")

	t.Run("embed_calendar", func(t *testing.T) {
		calJSON, _ := json.Marshal([]calEvent{{ID: 1, Title: "Test Ball", Start: "2026-07-01T20:00:00", Lat: 48.1, Lng: 11.5}})
		rec := httptest.NewRecorder()
		renderEmbed(rec, tmpls.embedCalendar, map[string]any{
			"Lang": "en", "Nonce": "test-nonce", "Strings": strs,
			"BaseURL": "https://example.test", "SiteName": "dansal",
			"CalData": template.JS(calJSON), "TileToken": "test-tile-token",
			"From": "2026-01-01", "To": "2026-12-31",
		})
		body, _ := io.ReadAll(rec.Body)
		if strings.Contains(string(body), "template error") {
			t.Fatalf("template execution error, body: %s", body)
		}
		if !strings.Contains(string(body), "</html>") {
			t.Fatalf("truncated render (no closing </html>), body: %s", body)
		}
		if !strings.Contains(string(body), "function fixDefaultMarkerIcon()") {
			t.Fatal("expected fixDefaultMarkerIcon() to still be defined (#1288)")
		}
		checkInlineJS(t, string(body))
	})

	t.Run("embed_locations", func(t *testing.T) {
		locJSON, _ := json.Marshal([]locMarker{{ID: 1, Name: "Test Hall", Lat: 48.1, Lng: 11.5, Future: 3}})
		rec := httptest.NewRecorder()
		renderEmbed(rec, tmpls.embedLocations, map[string]any{
			"Lang": "en", "Nonce": "test-nonce", "Strings": strs,
			"BaseURL": "https://example.test", "SiteName": "dansal",
			"LocData": template.JS(locJSON), "TileToken": "test-tile-token",
		})
		body, _ := io.ReadAll(rec.Body)
		if strings.Contains(string(body), "template error") {
			t.Fatalf("template execution error, body: %s", body)
		}
		if !strings.Contains(string(body), "</html>") {
			t.Fatalf("truncated render (no closing </html>), body: %s", body)
		}
		if !strings.Contains(string(body), "function fixDefaultMarkerIcon()") {
			t.Fatal("expected fixDefaultMarkerIcon() to still be defined (#1288)")
		}
		checkInlineJS(t, string(body))
	})
}
