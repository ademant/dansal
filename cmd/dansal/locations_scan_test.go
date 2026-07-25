package main

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// A room only ever sets location/floor_condition/no_street_shoes/attributes/
// parent_id/capacity/size_sqm — every other nullable column (address, town,
// etc.) is left NULL and inherited from its parent at read time (#687).
// scanLocation must tolerate that on every column, not just the ones
// exercised by whichever manual test happened to hit a bug.
func TestScanLocationToleratesAllNullFields(t *testing.T) {
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

	if _, err := db.Exec(`INSERT INTO locations (location, parent_id) VALUES ('Bare Room', NULL)`); err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(`SELECT ` + locationCols + `
		FROM locations l LEFT JOIN location_organizations lo ON l.id=lo.location_id
		GROUP BY l.id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var loc Location
		if err := scanLocation(rows, &loc); err != nil {
			t.Fatalf("scanLocation on all-NULL-optional-fields row: %v", err)
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected at least one row")
	}
}
