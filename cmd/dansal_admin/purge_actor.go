package main

import (
	"database/sql"
	"flag"
	"fmt"
)

// cmdPurgeActor implements `dansal_admin purge-actor` (#1491, compliance
// G12): fulfils an erasure request for a remote ActivityPub actor's
// federation data. That data (actors/followers/follows/tag_followers/
// delivery_failures) lives only in dansal_web's own web.db — unlike every
// other dansal_admin command, which talks to cmd/dansal's calendar.db either
// via the admin socket or (like fill-location-fields/export/export-user)
// by opening that file directly. dansal_web has no admin socket of its own,
// so this command opens web.db directly instead, following the latter
// precedent.
//
// Dry-run by default (mirrors `import`'s --apply convention) since this is
// an irreversible cross-table delete with no socket-side audit trail.
func cmdPurgeActor(args []string) {
	fs := flag.NewFlagSet("purge-actor", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["purge-actor"]) }
	actorURI := fs.String("actor-uri", "", "actor_uri of the remote actor to erase")
	dbPath := fs.String("db", "/var/lib/dansal-web/web.db", "path to web.db")
	apply := fs.Bool("apply", false, "write changes (default is dry-run)")
	fs.Parse(args)

	if *actorURI == "" {
		die("--actor-uri is required")
	}

	db := openDB(*dbPath)
	defer db.Close()

	inboxes, err := purgeActorInboxes(db, *actorURI)
	if err != nil {
		die("purge-actor: list inboxes: %v", err)
	}

	counts, err := purgeActorData(db, *actorURI, inboxes, *apply)
	if err != nil {
		die("purge-actor: %v", err)
	}

	if !*apply {
		fmt.Println("dry run (pass --apply to actually delete):")
	} else {
		fmt.Println("deleted:")
	}
	for _, table := range []string{"followers", "follows", "tag_followers", "delivery_failures"} {
		fmt.Printf("  %-18s %d row(s)\n", table, counts[table])
	}
}

// purgeActorInboxes collects every inbox_url known for actorURI across
// followers and tag_followers, before those rows are deleted — needed to
// also clear delivery_failures, which is keyed by inbox_url rather than
// actor_uri.
func purgeActorInboxes(db *sql.DB, actorURI string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, q := range []string{
		"SELECT DISTINCT inbox_url FROM followers WHERE actor_uri = ?",
		"SELECT DISTINCT inbox_url FROM tag_followers WHERE actor_uri = ?",
	} {
		rows, err := db.Query(q, actorURI)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var inbox string
			if err := rows.Scan(&inbox); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[inbox] {
				seen[inbox] = true
				out = append(out, inbox)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// purgeActorData deletes every row across the four federation tables that
// reference actorURI (or, for delivery_failures, one of its known
// inboxes), in a single transaction. When apply is false it rolls back and
// just reports the counts a real run would delete.
func purgeActorData(db *sql.DB, actorURI string, inboxes []string, apply bool) (map[string]int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	counts := map[string]int64{}

	res, err := tx.Exec("DELETE FROM followers WHERE actor_uri = ?", actorURI)
	if err != nil {
		return nil, fmt.Errorf("followers: %w", err)
	}
	counts["followers"], _ = res.RowsAffected()

	res, err = tx.Exec("DELETE FROM follows WHERE followee_ap_id = ?", actorURI)
	if err != nil {
		return nil, fmt.Errorf("follows: %w", err)
	}
	counts["follows"], _ = res.RowsAffected()

	res, err = tx.Exec("DELETE FROM tag_followers WHERE actor_uri = ?", actorURI)
	if err != nil {
		return nil, fmt.Errorf("tag_followers: %w", err)
	}
	counts["tag_followers"], _ = res.RowsAffected()

	for _, inbox := range inboxes {
		res, err = tx.Exec("DELETE FROM delivery_failures WHERE inbox_url = ?", inbox)
		if err != nil {
			return nil, fmt.Errorf("delivery_failures: %w", err)
		}
		n, _ := res.RowsAffected()
		counts["delivery_failures"] += n
	}

	if !apply {
		return counts, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return counts, nil
}
