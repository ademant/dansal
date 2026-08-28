package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestSeedDefaultTagsFreshInstall verifies the embedded default vocabulary
// (#1173) lands correctly on a fresh table: the balfolk seed's home-group
// grouping (bal-folk+fest-noz -> "ball", the four workshop variants ->
// "workshop") and declaration-order sort_order (festival first, matching
// the map-marker priority comment in tags.yaml).
func TestSeedDefaultTagsFreshInstall(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	old := db
	db = conn
	defer func() { db = old }()

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}

	seedDefaultTags(db, "")

	rows := map[string]struct {
		emoji, homeGroup, color string
		sortOrder               int
	}{}
	r, err := db.Query("SELECT slug, emoji, home_group, color, sort_order FROM tags")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for r.Next() {
		var slug, emoji, hg, color string
		var so int
		if err := r.Scan(&slug, &emoji, &hg, &color, &so); err != nil {
			t.Fatal(err)
		}
		rows[slug] = struct {
			emoji, homeGroup, color string
			sortOrder               int
		}{emoji, hg, color, so}
	}

	if rows["bal-folk"].homeGroup != "ball" || rows["fest-noz"].homeGroup != "ball" {
		t.Fatalf("expected bal-folk and fest-noz to share home_group \"ball\", got %+v / %+v", rows["bal-folk"], rows["fest-noz"])
	}
	for _, slug := range []string{"workshop", "music-course", "dance-workshop", "musician-workshop"} {
		if rows[slug].homeGroup != "workshop" {
			t.Errorf("expected %s home_group=workshop, got %+v", slug, rows[slug])
		}
	}
	if rows["open-air"].emoji != "" || rows["open-air"].homeGroup != "" {
		t.Fatalf("expected open-air to have no home-page presence (no emoji/home_group), got %+v", rows["open-air"])
	}
	if rows["beginners"].emoji != "" {
		t.Fatalf("expected a level tag to have no emoji, got %+v", rows["beginners"])
	}
	if rows["festival"].sortOrder != 0 {
		t.Fatalf("expected festival to be first in declaration order (sort_order=0, giving it top map-marker priority), got %d", rows["festival"].sortOrder)
	}
	if rows["bal-folk"].sortOrder >= rows["workshop"].sortOrder {
		t.Fatalf("expected bal-folk to sort before workshop (ball beats workshop in marker priority), got bal-folk=%d workshop=%d",
			rows["bal-folk"].sortOrder, rows["workshop"].sortOrder)
	}
}

// TestSeedDefaultTagsBackfillsExistingRow simulates a tag row seeded before
// #1173 (name/category only, empty emoji/home_group/color/sort_order — the
// shape every existing dansal install's tags table was in) and verifies
// seedDefaultTags's ON CONFLICT DO UPDATE actually backfills it — the whole
// point of making this an upsert rather than INSERT OR IGNORE, which would
// silently skip a slug that already exists.
func TestSeedDefaultTagsBackfillsExistingRow(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	old := db
	db = conn
	defer func() { db = old }()

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	if _, err := db.Exec("INSERT INTO tags (slug, name, category) VALUES ('bal-folk', 'Ball', 'format')"); err != nil {
		t.Fatal(err)
	}

	seedDefaultTags(db, "")

	var emoji, homeGroup string
	if err := db.QueryRow("SELECT emoji, home_group FROM tags WHERE slug='bal-folk'").Scan(&emoji, &homeGroup); err != nil {
		t.Fatal(err)
	}
	if emoji == "" || homeGroup != "ball" {
		t.Fatalf("expected the pre-existing bal-folk row to be backfilled with emoji/home_group, got emoji=%q home_group=%q", emoji, homeGroup)
	}
}

// TestSeedDefaultTagsOverrideFile verifies a custom tags_file (#1173's whole
// point: a non-balfolk instance shipping its own vocabulary) is used
// instead of the embedded default, and that an invalid override file falls
// back to the embedded default rather than leaving the table unseeded.
func TestSeedDefaultTagsOverrideFile(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	old := db
	db = conn
	defer func() { db = old }()

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "tags.yaml")
	custom := `
tags:
  - slug: tango
    name: Tango
    category: format
    emoji: "💃"
    home_group: tango
    color: "#123456"
`
	if err := os.WriteFile(path, []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}

	seedDefaultTags(db, path)

	var n int
	db.QueryRow("SELECT COUNT(*) FROM tags WHERE slug='tango'").Scan(&n)
	if n != 1 {
		t.Fatal("expected the custom vocabulary's tango tag to be seeded")
	}
	db.QueryRow("SELECT COUNT(*) FROM tags WHERE slug='bal-folk'").Scan(&n)
	if n != 0 {
		t.Fatal("expected the embedded default vocabulary NOT to be seeded when an override file is set")
	}

	// A bogus override path should silently fall back to the embedded default.
	seedDefaultTags(db, filepath.Join(dir, "does-not-exist.yaml"))
	db.QueryRow("SELECT COUNT(*) FROM tags WHERE slug='bal-folk'").Scan(&n)
	if n != 1 {
		t.Fatal("expected an unreadable override path to fall back to the embedded default vocabulary")
	}
}

// TestSeedDefaultTagsRejectsInvalidSlug guards the HTML-attribute-name
// safety concern noted in tags_seed.go: a malformed home_group must not
// reach the tags table, since dansal_web turns it into a literal
// data-<home_group> attribute name.
func TestSeedDefaultTagsRejectsInvalidSlug(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	old := db
	db = conn
	defer func() { db = old }()

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "tags.yaml")
	bad := `
tags:
  - slug: "not a slug!"
    name: Bad
    category: format
    emoji: "x"
    home_group: "also bad!"
  - slug: unknown-category
    name: Bad Category
    category: nonsense
  - slug: good-tag
    name: Good
    category: format
`
	if err := os.WriteFile(path, []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}

	seedDefaultTags(db, path)

	var n int
	db.QueryRow("SELECT COUNT(*) FROM tags").Scan(&n)
	if n != 1 {
		t.Fatalf("expected only the one valid entry to be seeded, got %d rows", n)
	}
	db.QueryRow("SELECT COUNT(*) FROM tags WHERE slug='good-tag'").Scan(&n)
	if n != 1 {
		t.Fatal("expected the valid entry to still be seeded")
	}
}
