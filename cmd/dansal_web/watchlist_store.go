package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log"
	"strconv"
	"time"
)

// Server-side watchlists (#1554). A visitor's list can be saved under a random
// key. The browser holds the key and a UUID; the server stores only SHA-256
// hashes of both, plus the ids. One list per UUID: saving with no key creates
// a new key and replaces the old one, which then stops working.

// watchlistDefaultRetentionMonths is used when webmin hasn't set
// site_settings.watchlist_retention_months.
const watchlistDefaultRetentionMonths = 12

func hashWatchlistToken(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// newWatchlistKey returns a random 128-bit key, hex-encoded.
func newWatchlistKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// saveWatchlistForUUID creates or replaces the list for a UUID, with a new key.
func saveWatchlistForUUID(db *sql.DB, uuidHash, keyHash, idsJSON string) error {
	_, err := db.Exec(`INSERT INTO watchlists (uuid_hash, key_hash, ids_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(uuid_hash) DO UPDATE SET key_hash = excluded.key_hash,
			ids_json = excluded.ids_json, updated_at = excluded.updated_at`,
		uuidHash, keyHash, idsJSON, time.Now().Unix())
	return err
}

// updateWatchlistByKey replaces the ids of the list that matches both the UUID
// and the key. It reports false when no such list exists.
func updateWatchlistByKey(db *sql.DB, uuidHash, keyHash, idsJSON string) (bool, error) {
	res, err := db.Exec(`UPDATE watchlists SET ids_json = ?, updated_at = ?
		WHERE uuid_hash = ? AND key_hash = ?`,
		idsJSON, time.Now().Unix(), uuidHash, keyHash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// loadWatchlistByKey returns the stored ids JSON for a key, and whether a list
// exists for it. A successful load refreshes updated_at, so an active list
// doesn't expire.
func loadWatchlistByKey(db *sql.DB, keyHash string) (string, bool) {
	var idsJSON string
	err := db.QueryRow("SELECT ids_json FROM watchlists WHERE key_hash = ?", keyHash).Scan(&idsJSON)
	if err != nil {
		return "", false
	}
	db.Exec("UPDATE watchlists SET updated_at = ? WHERE key_hash = ?", time.Now().Unix(), keyHash)
	return idsJSON, true
}

// watchlistRetention reads the webmin-set retention in months.
func watchlistRetention(db *sql.DB) time.Duration {
	months := watchlistDefaultRetentionMonths
	if v := getSiteSetting(db, "watchlist_retention_months"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 120 {
			months = n
		}
	}
	return time.Duration(months) * 30 * 24 * time.Hour
}

// sweepWatchlists deletes lists that have had no save or load within the
// retention period. Called hourly from startDataRetentionSweep.
func sweepWatchlists(db *sql.DB) {
	cutoff := time.Now().Add(-watchlistRetention(db)).Unix()
	if res, err := db.Exec("DELETE FROM watchlists WHERE updated_at < ?", cutoff); err != nil {
		log.Printf("data retention sweep: watchlists: %v", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("data retention sweep: removed %d expired watchlist(s)", n)
	}
}
