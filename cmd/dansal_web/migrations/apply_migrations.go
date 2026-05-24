package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// applyMigrations applies all SQL migration files in the migrations directory
func applyMigrations(db *sql.DB, migrationsDir string) error {
	// Create migrations table if it doesn't exist
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			migration_name TEXT UNIQUE NOT NULL,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}

	// Get list of already applied migrations
	applied := make(map[string]bool)
	rows, err := db.Query("SELECT migration_name FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("failed to query applied migrations: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("failed to scan migration name: %w", err)
		}
		applied[name] = true
	}

	// Find all migration files
	files, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("failed to read migrations directory: %w", err)
	}

	// Sort files by name to ensure proper order
	var migrationFiles []string
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".sql") {
			migrationFiles = append(migrationFiles, file.Name())
		}
	}
	sort.Strings(migrationFiles)

	// Apply each migration
	for _, filename := range migrationFiles {
		migrationName := strings.TrimSuffix(filename, ".sql")
		
		// Skip already applied migrations
		if applied[migrationName] {
			log.Printf("Migration %s already applied, skipping", migrationName)
			continue
		}
		
		// Read migration file
		path := filepath.Join(migrationsDir, filename)
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read migration file %s: %w", filename, err)
		}
		
		// Execute migration
		log.Printf("Applying migration %s", migrationName)
		if _, err := db.Exec(string(content)); err != nil {
			return fmt.Errorf("failed to apply migration %s: %w", migrationName, err)
		}
		
		// Record migration
		if _, err := db.Exec("INSERT INTO schema_migrations (migration_name) VALUES (?)", migrationName); err != nil {
			return fmt.Errorf("failed to record migration %s: %w", migrationName, err)
		}
		
		log.Printf("Successfully applied migration %s", migrationName)
	}

	return nil
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <database-path> <migrations-directory>\n", os.Args[0])
		os.Exit(1)
	}

	dbPath := os.Args[1]
	migrationsDir := os.Args[2]

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	if err := applyMigrations(db, migrationsDir); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}

	log.Println("All migrations applied successfully")
}