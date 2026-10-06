package webcommon

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestParseLangYAML(t *testing.T) {
	m := ParseLangYAML(`en: "Hello %s"
de: "Hallo %s"
`)
	if m["en"] != "Hello %s" || m["de"] != "Hallo %s" {
		t.Fatalf("ParseLangYAML = %+v", m)
	}

	// Malformed input must not panic — logged and an empty map returned.
	m = ParseLangYAML("not: [valid: yaml")
	if len(m) != 0 {
		t.Errorf("ParseLangYAML(malformed) = %+v, want empty map", m)
	}
}

// TestMigrateLegacyLangBlob covers #1461's one-time backfill from the
// pre-#1461 single-blob site_settings row into one row per language.
func TestMigrateLegacyLangBlob(t *testing.T) {
	db := openTestDB(t)
	langs := []string{"de", "en", "fr"}

	t.Run("no legacy blob is a no-op", func(t *testing.T) {
		MigrateLegacyLangBlob(db, "home_intro", langs)
		for _, lang := range langs {
			if v := GetSiteSetting(db, "home_intro_"+lang); v != "" {
				t.Errorf("home_intro_%s = %q, want empty (nothing to migrate)", lang, v)
			}
		}
	})

	t.Run("a populated blob splits into per-language rows and the blob is cleared", func(t *testing.T) {
		SetSiteSetting(db, "default_desc_ball", `en: "English text."
de: "Deutscher Text."
`)
		MigrateLegacyLangBlob(db, "default_desc_ball", langs)

		if got := GetSiteSetting(db, "default_desc_ball_en"); got != "English text." {
			t.Errorf("default_desc_ball_en = %q, want migrated English text", got)
		}
		if got := GetSiteSetting(db, "default_desc_ball_de"); got != "Deutscher Text." {
			t.Errorf("default_desc_ball_de = %q, want migrated German text", got)
		}
		if got := GetSiteSetting(db, "default_desc_ball_fr"); got != "" {
			t.Errorf("default_desc_ball_fr = %q, want empty (not present in the blob)", got)
		}
		if got := GetSiteSetting(db, "default_desc_ball"); got != "" {
			t.Errorf("default_desc_ball blob = %q, want cleared after migration", got)
		}
	})

	t.Run("idempotent: a second call is a no-op since the blob is already cleared", func(t *testing.T) {
		SetSiteSetting(db, "default_desc_ball_en", "Edited after migration.")
		MigrateLegacyLangBlob(db, "default_desc_ball", langs)
		if got := GetSiteSetting(db, "default_desc_ball_en"); got != "Edited after migration." {
			t.Errorf("default_desc_ball_en = %q, a second migrate call must not overwrite a post-migration edit", got)
		}
	})
}
