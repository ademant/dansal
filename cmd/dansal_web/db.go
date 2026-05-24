package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// addColumnIfNotExists adds a column to a table if it doesn't already exist
// SQLite doesn't support IF NOT EXISTS with ALTER TABLE, so we need to check first
func addColumnIfNotExists(db *sql.DB, table, column, columnType string) {
	// Check if column exists
	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
		table, column,
	).Scan(&count)
	
	if err != nil {
		log.Printf("Warning: could not check if column %s exists in table %s: %v", column, table, err)
		return
	}
	
	if count == 0 {
		// Column doesn't exist, add it
		if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, columnType)); err != nil {
			log.Printf("Warning: could not add column %s to table %s: %v", column, table, err)
		} else {
			log.Printf("Added column %s to table %s", column, table)
		}
	}
}

func initDB(path string) *sql.DB {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	
	// Create tables
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS actors (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER UNIQUE NOT NULL,
    org_slug TEXT UNIQUE NOT NULL,
    public_key_pem TEXT NOT NULL,
    private_key_pem TEXT NOT NULL,
    public_key_ed25519_pem TEXT,
    private_key_ed25519_pem TEXT,
    public_key_multibase TEXT,
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
	
	// Add Ed25519 key columns (required for WebFinger implementation)
	// SQLite doesn't support IF NOT EXISTS with ALTER TABLE, so we check first
	addColumnIfNotExists(db, "actors", "public_key_ed25519_pem", "TEXT")
	addColumnIfNotExists(db, "actors", "private_key_ed25519_pem", "TEXT")
	addColumnIfNotExists(db, "actors", "public_key_multibase", "TEXT")
	
	db.Exec("ALTER TABLE federated_events ADD COLUMN description TEXT")
	db.Exec("ALTER TABLE federated_events ADD COLUMN image_url TEXT")
	db.Exec("ALTER TABLE federated_events ADD COLUMN tags TEXT")
	
	// Create schema_migrations table for tracking
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			migration_name TEXT UNIQUE NOT NULL,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		log.Printf("Warning: could not create schema_migrations table: %v", err)
	}
	
	// Mark the ed25519 migration as applied
	if _, err := db.Exec("INSERT OR IGNORE INTO schema_migrations (migration_name) VALUES ('001_add_ed25519_keys')"); err != nil {
		log.Printf("Warning: could not record ed25519 migration: %v", err)
	}
	
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
	PublicKeyMultibase   string
}

func getActorBySlug(db *sql.DB, slug string) (*ActorRecord, error) {
	var a ActorRecord
	var publicKeyEd25519PEM, privateKeyEd25519PEM, publicKeyMultibase sql.NullString
	err := db.QueryRow(
		"SELECT id, org_id, org_slug, public_key_pem, private_key_pem, public_key_ed25519_pem, private_key_ed25519_pem, public_key_multibase FROM actors WHERE org_slug = ?",
		slug,
	).Scan(&a.ID, &a.OrgID, &a.OrgSlug, &a.PublicKeyPEM, &a.PrivateKeyPEM, &publicKeyEd25519PEM, &privateKeyEd25519PEM, &publicKeyMultibase)
	if err != nil {
		return nil, err
	}
	// Convert NULL values to empty strings
	a.PublicKeyEd25519PEM = publicKeyEd25519PEM.String
	a.PrivateKeyEd25519PEM = privateKeyEd25519PEM.String
	a.PublicKeyMultibase = publicKeyMultibase.String
	return &a, nil
}

func getActorByOrgID(db *sql.DB, orgID int) (*ActorRecord, error) {
	var a ActorRecord
	var publicKeyEd25519PEM, privateKeyEd25519PEM, publicKeyMultibase sql.NullString
	err := db.QueryRow(
		"SELECT id, org_id, org_slug, public_key_pem, private_key_pem, public_key_ed25519_pem, private_key_ed25519_pem, public_key_multibase FROM actors WHERE org_id = ?",
		orgID,
	).Scan(&a.ID, &a.OrgID, &a.OrgSlug, &a.PublicKeyPEM, &a.PrivateKeyPEM, &publicKeyEd25519PEM, &privateKeyEd25519PEM, &publicKeyMultibase)
	if err != nil {
		return nil, err
	}
	// Convert NULL values to empty strings
	a.PublicKeyEd25519PEM = publicKeyEd25519PEM.String
	a.PrivateKeyEd25519PEM = privateKeyEd25519PEM.String
	a.PublicKeyMultibase = publicKeyMultibase.String
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
	pubEd25519, privEd25519, multibaseKey, err := generateEd25519KeyPairWithMultibase()
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(
		"INSERT INTO actors (org_id, org_slug, public_key_pem, private_key_pem, public_key_ed25519_pem, private_key_ed25519_pem, public_key_multibase) VALUES (?, ?, ?, ?, ?, ?, ?)",
		orgID, orgSlug, pub, priv, pubEd25519, privEd25519, multibaseKey,
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

type RSVP struct {
	ID        int64
	APID      string
	EventAPID string
	ActorID   string
	RSVPType  string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Interaction struct {
	ID             int64
	APID           string
	TargetType     string
	TargetID       string
	ActorID        string
	InteractionType string
	Content        string
	CreatedAt      time.Time
}

type LocationActor struct {
	ID                   int
	Slug                 string
	Name                 string
	Description          string
	Address              string
	Latitude             *float64
	Longitude            *float64
	PublicKeyPEM         string
	PrivateKeyPEM        string
	PublicKeyEd25519PEM  string
	PrivateKeyEd25519PEM string
	PublicKeyMultibase   string
	CreatedAt            time.Time
}

type MusicianActor struct {
	ID                   int
	Slug                 string
	Name                 string
	MusicBrainzID        string
	Description          string
	ImageURL             string
	PublicKeyPEM         string
	PrivateKeyPEM        string
	PublicKeyEd25519PEM  string
	PrivateKeyEd25519PEM string
	PublicKeyMultibase   string
	CreatedAt            time.Time
}

type WebFingerAlias struct {
	ID         int
	Alias      string
	TargetType string
	TargetID   int
	CreatedAt  time.Time
}

type EventUpdate struct {
	ID        int
	EventAPID string
	UpdateType string
	UpdateAPID string
	UpdatedAt  time.Time
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

// RSVP functions
func createRSVP(db *sql.DB, rsvp RSVP) error {
	_, err := db.Exec(
		`INSERT INTO rsvps (ap_id, event_ap_id, actor_id, rsvp_type, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rsvp.APID, rsvp.EventAPID, rsvp.ActorID, rsvp.RSVPType, rsvp.Status, rsvp.CreatedAt, rsvp.UpdatedAt,
	)
	return err
}

func getRSVP(db *sql.DB, apID string) (*RSVP, error) {
	var r RSVP
	err := db.QueryRow(
		`SELECT id, ap_id, event_ap_id, actor_id, rsvp_type, status, created_at, updated_at
		 FROM rsvps WHERE ap_id = ?`,
		apID,
	).Scan(&r.ID, &r.APID, &r.EventAPID, &r.ActorID, &r.RSVPType, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func listRSVPsByEvent(db *sql.DB, eventAPID string) ([]RSVP, error) {
	rows, err := db.Query(
		`SELECT id, ap_id, event_ap_id, actor_id, rsvp_type, status, created_at, updated_at
		 FROM rsvps WHERE event_ap_id = ? ORDER BY created_at ASC`,
		eventAPID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	var rsvps []RSVP
	for rows.Next() {
		var r RSVP
		if err := rows.Scan(&r.ID, &r.APID, &r.EventAPID, &r.ActorID, &r.RSVPType, &r.Status, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		rsvps = append(rsvps, r)
	}
	return rsvps, nil
}

func updateRSVPStatus(db *sql.DB, apID, status string) error {
	_, err := db.Exec(
		`UPDATE rsvps SET status = ?, updated_at = ? WHERE ap_id = ?`,
		status, time.Now(), apID,
	)
	return err
}

// Interaction functions
func createInteraction(db *sql.DB, interaction Interaction) error {
	_, err := db.Exec(
		`INSERT INTO interactions (ap_id, target_type, target_id, actor_id, interaction_type, content, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		interaction.APID, interaction.TargetType, interaction.TargetID, interaction.ActorID,
		interaction.InteractionType, interaction.Content, interaction.CreatedAt,
	)
	return err
}

func listInteractionsByTarget(db *sql.DB, targetID string) ([]Interaction, error) {
	rows, err := db.Query(
		`SELECT id, ap_id, target_type, target_id, actor_id, interaction_type, content, created_at
		 FROM interactions WHERE target_id = ? ORDER BY created_at DESC`,
		targetID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	var interactions []Interaction
	for rows.Next() {
		var i Interaction
		if err := rows.Scan(&i.ID, &i.APID, &i.TargetType, &i.TargetID, &i.ActorID, &i.InteractionType, &i.Content, &i.CreatedAt); err != nil {
			return nil, err
		}
		interactions = append(interactions, i)
	}
	return interactions, nil
}

// Location Actor functions
func createLocationActor(db *sql.DB, actor LocationActor) error {
	_, err := db.Exec(
		`INSERT INTO location_actors (
			slug, name, description, address, latitude, longitude,
			public_key_pem, private_key_pem, public_key_ed25519_pem, private_key_ed25519_pem, public_key_multibase
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		actor.Slug, actor.Name, actor.Description, actor.Address, actor.Latitude, actor.Longitude,
		actor.PublicKeyPEM, actor.PrivateKeyPEM, actor.PublicKeyEd25519PEM, actor.PrivateKeyEd25519PEM, actor.PublicKeyMultibase,
	)
	return err
}

func getLocationActorBySlug(db *sql.DB, slug string) (*LocationActor, error) {
	var a LocationActor
	var latitude, longitude sql.NullFloat64
	err := db.QueryRow(
		`SELECT id, slug, name, description, address, latitude, longitude,
			public_key_pem, private_key_pem, public_key_ed25519_pem, private_key_ed25519_pem, public_key_multibase, created_at
		 FROM location_actors WHERE slug = ?`,
		slug,
	).Scan(
		&a.ID, &a.Slug, &a.Name, &a.Description, &a.Address, &latitude, &longitude,
		&a.PublicKeyPEM, &a.PrivateKeyPEM, &a.PublicKeyEd25519PEM, &a.PrivateKeyEd25519PEM, &a.PublicKeyMultibase, &a.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if latitude.Valid {
		a.Latitude = &latitude.Float64
	}
	if longitude.Valid {
		a.Longitude = &longitude.Float64
	}
	return &a, nil
}

// Musician Actor functions
func createMusicianActor(db *sql.DB, actor MusicianActor) error {
	_, err := db.Exec(
		`INSERT INTO musician_actors (
			slug, name, musicbrainz_id, description, image_url,
			public_key_pem, private_key_pem, public_key_ed25519_pem, private_key_ed25519_pem, public_key_multibase
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		actor.Slug, actor.Name, actor.MusicBrainzID, actor.Description, actor.ImageURL,
		actor.PublicKeyPEM, actor.PrivateKeyPEM, actor.PublicKeyEd25519PEM, actor.PrivateKeyEd25519PEM, actor.PublicKeyMultibase,
	)
	return err
}

func getMusicianActorBySlug(db *sql.DB, slug string) (*MusicianActor, error) {
	var a MusicianActor
	err := db.QueryRow(
		`SELECT id, slug, name, musicbrainz_id, description, image_url,
			public_key_pem, private_key_pem, public_key_ed25519_pem, private_key_ed25519_pem, public_key_multibase, created_at
		 FROM musician_actors WHERE slug = ?`,
		slug,
	).Scan(
		&a.ID, &a.Slug, &a.Name, &a.MusicBrainzID, &a.Description, &a.ImageURL,
		&a.PublicKeyPEM, &a.PrivateKeyPEM, &a.PublicKeyEd25519PEM, &a.PrivateKeyEd25519PEM, &a.PublicKeyMultibase, &a.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// WebFinger Alias functions
func createWebFingerAlias(db *sql.DB, alias WebFingerAlias) error {
	_, err := db.Exec(
		`INSERT INTO webfinger_aliases (alias, target_type, target_id) VALUES (?, ?, ?)`,
		alias.Alias, alias.TargetType, alias.TargetID,
	)
	return err
}

func getWebFingerAlias(db *sql.DB, alias string) (*WebFingerAlias, error) {
	var a WebFingerAlias
	err := db.QueryRow(
		`SELECT id, alias, target_type, target_id, created_at FROM webfinger_aliases WHERE alias = ?`,
		alias,
	).Scan(&a.ID, &a.Alias, &a.TargetType, &a.TargetID, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Event Update functions
func createEventUpdate(db *sql.DB, update EventUpdate) error {
	_, err := db.Exec(
		`INSERT INTO event_updates (event_ap_id, update_type, update_ap_id, updated_at)
		 VALUES (?, ?, ?, ?)`,
		update.EventAPID, update.UpdateType, update.UpdateAPID, update.UpdatedAt,
	)
	return err
}

func listEventUpdates(db *sql.DB, eventAPID string) ([]EventUpdate, error) {
	rows, err := db.Query(
		`SELECT id, event_ap_id, update_type, update_ap_id, updated_at
		 FROM event_updates WHERE event_ap_id = ? ORDER BY updated_at DESC`,
		eventAPID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	var updates []EventUpdate
	for rows.Next() {
		var u EventUpdate
		if err := rows.Scan(&u.ID, &u.EventAPID, &u.UpdateType, &u.UpdateAPID, &u.UpdatedAt); err != nil {
			return nil, err
		}
		updates = append(updates, u)
	}
	return updates, nil
}
