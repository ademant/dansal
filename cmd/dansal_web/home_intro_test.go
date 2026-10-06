package main

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ademant/dansal/internal/webcommon"
)

// TestLoadLangMapWithDefault covers #1298/#1461: a per-language DB override
// is merged on top of the shipped default rather than replacing it, so a
// language the admin never touches (or whose row is explicitly empty —
// webmin's save handler stores "" for a language that still matches its
// default, see siteConfigLegalTextSaveHandler) still resolves to working
// default text instead of going silently blank.
func TestLoadLangMapWithDefault(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	defaults := webcommon.ParseLangYAML(webcommon.DefaultHomeIntroYAML)

	t.Run("empty DB returns the full shipped default", func(t *testing.T) {
		m := loadLangMapWithDefault(db, "home_intro", defaults)
		if len(m) < 12 {
			t.Fatalf("expected at least 12 default languages, got %d: %v", len(m), m)
		}
		if !strings.Contains(m["en"], "%s") {
			t.Errorf("expected the English default to carry a %%s placeholder, got %q", m["en"])
		}
	})

	t.Run("a DB override for one language leaves every other language on the default", func(t *testing.T) {
		setSiteSetting(db, "home_intro_en", "Custom %s text.")
		m := loadLangMapWithDefault(db, "home_intro", defaults)
		if m["en"] != "Custom %s text." {
			t.Errorf("en = %q, want the override", m["en"])
		}
		if m["de"] != defaults["de"] {
			t.Errorf("de should be unchanged from the default, got %q", m["de"])
		}
		setSiteSetting(db, "home_intro_en", "")
	})

	t.Run("an explicitly empty row for a language does not blank the default", func(t *testing.T) {
		setSiteSetting(db, "home_intro_en", "")
		setSiteSetting(db, "home_intro_de", "Custom.")
		m := loadLangMapWithDefault(db, "home_intro", defaults)
		if m["en"] != defaults["en"] {
			t.Errorf("en with an empty row should keep the default, got %q", m["en"])
		}
		if m["de"] != "Custom." {
			t.Errorf("de = %q, want the override", m["de"])
		}
	})
}

// TestSiteSettingsCacheHomeIntro covers the cache's language-fallback
// accessor: an unconfigured language falls back to "de".
func TestSiteSettingsCacheHomeIntro(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	cache := newSiteSettingsCache(db)

	if got := cache.HomeIntro("en"); !strings.Contains(got, "%s") {
		t.Errorf("HomeIntro(en) = %q, want the shipped English default", got)
	}
	// "xx" isn't a real language in the default set — must fall back to "de".
	if got, want := cache.HomeIntro("xx"), cache.HomeIntro("de"); got != want {
		t.Errorf("HomeIntro(xx) = %q, want the de fallback %q", got, want)
	}
}

// TestSmokeRenderIndexHomeIntro covers the actual index.html render: the
// paragraph appears above the event list, with "%s" already filled in with
// the site name.
func TestSmokeRenderIndexHomeIntro(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	siteCfg = newSiteSettingsCache(db)

	tmpls := loadTemplates()
	i18n := loadI18n("")
	cfg := &Config{Domain: "example.test", SiteName: "Example Calendar"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Language", "en")

	rec := httptest.NewRecorder()
	homeIntro := ""
	if tpl := siteCfg.HomeIntro(i18n.detectLang(req)); tpl != "" {
		homeIntro = fmt.Sprintf(tpl, effectiveSiteName(cfg))
	}
	renderTemplate(rec, tmpls.index, tmplData(req, cfg, i18n, "Events", IndexData{HomeIntro: homeIntro}))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}
	// #1299: a real <details> disclosure, not a plain always-visible <p> —
	// collapsed by default (no "open" attribute) but still present in the
	// raw HTML for any crawler that doesn't run JS, with a translated
	// <summary> toggle label.
	if !strings.Contains(string(body), `<details class="home-intro">`) {
		t.Fatalf("expected a collapsible <details class=\"home-intro\">, body tail: %s", body[max(0, len(body)-800):])
	}
	if strings.Contains(string(body), `<details class="home-intro" open>`) {
		t.Fatalf("home-intro must be collapsed by default (no open attribute)")
	}
	if !strings.Contains(string(body), "<summary>About this site</summary>") {
		t.Fatalf("expected the translated summary toggle label, body tail: %s", body[max(0, len(body)-800):])
	}
	if !strings.Contains(string(body), "Example Calendar is the community calendar") {
		t.Fatalf("expected the site name to be filled into the English default, body tail: %s", body[max(0, len(body)-800):])
	}
	// #1299: the decorative map is hidden from the accessibility tree — the
	// same event data is already accessible as text further down the page.
	if !strings.Contains(string(body), `id="map-container" aria-hidden="true"`) {
		t.Fatalf("expected #map-container to be aria-hidden, body tail: %s", body[max(0, len(body)-800):])
	}
}
