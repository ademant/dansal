package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
)

// TestIndexPhoneTabs covers #1613: the homepage renders the three phone tabs
// (list first), the panels they switch, and no longer loads Leaflet with
// render-blocking script tags.
func TestIndexPhoneTabs(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.index, tmplData(req, cfg, i18n, "Events", IndexData{}))
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if strings.Contains(html, "template error") {
		t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
	}

	for _, want := range []string{
		`id="home-tabs" data-mtab="map"`,
		`role="tab" class="mtab" id="mtab-list" data-tab="list" aria-controls="mpanel-list" aria-selected="false"`,
		`id="mtab-week" data-tab="week" aria-controls="mpanel-week" aria-selected="false"`,
		`id="mtab-map" data-tab="map" aria-controls="mpanel-map" aria-selected="true"`,
		`id="mpanel-list" data-tab="list" role="tabpanel"`,
		`id="mpanel-week" data-tab="week" role="tabpanel"`,
		`id="mpanel-map" data-tab="map" role="tabpanel"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index page missing %s", want)
		}
	}
	for _, gone := range []string{"mobile-show-all", "mobile-mode-bar", "leaflet.markercluster.js", "/static/leaflet/leaflet.js"} {
		if strings.Contains(html, gone) {
			t.Errorf("index page still contains %q", gone)
		}
	}

	var cfgYAML struct {
		Languages map[string]struct {
			Strings map[string]string `yaml:"strings"`
		} `yaml:"languages"`
	}
	raw, err := os.ReadFile("i18n.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &cfgYAML); err != nil {
		t.Fatal(err)
	}
	for lang, section := range cfgYAML.Languages {
		for _, key := range []string{"mtab_label", "mtab_list", "mtab_calendar", "mtab_map"} {
			if section.Strings[key] == "" {
				t.Errorf("%s: missing %s", lang, key)
			}
		}
	}
}
