package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetectLangHolidayCountryFallback(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)

	i18n := loadI18n("")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No cookie, no Accept-Language header.

	origSiteCfg := siteCfg
	defer func() { siteCfg = origSiteCfg }()

	t.Run("no site config falls back to default", func(t *testing.T) {
		siteCfg = nil
		if got := i18n.detectLang(req); got != i18n.DefaultLang {
			t.Fatalf("got %q, want default %q", got, i18n.DefaultLang)
		}
	})

	t.Run("unambiguous country wins over default", func(t *testing.T) {
		db.Exec(`INSERT OR REPLACE INTO site_settings (key, value) VALUES ('holiday_country', 'FR')`)
		siteCfg = newSiteSettingsCache(db)
		if got := i18n.detectLang(req); got != "fr" {
			t.Fatalf("got %q, want fr", got)
		}
	})

	t.Run("ambiguous country falls back to default", func(t *testing.T) {
		db.Exec(`INSERT OR REPLACE INTO site_settings (key, value) VALUES ('holiday_country', 'CH')`)
		siteCfg = newSiteSettingsCache(db)
		if got := i18n.detectLang(req); got != i18n.DefaultLang {
			t.Fatalf("got %q, want default %q", got, i18n.DefaultLang)
		}
	})

	t.Run("cookie takes priority over holiday_country", func(t *testing.T) {
		db.Exec(`INSERT OR REPLACE INTO site_settings (key, value) VALUES ('holiday_country', 'FR')`)
		siteCfg = newSiteSettingsCache(db)
		req2 := httptest.NewRequest(http.MethodGet, "/", nil)
		req2.AddCookie(&http.Cookie{Name: cookieLang, Value: "es"})
		if got := i18n.detectLang(req2); got != "es" {
			t.Fatalf("got %q, want es", got)
		}
	})

	t.Run("Accept-Language takes priority over holiday_country", func(t *testing.T) {
		db.Exec(`INSERT OR REPLACE INTO site_settings (key, value) VALUES ('holiday_country', 'FR')`)
		siteCfg = newSiteSettingsCache(db)
		req2 := httptest.NewRequest(http.MethodGet, "/", nil)
		req2.Header.Set("Accept-Language", "it;q=0.9")
		if got := i18n.detectLang(req2); got != "it" {
			t.Fatalf("got %q, want it", got)
		}
	})
}

func TestDetectLangQueryParam(t *testing.T) {
	i18n := loadI18n("")

	t.Run("valid lang param wins over everything else", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?lang=es", nil)
		req.AddCookie(&http.Cookie{Name: cookieLang, Value: "fr"})
		req.Header.Set("Accept-Language", "it;q=0.9")
		if got := i18n.detectLang(req); got != "es" {
			t.Fatalf("got %q, want es", got)
		}
	})

	t.Run("unknown lang param falls through to cookie", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?lang=xx", nil)
		req.AddCookie(&http.Cookie{Name: cookieLang, Value: "fr"})
		if got := i18n.detectLang(req); got != "fr" {
			t.Fatalf("got %q, want fr", got)
		}
	})

	t.Run("empty lang param falls through to cookie", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?lang=", nil)
		req.AddCookie(&http.Cookie{Name: cookieLang, Value: "fr"})
		if got := i18n.detectLang(req); got != "fr" {
			t.Fatalf("got %q, want fr", got)
		}
	})
}

func TestHolidayCountryLang(t *testing.T) {
	cases := map[string]struct {
		lang string
		ok   bool
	}{
		"DE": {"de", true},
		"at": {"de", true},
		"FR": {"fr", true},
		"NL": {"nl", true},
		"GB": {"en", true},
		"CH": {"", false},
		"BE": {"", false},
		"LU": {"", false},
		"":   {"", false},
		"XX": {"", false},
	}
	for country, want := range cases {
		lang, ok := holidayCountryLang(country)
		if lang != want.lang || ok != want.ok {
			t.Errorf("holidayCountryLang(%q) = (%q, %v), want (%q, %v)", country, lang, ok, want.lang, want.ok)
		}
	}
}

// TestFetchRunTimezoneFallbackTranslationNotDrinkRelated is a narrow
// regression guard for a specific copy-paste mistranslation: the German and
// Breton values for fetch_run_timezone_fallback ("time taken from the feed's
// VTIMEZONE") read "Getränkezeit"/"Koudiù" — both drink-related, apparently
// copy-pasted from the neighbouring drink_alcohol/drink_soft keys — instead
// of anything about a timezone. All other 10 languages translated it
// correctly. Semantic correctness can't be checked automatically in
// general, but this exact regression can be.
func TestFetchRunTimezoneFallbackTranslationNotDrinkRelated(t *testing.T) {
	i := loadI18n("")
	for _, lang := range []string{"de", "br"} {
		s, ok := i.langs[lang]
		if !ok {
			t.Fatalf("language %q not found", lang)
		}
		got := s.Strings["fetch_run_timezone_fallback"]
		if got == "" {
			t.Fatalf("%s: fetch_run_timezone_fallback is empty", lang)
		}
		for _, bad := range []string{"Getränk", "Koudiù"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s: fetch_run_timezone_fallback = %q, still contains drink-related %q", lang, got, bad)
			}
		}
	}
}
