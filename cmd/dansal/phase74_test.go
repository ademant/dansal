package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// #1825: a notification with no config (tests, partial startup) must not
// panic in the async path; with no config there is no SMTP to use.
func TestNotifyUserWithoutConfigDoesNotPanic(t *testing.T) {
	oldCfg := config
	config = nil
	defer func() { config = oldCfg }()
	notifyUser("", "", false, "someone@example.test", "subject", "body")
}

// #1826: a legacy locations table without organization_id, with the old
// aliases JSON column, must migrate instead of failing on the missing column.
func TestAliasMigrationAcceptsLegacyLocationsWithoutOrganizationID(t *testing.T) {
	old := db
	conn, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db = conn
	defer func() { db = old }()

	if _, err := db.Exec(`CREATE TABLE locations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		location TEXT NOT NULL,
		short_name TEXT, address TEXT, zipcode TEXT, town TEXT, country TEXT,
		country_code TEXT, region TEXT, latitude REAL, longitude REAL,
		internetsite TEXT, osm_id INTEGER, osm_type TEXT, geohash TEXT,
		wikidata_id TEXT, mb_place_id TEXT, notes_md TEXT, attributes TEXT,
		parking TEXT, floor_condition TEXT, no_street_shoes INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at INTEGER, updated_by TEXT,
		wheelchair_accessible INTEGER, hearing_loop INTEGER, visual_support INTEGER,
		aliases TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO locations (location, aliases) VALUES ('Hall', '["Old Hall"]')`); err != nil {
		t.Fatal(err)
	}

	migrateLocationAliasesToJunction()
	var n int
	db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('locations') WHERE name='aliases'").Scan(&n)
	if n != 0 {
		t.Fatalf("aliases column should be gone after the migration")
	}
	db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('locations') WHERE name='organization_id'").Scan(&n)
	if n != 1 {
		t.Fatalf("the rebuilt table should have organization_id")
	}
	db.QueryRow("SELECT COUNT(*) FROM location_aliases WHERE alias='Old Hall'").Scan(&n)
	if n != 1 {
		t.Fatalf("alias should move to location_aliases")
	}
	db.QueryRow("SELECT COUNT(*) FROM locations WHERE location='Hall' AND organization_id IS NULL").Scan(&n)
	if n != 1 {
		t.Fatalf("location should survive the rebuild, org-free")
	}
}
