package main

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// Test that the migration SQL is valid and can be executed
func TestMigrationSQLValidity(t *testing.T) {
	migrationSQL := `
		-- Composite index for event filtering
		CREATE INDEX IF NOT EXISTS idx_events_filter ON events(is_published, start_time, end_time, location_id);

		-- Geospatial index for location-based queries
		CREATE INDEX IF NOT EXISTS idx_locations_geo ON locations(latitude, longitude) WHERE latitude IS NOT NULL AND longitude IS NOT NULL;

		-- Organization-based event queries
		CREATE INDEX IF NOT EXISTS idx_events_org_published ON events(organization_id, is_published, start_time) WHERE organization_id IS NOT NULL;

		-- Tag search optimization
		CREATE INDEX IF NOT EXISTS idx_events_tag_search ON event_tags(tag, event_id);

		-- Musician and organization name searches
		CREATE INDEX IF NOT EXISTS idx_musicians_name ON musicians(bandname);
		CREATE INDEX IF NOT EXISTS idx_organizations_name ON organizations(name);

		-- Create schema_migrations table if it doesn't exist
		CREATE TABLE IF NOT EXISTS schema_migrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			migration_name TEXT UNIQUE NOT NULL,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		-- Record migration
		INSERT OR IGNORE INTO schema_migrations (migration_name) VALUES ('001_add_performance_indexes');
	`

	// Create a temporary database with basic tables
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create basic tables that the indexes reference
	_, err = db.Exec(`
		CREATE TABLE events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT,
			start_time INTEGER,
			end_time INTEGER,
			is_published INTEGER,
			location_id INTEGER,
			organization_id INTEGER
		);

		CREATE TABLE locations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			location TEXT,
			latitude REAL,
			longitude REAL
		);

		CREATE TABLE event_tags (
			event_id INTEGER,
			tag TEXT,
			PRIMARY KEY (event_id, tag)
		);

		CREATE TABLE musicians (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bandname TEXT
		);

		CREATE TABLE organizations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT
		);
	`)
	if err != nil {
		t.Fatalf("Failed to create basic tables: %v", err)
	}

	// Execute the migration SQL
	_, err = db.Exec(migrationSQL)
	if err != nil {
		t.Fatalf("Migration SQL failed: %v", err)
	}

	// Verify indexes were created
	var indexCount int
	err = db.QueryRow(`
		SELECT COUNT(*) 
		FROM sqlite_master 
		WHERE type = 'index' AND 
		      name IN ('idx_events_filter', 'idx_locations_geo', 'idx_events_org_published', 
		               'idx_events_tag_search', 'idx_musicians_name', 'idx_organizations_name')
	`).Scan(&indexCount)
	if err != nil {
		t.Fatalf("Failed to count indexes: %v", err)
	}

	expectedIndexes := 6
	if indexCount != expectedIndexes {
		t.Errorf("Expected %d indexes, got %d", expectedIndexes, indexCount)
	}

	// Verify migration was recorded
	var migrationName string
	err = db.QueryRow("SELECT migration_name FROM schema_migrations WHERE migration_name = '001_add_performance_indexes'").Scan(&migrationName)
	if err != nil {
		t.Fatalf("Failed to query migration: %v", err)
	}

	if migrationName != "001_add_performance_indexes" {
		t.Errorf("Expected migration name '001_add_performance_indexes', got '%s'", migrationName)
	}
}

// Test that the migration is idempotent
func TestMigrationIdempotency(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create basic tables
	_, err = db.Exec(`
		CREATE TABLE events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT,
			start_time INTEGER,
			end_time INTEGER,
			is_published INTEGER,
			location_id INTEGER,
			organization_id INTEGER
		);

		CREATE TABLE locations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			location TEXT,
			latitude REAL,
			longitude REAL
		);

		CREATE TABLE event_tags (
			event_id INTEGER,
			tag TEXT,
			PRIMARY KEY (event_id, tag)
		);

		CREATE TABLE musicians (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bandname TEXT
		);

		CREATE TABLE organizations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT
		);
	`)
	if err != nil {
		t.Fatalf("Failed to create basic tables: %v", err)
	}

	// Run migration twice
	migrationSQL := `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			migration_name TEXT UNIQUE NOT NULL,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE INDEX IF NOT EXISTS idx_events_filter ON events(is_published, start_time, end_time, location_id);
		CREATE INDEX IF NOT EXISTS idx_locations_geo ON locations(latitude, longitude) WHERE latitude IS NOT NULL AND longitude IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_events_org_published ON events(organization_id, is_published, start_time) WHERE organization_id IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_events_tag_search ON event_tags(tag, event_id);
		CREATE INDEX IF NOT EXISTS idx_musicians_name ON musicians(bandname);
		CREATE INDEX IF NOT EXISTS idx_organizations_name ON organizations(name);

		INSERT OR IGNORE INTO schema_migrations (migration_name) VALUES ('001_add_performance_indexes');
	`

	_, err = db.Exec(migrationSQL)
	if err != nil {
		t.Fatalf("First migration run failed: %v", err)
	}

	_, err = db.Exec(migrationSQL)
	if err != nil {
		t.Fatalf("Second migration run failed: %v", err)
	}

	// Should still have only one migration record
	var migrationCount int
	err = db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE migration_name = '001_add_performance_indexes'").Scan(&migrationCount)
	if err != nil {
		t.Fatalf("Failed to count migrations: %v", err)
	}

	if migrationCount != 1 {
		t.Errorf("Expected 1 migration after second run, got %d", migrationCount)
	}
}