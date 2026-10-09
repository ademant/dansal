package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestCmdPurgeActorSmoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	db.Exec(`CREATE TABLE followers (id INTEGER PRIMARY KEY, org_id INTEGER, actor_uri TEXT, inbox_url TEXT, fail_count INTEGER DEFAULT 0)`)
	db.Exec(`CREATE TABLE follows (id INTEGER PRIMARY KEY, actor_id INTEGER, followee_ap_id TEXT, followee_inbox TEXT, follow_activity_id TEXT, state TEXT)`)
	db.Exec(`CREATE TABLE tag_followers (id INTEGER PRIMARY KEY, tag_slug TEXT, actor_uri TEXT, inbox_url TEXT, follow_activity_id TEXT)`)
	db.Exec(`CREATE TABLE delivery_failures (id INTEGER PRIMARY KEY, activity_id TEXT, org_id INTEGER, inbox_url TEXT, activity_json TEXT, attempts INTEGER, last_error TEXT, next_attempt_at INTEGER)`)

	actorURI := "https://remote.example/actor"
	inbox := "https://remote.example/inbox"
	db.Exec(`INSERT INTO followers (org_id, actor_uri, inbox_url) VALUES (7, ?, ?)`, actorURI, inbox)
	db.Exec(`INSERT INTO follows (actor_id, followee_ap_id, followee_inbox, follow_activity_id, state) VALUES (1, ?, ?, 'act0', 'accepted')`, actorURI, inbox)
	db.Exec(`INSERT INTO tag_followers (tag_slug, actor_uri, inbox_url, follow_activity_id) VALUES ('bal-folk', ?, ?, 'act1')`, actorURI, inbox)
	db.Exec(`INSERT INTO delivery_failures (activity_id, org_id, inbox_url, activity_json, attempts, next_attempt_at) VALUES ('act2', 7, ?, '{}', 1, 0)`, inbox)

	inboxes, err := purgeActorInboxes(db, actorURI)
	if err != nil {
		t.Fatal(err)
	}
	if len(inboxes) != 1 || inboxes[0] != inbox {
		t.Fatalf("inboxes = %v", inboxes)
	}

	// Dry run: nothing should actually be deleted.
	counts, err := purgeActorData(db, actorURI, inboxes, false)
	if err != nil {
		t.Fatal(err)
	}
	if counts["followers"] != 1 || counts["follows"] != 1 || counts["tag_followers"] != 1 || counts["delivery_failures"] != 1 {
		t.Fatalf("dry-run counts = %+v", counts)
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM followers").Scan(&n)
	if n != 1 {
		t.Fatalf("dry-run should not have deleted followers row, count = %d", n)
	}

	// Apply: now it should actually delete.
	counts, err = purgeActorData(db, actorURI, inboxes, true)
	if err != nil {
		t.Fatal(err)
	}
	if counts["followers"] != 1 || counts["follows"] != 1 || counts["tag_followers"] != 1 || counts["delivery_failures"] != 1 {
		t.Fatalf("apply counts = %+v", counts)
	}
	for _, table := range []string{"followers", "follows", "tag_followers", "delivery_failures"} {
		db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
		if n != 0 {
			t.Errorf("%s should be empty after apply, got %d", table, n)
		}
	}
}
