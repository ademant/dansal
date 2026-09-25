package main

import (
	"database/sql"
	"testing"
)

// #1375: deleting a user who created content used to fail with a FOREIGN KEY
// error (events.created_by_id etc. reference users(id) with NO ACTION).
func TestDeleteUserByIDKeepsContentAndClearsReferences(t *testing.T) {
	old := db
	t.Cleanup(func() { db = old })
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1) // one connection so the PRAGMA below applies to every statement
	t.Cleanup(func() { conn.Close() })
	db = conn
	if err := createTables(); err != nil {
		t.Fatalf("createTables: %v", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}

	res, err := db.Exec("INSERT INTO users (email, password_hash, role) VALUES ('svc@example.test', 'x', 'publisher')")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	uid, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO events (title, start_time, end_time, created_by_id, changed_by_id) VALUES ('E', 1000, 2000, ?, ?)`, uid, uid); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO musicians (bandname, created_by_id) VALUES ('Band', ?)`, uid); err != nil {
		t.Fatalf("insert musician: %v", err)
	}

	// Sanity: a plain DELETE really is blocked, otherwise this test proves nothing.
	if _, err := db.Exec("DELETE FROM users WHERE id=?", uid); err == nil {
		t.Fatal("expected a plain DELETE to hit the FOREIGN KEY constraint")
	}

	if err := deleteUserByID(db, uid); err != nil {
		t.Fatalf("deleteUserByID: %v", err)
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM users WHERE id=?", uid).Scan(&n)
	if n != 0 {
		t.Fatal("user still exists")
	}
	var created, changed sql.NullInt64
	if err := db.QueryRow("SELECT created_by_id, changed_by_id FROM events WHERE title='E'").Scan(&created, &changed); err != nil {
		t.Fatalf("event must be kept: %v", err)
	}
	if created.Valid || changed.Valid {
		t.Fatalf("event attribution must be cleared, got %v %v", created, changed)
	}
	var mc sql.NullInt64
	if err := db.QueryRow("SELECT created_by_id FROM musicians WHERE bandname='Band'").Scan(&mc); err != nil || mc.Valid {
		t.Fatalf("musician must be kept with attribution cleared: %v %v", mc, err)
	}
}
