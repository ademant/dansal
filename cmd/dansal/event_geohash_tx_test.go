package main

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// TestInsertEventGeohashUsesTransaction covers #1639. The pool has a single
// connection and the event is inserted inside an open transaction that holds
// it. Setting location_geohash must reuse that transaction's connection; if it
// asked the pool for another one it would wait forever, which is the deadlock
// seen on dev. The geohash must also be set once the transaction commits.
func TestInsertEventGeohashUsesTransaction(t *testing.T) {
	conn := setupDedupTestDB(t)
	loc := insertTestLocation(t, 50.73, 7.10)
	conn.SetMaxOpenConns(1)

	const start = int64(2000000000)
	done := make(chan error, 1)
	go func() {
		tx, err := conn.Begin()
		if err != nil {
			done <- err
			return
		}
		id, _, _, err := insertEvent(tx, EventInput{
			Title: "Geohash", StartTime: start, EndTime: start + 3600,
			IsPublished: true, LocationID: int64(loc),
		})
		if err != nil {
			tx.Rollback()
			done <- err
			return
		}
		if err := tx.Commit(); err != nil {
			done <- err
			return
		}
		var gh sql.NullString
		if err := conn.QueryRow("SELECT location_geohash FROM events WHERE id = ?", id).Scan(&gh); err != nil {
			done <- err
			return
		}
		if !gh.Valid || gh.String == "" {
			done <- fmt.Errorf("location_geohash not set after commit")
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: insertEvent inside a transaction waited for a second pool connection")
	}
}
