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

// TestSuggestPhoneStepBar covers #1616: the new-suggestion page carries the
// six-step phone bar with its "edited" word, and the top arrows stay for
// wider screens. Every language has the two new strings.
func TestSuggestPhoneStepBar(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/events/suggest", nil)
	rec := httptest.NewRecorder()
	renderTemplate(rec, tmpls.suggestEvent, tmplData(req, cfg, i18n, "test", SuggestPageData{}))
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if strings.Contains(html, "template error") {
		t.Fatalf("template execution error: %s", html[max(0, len(html)-300):])
	}
	if !strings.Contains(html, `id="wiz-mtabs"`) {
		t.Fatal("suggest page has no phone step bar")
	}
	if n := strings.Count(html, `role="tab" class="mtab" data-step="`); n != 6 {
		t.Errorf("phone step bar has %d tabs, want 6", n)
	}
	if !strings.Contains(html, `id="wiz-arrows"`) {
		t.Error("top progress arrows must stay for wider screens")
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
		for _, key := range []string{"suggest_step_bar_label", "suggest_tab_edited"} {
			if section.Strings[key] == "" {
				t.Errorf("%s: missing %s", lang, key)
			}
		}
	}
}
