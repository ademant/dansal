package main

import (
	"database/sql"
	"log"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

func initDB(path string) *sql.DB {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS actors (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER UNIQUE NOT NULL,
    org_slug TEXT UNIQUE NOT NULL,
    public_key_pem TEXT NOT NULL,
    private_key_pem TEXT NOT NULL,
    public_key_ed25519_pem TEXT,
    private_key_ed25519_pem TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS followers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL,
    actor_uri TEXT NOT NULL,
    inbox_url TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(org_id, actor_uri)
);
CREATE TABLE IF NOT EXISTS delivered (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL,
    org_id INTEGER NOT NULL,
    delivered_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(event_id, org_id)
);
CREATE TABLE IF NOT EXISTS follows (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_id INTEGER NOT NULL,
    followee_ap_id TEXT NOT NULL,
    followee_inbox TEXT NOT NULL,
    follow_activity_id TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(actor_id, followee_ap_id)
);
CREATE TABLE IF NOT EXISTS federated_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ap_id TEXT UNIQUE NOT NULL,
    actor_id TEXT NOT NULL,
    name TEXT,
    start_time TEXT,
    end_time TEXT,
    url TEXT,
    location_name TEXT,
    description TEXT,
    image_url TEXT,
    tags TEXT,
    raw_json TEXT,
    received_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS site_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS event_templates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    org_id INTEGER,
    name TEXT NOT NULL,
    data TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`); err != nil {
		log.Fatalf("init db schema: %v", err)
	}
	// Idempotent column additions for schema evolution
	db.Exec("ALTER TABLE federated_events ADD COLUMN description TEXT")
	db.Exec("ALTER TABLE federated_events ADD COLUMN image_url TEXT")
	db.Exec("ALTER TABLE federated_events ADD COLUMN tags TEXT")
	return db
}

type ActorRecord struct {
	ID                   int
	OrgID                int
	OrgSlug              string
	PublicKeyPEM         string
	PrivateKeyPEM        string
	PublicKeyEd25519PEM  string
	PrivateKeyEd25519PEM string
}

func getActorBySlug(db *sql.DB, slug string) (*ActorRecord, error) {
	var a ActorRecord
	err := db.QueryRow(
		"SELECT id, org_id, org_slug, public_key_pem, private_key_pem, public_key_ed25519_pem, private_key_ed25519_pem FROM actors WHERE org_slug = ?",
		slug,
	).Scan(&a.ID, &a.OrgID, &a.OrgSlug, &a.PublicKeyPEM, &a.PrivateKeyPEM, &a.PublicKeyEd25519PEM, &a.PrivateKeyEd25519PEM)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func getActorByOrgID(db *sql.DB, orgID int) (*ActorRecord, error) {
	var a ActorRecord
	err := db.QueryRow(
		"SELECT id, org_id, org_slug, public_key_pem, private_key_pem, public_key_ed25519_pem, private_key_ed25519_pem FROM actors WHERE org_id = ?",
		orgID,
	).Scan(&a.ID, &a.OrgID, &a.OrgSlug, &a.PublicKeyPEM, &a.PrivateKeyPEM, &a.PublicKeyEd25519PEM, &a.PrivateKeyEd25519PEM)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ensureRelayActor creates (or fetches) the special relay actor with org_id=0.
func ensureRelayActor(db *sql.DB) (*ActorRecord, error) {
	return ensureActor(db, 0, "relay")
}

func ensureActor(db *sql.DB, orgID int, orgSlug string) (*ActorRecord, error) {
	a, err := getActorByOrgID(db, orgID)
	if err == nil {
		return a, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	pub, priv, err := generateRSAKeyPair()
	if err != nil {
		return nil, err
	}

	// Generate Ed25519 key pair
	pubEd25519, privEd25519, err := generateEd25519KeyPair()
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(
		"INSERT INTO actors (org_id, org_slug, public_key_pem, private_key_pem, public_key_ed25519_pem, private_key_ed25519_pem) VALUES (?, ?, ?, ?, ?, ?)",
		orgID, orgSlug, pub, priv, pubEd25519, privEd25519,
	)
	if err != nil {
		return nil, err
	}
	return getActorByOrgID(db, orgID)
}

func addFollower(db *sql.DB, orgID int, actorURI, inboxURL string) error {
	_, err := db.Exec(
		"INSERT OR IGNORE INTO followers (org_id, actor_uri, inbox_url) VALUES (?, ?, ?)",
		orgID, actorURI, inboxURL,
	)
	return err
}

func removeFollower(db *sql.DB, orgID int, actorURI string) error {
	_, err := db.Exec(
		"DELETE FROM followers WHERE org_id = ? AND actor_uri = ?",
		orgID, actorURI,
	)
	return err
}

func countFollowers(db *sql.DB, orgID int) (int, error) {
	var n int
	err := db.QueryRow("SELECT COUNT(*) FROM followers WHERE org_id = ?", orgID).Scan(&n)
	return n, err
}

func listFollowers(db *sql.DB, orgID int) ([]struct{ ActorURI, InboxURL string }, error) {
	rows, err := db.Query(
		"SELECT actor_uri, inbox_url FROM followers WHERE org_id = ? ORDER BY created_at",
		orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fs []struct{ ActorURI, InboxURL string }
	for rows.Next() {
		var f struct{ ActorURI, InboxURL string }
		if err := rows.Scan(&f.ActorURI, &f.InboxURL); err != nil {
			return nil, err
		}
		fs = append(fs, f)
	}
	return fs, nil
}

func markDelivered(db *sql.DB, eventID, orgID int) error {
	_, err := db.Exec(
		"INSERT OR IGNORE INTO delivered (event_id, org_id) VALUES (?, ?)",
		eventID, orgID,
	)
	return err
}

func isDelivered(db *sql.DB, eventID, orgID int) bool {
	var n int
	db.QueryRow(
		"SELECT COUNT(*) FROM delivered WHERE event_id = ? AND org_id = ?",
		eventID, orgID,
	).Scan(&n)
	return n > 0
}

type FollowRecord struct {
	ID               int
	ActorID          int
	FolloweeAPID     string
	FolloweeInbox    string
	FollowActivityID string
	State            string
	CreatedAt        string
}

func addFollow(db *sql.DB, actorID int, followeeAPID, followeeInbox, followActivityID string) error {
	_, err := db.Exec(
		"INSERT OR IGNORE INTO follows (actor_id, followee_ap_id, followee_inbox, follow_activity_id) VALUES (?, ?, ?, ?)",
		actorID, followeeAPID, followeeInbox, followActivityID,
	)
	return err
}

func getFollow(db *sql.DB, actorID int, followeeAPID string) (*FollowRecord, error) {
	var f FollowRecord
	err := db.QueryRow(
		"SELECT id, actor_id, followee_ap_id, followee_inbox, follow_activity_id, state, created_at FROM follows WHERE actor_id=? AND followee_ap_id=?",
		actorID, followeeAPID,
	).Scan(&f.ID, &f.ActorID, &f.FolloweeAPID, &f.FolloweeInbox, &f.FollowActivityID, &f.State, &f.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func listFollows(db *sql.DB, actorID int) ([]FollowRecord, error) {
	rows, err := db.Query(
		"SELECT id, actor_id, followee_ap_id, followee_inbox, follow_activity_id, state, created_at FROM follows WHERE actor_id=? ORDER BY created_at",
		actorID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fs []FollowRecord
	for rows.Next() {
		var f FollowRecord
		if err := rows.Scan(&f.ID, &f.ActorID, &f.FolloweeAPID, &f.FolloweeInbox, &f.FollowActivityID, &f.State, &f.CreatedAt); err != nil {
			return nil, err
		}
		fs = append(fs, f)
	}
	return fs, nil
}

func removeFollow(db *sql.DB, actorID int, followeeAPID string) error {
	_, err := db.Exec(
		"DELETE FROM follows WHERE actor_id=? AND followee_ap_id=?",
		actorID, followeeAPID,
	)
	return err
}

func updateFollowStateByActivityID(db *sql.DB, followActivityID, state string) error {
	_, err := db.Exec(
		"UPDATE follows SET state=? WHERE follow_activity_id=?",
		state, followActivityID,
	)
	return err
}

func listOrgActorSlugs(db *sql.DB) (map[int]string, error) {
	rows, err := db.Query("SELECT org_id, org_slug FROM actors WHERE org_id > 0")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[int]string)
	for rows.Next() {
		var orgID int
		var slug string
		if err := rows.Scan(&orgID, &slug); err != nil {
			return nil, err
		}
		m[orgID] = slug
	}
	return m, nil
}

func getSiteSetting(db *sql.DB, key string) string {
	var v string
	db.QueryRow("SELECT value FROM site_settings WHERE key = ?", key).Scan(&v)
	return v
}

func setSiteSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec(
		"INSERT INTO site_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		key, value,
	)
	return err
}


type FederatedEvent struct {
	ID           int64
	APID         string
	ActorID      string
	Name         string
	StartTime    string
	EndTime      string
	URL          string
	LocationName string
	Description  string
	ImageURL     string
	Tags         []string
	RawJSON      string
	ReceivedAt   int64
}

func upsertFederatedEvent(db *sql.DB, fe FederatedEvent) error {
	tagsStr := strings.Join(fe.Tags, ",")
	_, err := db.Exec(
		`INSERT INTO federated_events (ap_id, actor_id, name, start_time, end_time, url, location_name, description, image_url, tags, raw_json, received_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(ap_id) DO UPDATE SET
		   actor_id=excluded.actor_id, name=excluded.name, start_time=excluded.start_time,
		   end_time=excluded.end_time, url=excluded.url, location_name=excluded.location_name,
		   description=excluded.description, image_url=excluded.image_url, tags=excluded.tags,
		   raw_json=excluded.raw_json, received_at=excluded.received_at`,
		fe.APID, fe.ActorID, fe.Name, fe.StartTime, fe.EndTime, fe.URL, fe.LocationName,
		fe.Description, fe.ImageURL, tagsStr, fe.RawJSON, fe.ReceivedAt,
	)
	return err
}

func deleteFederatedEvent(db *sql.DB, apID string) error {
	_, err := db.Exec("DELETE FROM federated_events WHERE ap_id = ?", apID)
	return err
}

func listFederatedEvents(db *sql.DB) ([]FederatedEvent, error) {
	rows, err := db.Query(
		"SELECT id, ap_id, actor_id, name, start_time, end_time, url, location_name, COALESCE(description,''), COALESCE(image_url,''), COALESCE(tags,''), raw_json, received_at FROM federated_events ORDER BY start_time ASC",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fes []FederatedEvent
	for rows.Next() {
		var fe FederatedEvent
		var tagsStr string
		if err := rows.Scan(&fe.ID, &fe.APID, &fe.ActorID, &fe.Name, &fe.StartTime, &fe.EndTime, &fe.URL, &fe.LocationName, &fe.Description, &fe.ImageURL, &tagsStr, &fe.RawJSON, &fe.ReceivedAt); err != nil {
			return nil, err
		}
		if tagsStr != "" {
			fe.Tags = strings.Split(tagsStr, ",")
		}
		fes = append(fes, fe)
	}
	return fes, nil
}

type EventTemplate struct {
	ID        int
	UserID    int
	OrgID     *int
	Name      string
	Data      string
	CreatedAt string
}

func listTemplates(db *sql.DB, userID int, orgIDs []int) ([]EventTemplate, error) {
	query := "SELECT id, user_id, org_id, name, data, created_at FROM event_templates WHERE user_id = ?"
	args := []any{userID}
	for _, oid := range orgIDs {
		query += " OR org_id = ?"
		args = append(args, oid)
	}
	query += " ORDER BY name"
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ts []EventTemplate
	for rows.Next() {
		var t EventTemplate
		if err := rows.Scan(&t.ID, &t.UserID, &t.OrgID, &t.Name, &t.Data, &t.CreatedAt); err != nil {
			return nil, err
		}
		ts = append(ts, t)
	}
	return ts, nil
}

func getTemplate(db *sql.DB, id int) (EventTemplate, error) {
	var t EventTemplate
	err := db.QueryRow(
		"SELECT id, user_id, org_id, name, data, created_at FROM event_templates WHERE id = ?", id,
	).Scan(&t.ID, &t.UserID, &t.OrgID, &t.Name, &t.Data, &t.CreatedAt)
	return t, err
}

func saveTemplate(db *sql.DB, userID int, orgID *int, name, data string) (int64, error) {
	res, err := db.Exec(
		"INSERT INTO event_templates (user_id, org_id, name, data) VALUES (?, ?, ?, ?)",
		userID, orgID, name, data,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func deleteTemplate(db *sql.DB, id, userID int, isAdmin bool) error {
	var err error
	if isAdmin {
		_, err = db.Exec("DELETE FROM event_templates WHERE id = ?", id)
	} else {
		_, err = db.Exec("DELETE FROM event_templates WHERE id = ? AND user_id = ?", id, userID)
	}
	return err
}
