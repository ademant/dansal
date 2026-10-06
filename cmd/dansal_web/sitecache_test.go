package main

import (
	"database/sql"
	"strings"
	"testing"
)

// TestParseSameAs covers #1296's site_settings parsing: one URL per line,
// blank lines dropped, and nil (not an empty slice) for an empty/blank
// setting so callers can tell "not configured" from "configured empty".
func TestParseSameAs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   \n\n  \t\n", nil},
		{"single URL", "https://github.com/example", []string{"https://github.com/example"}},
		{
			"multiple URLs with blank lines and surrounding whitespace",
			"https://github.com/example\n\n  https://mas.to/@example  \n\nhttps://www.wikidata.org/wiki/Q123\n",
			[]string{"https://github.com/example", "https://mas.to/@example", "https://www.wikidata.org/wiki/Q123"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseSameAs(c.raw)
			if len(got) != len(c.want) {
				t.Fatalf("parseSameAs(%q) = %v, want %v", c.raw, got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("parseSameAs(%q)[%d] = %q, want %q", c.raw, i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestSiteSettingsCacheDescBuckets covers #1290's three default-description
// site settings: each returns working default English text out of the box,
// each bucket is independently overridable, and an unset one still falls
// back to "de" for an unknown language (same convention as HomeIntro).
func TestSiteSettingsCacheDescBuckets(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	cache := newSiteSettingsCache(db)

	if got := cache.DescBall("en"); !strings.Contains(got, "bal-folk") {
		t.Errorf("DescBall(en) = %q, want the shipped default mentioning bal-folk", got)
	}
	if got := cache.DescWorkshop("en"); !strings.Contains(got, "workshop") {
		t.Errorf("DescWorkshop(en) = %q, want the shipped default mentioning workshop", got)
	}
	if got := cache.DescFestival("en"); !strings.Contains(got, "festival") {
		t.Errorf("DescFestival(en) = %q, want the shipped default mentioning festival", got)
	}

	setSiteSetting(db, "default_desc_ball_en", "Custom ball description.")
	cache = newSiteSettingsCache(db)
	if got := cache.DescBall("en"); got != "Custom ball description." {
		t.Errorf("DescBall(en) after override = %q, want the override", got)
	}
	// Workshop/festival are untouched by the ball-only override.
	if got := cache.DescWorkshop("en"); !strings.Contains(got, "workshop") {
		t.Errorf("DescWorkshop(en) should be unaffected by a default_desc_ball override, got %q", got)
	}

	if got, want := cache.DescBall("xx"), cache.DescBall("de"); got != want {
		t.Errorf("DescBall(xx) = %q, want the de fallback %q", got, want)
	}
}

func TestSiteSettingsCacheLegalPages(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	setSiteSetting(db, "privacy_en", "# Privacy")
	setSiteSetting(db, "privacy_uk", "# Конфіденційність")
	setSiteSetting(db, "terms_fr", "# Conditions")

	cache := newSiteSettingsCache(db)
	if got := cache.LegalPageText("privacy", "en"); got != "# Privacy" {
		t.Errorf("privacy en = %q, want configured text", got)
	}
	if got := cache.LegalPageText("privacy", "uk"); got != "# Конфіденційність" {
		t.Errorf("privacy uk = %q, want configured Ukrainian text", got)
	}
	if got := cache.LegalPageText("privacy", "fr"); got != "# Privacy" {
		t.Errorf("privacy fr fallback = %q, want English text", got)
	}
	if got := cache.LegalPageText("terms", "fr"); got != "# Conditions" {
		t.Errorf("terms fr = %q, want configured text", got)
	}
	if !cache.HasLegalPage("privacy") || !cache.HasLegalPage("terms") {
		t.Error("configured legal pages should be reported as available")
	}
	if cache.HasLegalPage("impressum") || cache.LegalPageText("unknown", "en") != "" {
		t.Error("unconfigured page should not be available or return text")
	}
}

func TestSiteSettingsCacheImpressumLanguageFallback(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	setSiteSetting(db, "impressum_en", "English notice")
	setSiteSetting(db, "impressum_de", "German notice")
	cache := newSiteSettingsCache(db)
	if got := cache.ImpressumText("fr"); got != "English notice" {
		t.Errorf("ImpressumText(fr) = %q, want English fallback", got)
	}

	setSiteSetting(db, "impressum_fr", "Avis français")
	cache = newSiteSettingsCache(db)
	if got := cache.ImpressumText("fr"); got != "Avis français" {
		t.Errorf("ImpressumText(fr) = %q, want selected-language text", got)
	}
}
