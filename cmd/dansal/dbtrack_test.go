package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openTrackedTestDB opens a fresh SQLite file through the tracking driver and
// turns tracking on for the test. The tracking flag and registry are restored
// when the test ends.
func openTrackedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	prev := trackingDB
	trackingDB = true
	t.Cleanup(func() { trackingDB = prev })

	d, err := sql.Open(trackedDriverName, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec("CREATE TABLE t (id INTEGER, name TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec("INSERT INTO t VALUES (1, 'a'), (2, 'b')"); err != nil {
		t.Fatal(err)
	}
	return d
}

// openCount returns how many entries of kind are open, across all call sites.
func openCount(kind string) (n int, sites []string) {
	groups, _ := trackedOpenGroups(time.Now())
	for key, g := range groups {
		if strings.HasPrefix(key, kind+" ") {
			n += g.count
			sites = append(sites, key)
		}
	}
	return n, sites
}

func TestTrackedDriverPassesQueriesThrough(t *testing.T) {
	d := openTrackedTestDB(t)
	var name string
	if err := d.QueryRow("SELECT name FROM t WHERE id = ?", 2).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "b" {
		t.Fatalf("name = %q, want b", name)
	}
	rows, err := d.Query("SELECT id FROM t ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("ids = %v, want [1 2]", ids)
	}
	if n, _ := openCount("rows"); n != 0 {
		t.Fatalf("open rows after close = %d, want 0", n)
	}
}

func TestTrackedDriverCountsOpenRowsByCallSite(t *testing.T) {
	d := openTrackedTestDB(t)
	rows, err := d.Query("SELECT id FROM t")
	if err != nil {
		t.Fatal(err)
	}
	n, sites := openCount("rows")
	if n != 1 {
		t.Fatalf("open rows = %d, want 1", n)
	}
	if len(sites) != 1 || !strings.Contains(sites[0], "dbtrack_test.go") {
		t.Fatalf("site = %v, want this test file", sites)
	}
	rows.Close()
	if n, _ := openCount("rows"); n != 0 {
		t.Fatalf("open rows after close = %d, want 0", n)
	}
}

func TestTrackedDriverCountsOpenTxUntilCommit(t *testing.T) {
	d := openTrackedTestDB(t)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := openCount("tx"); n != 1 {
		t.Fatalf("open tx = %d, want 1", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n, _ := openCount("tx"); n != 0 {
		t.Fatalf("open tx after commit = %d, want 0", n)
	}
}

func TestTrackedDriverReportsResultSetHeldPastThreshold(t *testing.T) {
	d := openTrackedTestDB(t)
	rows, err := d.Query("SELECT id FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	// database/sql keeps a connection checked out while its rows are open, so
	// a result set that is never closed stays listed. Look at it from a time
	// past the threshold: it must be reported as held.
	_, held := trackedOpenGroups(time.Now().Add(trackedAfter + time.Second))
	if len(held) != 1 || !strings.Contains(held[0], "kind=rows") || !strings.Contains(held[0], "dbtrack_test.go") {
		t.Fatalf("held = %v, want one rows entry from this test file", held)
	}
	rows.Close()
	if _, held := trackedOpenGroups(time.Now().Add(trackedAfter + time.Second)); len(held) != 0 {
		t.Fatalf("held after close = %v, want none", held)
	}
}
