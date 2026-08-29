package main

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TestCategoryFilterColumnSafetyNet verifies fetch_sources.category_filter
// exists on a fresh install (createTables) and is backfilled by migrateDB's
// safety net even when v32 was already marked applied on an older DB shape
// (e.g. restored from a pre-#1187 backup with schema_migrations intact but
// the column missing).
func TestCategoryFilterColumnSafetyNet(t *testing.T) {
	old := db
	defer func() { db = old }()

	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db = conn

	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	migrateDB()

	hasColumn := func() bool {
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('fetch_sources') WHERE name='category_filter'").Scan(&n)
		return n > 0
	}
	if !hasColumn() {
		t.Fatal("category_filter column missing after createTables+migrateDB")
	}

	// Simulate an older DB shape: drop the column, but leave v32 marked
	// applied (schema_migrations survives a restore even if the ALTER TABLE
	// itself didn't, e.g. a hand-edited backup).
	if _, err := db.Exec("ALTER TABLE fetch_sources DROP COLUMN category_filter"); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if hasColumn() {
		t.Fatal("test setup failed: column still present after DROP COLUMN")
	}
	migrateDB()
	if !hasColumn() {
		t.Fatal("safety net did not restore category_filter column on re-migration")
	}
}

// TestEventCategoriesMatchFilter exercises the pure matching helper: empty
// filter always matches (#1187's default, unchanged import-everything
// behavior); a non-empty filter requires a case-insensitive intersection.
func TestEventCategoriesMatchFilter(t *testing.T) {
	cases := []struct {
		name   string
		events []string
		filter []string
		want   bool
	}{
		{"empty filter matches everything", []string{"Sonstiges"}, nil, true},
		{"empty filter matches even with no event categories", nil, nil, true},
		{"exact match", []string{"Balfolk"}, []string{"Balfolk"}, true},
		{"case-insensitive match", []string{"balfolk"}, []string{"Balfolk"}, true},
		{"one of several event categories matches", []string{"Feminismus | Gender | Queer", "Balfolk"}, []string{"Balfolk"}, true},
		{"one of several filter values matches", []string{"Tanz"}, []string{"Balfolk", "Tanz"}, true},
		{"no overlap", []string{"Feminismus | Gender | Queer"}, []string{"Balfolk"}, false},
		{"no event categories, non-empty filter", nil, []string{"Balfolk"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := eventCategoriesMatchFilter(c.events, c.filter)
			if got != c.want {
				t.Errorf("eventCategoriesMatchFilter(%v, %v) = %v, want %v", c.events, c.filter, got, c.want)
			}
		})
	}
}

// icsWithTwoCategorizedEvents builds a minimal, valid iCal feed with two
// future VEVENTs distinguished only by their CATEGORIES, mirroring the real
// bewegungsmelder-aachen.de shape that motivated #1187 (a general activist
// calendar where only some events are Balfolk).
func icsWithTwoCategorizedEvents() []byte {
	start := time.Now().AddDate(0, 0, 14).UTC().Format("20060102T150405Z")
	end := time.Now().AddDate(0, 0, 14).Add(2 * time.Hour).UTC().Format("20060102T150405Z")
	return []byte(fmt.Sprintf(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:balfolk-event@example.test
DTSTART:%s
DTEND:%s
SUMMARY:Balfolk - Folktanz am Elisenbrunnen
CATEGORIES:Feminismus | Gender | Queer,Balfolk
END:VEVENT
BEGIN:VEVENT
UID:stammtisch-event@example.test
DTSTART:%s
DTEND:%s
SUMMARY:Feministischer Stammtisch
CATEGORIES:Feminismus | Gender | Queer
END:VEVENT
END:VCALENDAR
`, start, end, start, end))
}

// TestParseICalBodyCategoryFilter verifies that a FetchSource.CategoryFilter
// skips non-matching VEVENTs entirely (#1187), while an empty filter keeps
// today's import-everything behavior.
func TestParseICalBodyCategoryFilter(t *testing.T) {
	body := icsWithTwoCategorizedEvents()

	t.Run("no filter imports both events", func(t *testing.T) {
		src := FetchSource{Type: "ical"}
		entries, err := parseICalBody(body, src)
		if err != nil {
			t.Fatalf("parseICalBody: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("got %d entries, want 2", len(entries))
		}
	})

	t.Run("Balfolk filter keeps only the matching event", func(t *testing.T) {
		src := FetchSource{Type: "ical", CategoryFilter: []string{"Balfolk"}}
		entries, err := parseICalBody(body, src)
		if err != nil {
			t.Fatalf("parseICalBody: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("got %d entries, want 1", len(entries))
		}
		if entries[0].req.Title != "Balfolk - Folktanz am Elisenbrunnen" {
			t.Errorf("unexpected event survived filter: %q", entries[0].req.Title)
		}
	})

	t.Run("non-matching filter keeps nothing", func(t *testing.T) {
		src := FetchSource{Type: "ical", CategoryFilter: []string{"Musik"}}
		entries, err := parseICalBody(body, src)
		if err != nil {
			t.Fatalf("parseICalBody: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("got %d entries, want 0", len(entries))
		}
	})
}
