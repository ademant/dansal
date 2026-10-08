package main

import (
	"path/filepath"
	"testing"
	"time"
)

// TestDataRetentionDuration covers #1440 (G3): DataRetentionDays is
// configurable via YAML, defaulting to 90 days for a zero-value Config
// (e.g. hand-built in tests, or an operator who never set it).
func TestDataRetentionDuration(t *testing.T) {
	if got, want := (&Config{}).dataRetentionDuration(), 90*24*time.Hour; got != want {
		t.Errorf("zero-value Config: dataRetentionDuration() = %v, want %v", got, want)
	}
	if got, want := (&Config{DataRetentionDays: 30}).dataRetentionDuration(), 30*24*time.Hour; got != want {
		t.Errorf("DataRetentionDays=30: dataRetentionDuration() = %v, want %v", got, want)
	}
}

// TestSweepGeocodeCache covers #1440 (G3): geocode_cache rows (visitor
// search query text) past cfg.dataRetentionDuration() are actually deleted,
// not just skipped at read time (getGeocodeCache) as before — a row that
// was never looked up again used to stay in the table forever.
func TestSweepGeocodeCache(t *testing.T) {
	db := initDB(filepath.Join(t.TempDir(), "web.db"))
	defer db.Close()

	now := time.Now().Unix()
	if _, err := db.Exec("INSERT INTO geocode_cache (query, results_json, fetched_at) VALUES (?, '[]', ?)",
		"old query", now-100*24*60*60); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO geocode_cache (query, results_json, fetched_at) VALUES (?, '[]', ?)",
		"recent query", now-10*24*60*60); err != nil {
		t.Fatal(err)
	}

	sweepGeocodeCache(&Config{DataRetentionDays: 90}, db)

	var queries []string
	rows, err := db.Query("SELECT query FROM geocode_cache ORDER BY query")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var q string
		if err := rows.Scan(&q); err != nil {
			t.Fatal(err)
		}
		queries = append(queries, q)
	}
	if len(queries) != 1 || queries[0] != "recent query" {
		t.Errorf("geocode_cache after sweep = %v, want only [\"recent query\"]", queries)
	}
}
