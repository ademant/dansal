package main

import (
	"database/sql"
	"log"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func migrationApplied(db *sql.DB, version int) bool {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version=?", version).Scan(&n)
	return n > 0
}

func initDB(path string) *sql.DB {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000&_synchronous=NORMAL&_cache_size=-8000&_mmap_size=134217728")
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(time.Hour)
	// #737: actors.org_id, followers.org_id, delivered.event_id/org_id, and
	// event_templates.org_id all reference IDs in calendar.db (the dansal API DB).
	// SQLite cannot enforce cross-database foreign keys, so referential integrity
	// here is application-enforced: dansal_web deletes/updates these rows via
	// API-triggered hooks (org delete, event delete) rather than ON DELETE CASCADE.
	// This is intentional — coupling both DBs into one file would prevent
	// independent scaling and per-instance isolation. The blast radius of an orphaned
	// row is bounded: a missing org_id causes a missed ActivityPub delivery, not
	// data corruption. No known incidents have occurred from this pattern.
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS actors (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER UNIQUE NOT NULL,
    org_slug TEXT UNIQUE NOT NULL,
    public_key_pem TEXT NOT NULL,
    private_key_pem TEXT NOT NULL,
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
    transferred_to INTEGER,
    transferred_at DATETIME,
    is_update INTEGER DEFAULT 0,
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
CREATE UNIQUE INDEX IF NOT EXISTS idx_event_templates_name ON event_templates(user_id, name);
CREATE TABLE IF NOT EXISTS template_pins (
    user_id INTEGER NOT NULL,
    template_id INTEGER NOT NULL,
    PRIMARY KEY (user_id, template_id)
);
CREATE TABLE IF NOT EXISTS delivery_failures (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    activity_id TEXT NOT NULL,
    org_id INTEGER NOT NULL,
    inbox_url TEXT NOT NULL,
    activity_json TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 1,
    last_error TEXT,
    next_attempt_at INTEGER NOT NULL,
    UNIQUE(activity_id, org_id, inbox_url)
);
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY);
`); err != nil {
		log.Fatalf("init db schema: %v", err)
	}

	// Migration v1: add description/image_url/tags to federated_events and
	// transfer/update tracking columns to delivered.
	// Safety-net ALTERs run unconditionally (idempotent on existing columns);
	// the migration record prevents re-running expensive operations in future.
	if !migrationApplied(db, 1) {
		db.Exec("ALTER TABLE federated_events ADD COLUMN description TEXT")
		db.Exec("ALTER TABLE federated_events ADD COLUMN image_url TEXT")
		db.Exec("ALTER TABLE federated_events ADD COLUMN tags TEXT")
		db.Exec("ALTER TABLE delivered ADD COLUMN transferred_to INTEGER")
		db.Exec("ALTER TABLE delivered ADD COLUMN transferred_at DATETIME")
		db.Exec("ALTER TABLE delivered ADD COLUMN is_update INTEGER DEFAULT 0")
		db.Exec("INSERT OR IGNORE INTO schema_migrations VALUES (1)")
	}

	// Migration v2: city alias table for folkdance.page enrichment
	if !migrationApplied(db, 2) {
		db.Exec(`CREATE TABLE IF NOT EXISTS city_aliases (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			alias     TEXT    NOT NULL COLLATE NOCASE,
			canonical TEXT    NOT NULL,
			UNIQUE(alias)
		)`)
		// Pre-seed common English↔German (and a few other European) pairs.
		seeds := [][2]string{
			{"Cologne", "Köln"},
			{"Munich", "München"},
			{"Nuremberg", "Nürnberg"},
			{"Vienna", "Wien"},
			{"Ghent", "Gent"},
			{"Bruges", "Brugge"},
			{"Brussels", "Bruxelles"},
			{"Antwerp", "Antwerpen"},
			{"The Hague", "Den Haag"},
			{"Gothenburg", "Göteborg"},
			{"Zurich", "Zürich"},
			{"Berne", "Bern"},
			{"Basle", "Basel"},
			{"Freiburg", "Freiburg im Breisgau"},
		}
		for _, s := range seeds {
			db.Exec("INSERT OR IGNORE INTO city_aliases(alias, canonical) VALUES(?,?)", s[0], s[1])
		}
		db.Exec("INSERT OR IGNORE INTO schema_migrations VALUES (2)")
	}

	return db
}

type ActorRecord struct {
	ID            int
	OrgID         int
	OrgSlug       string
	PublicKeyPEM  string
	PrivateKeyPEM string
}

func getActorBySlug(db *sql.DB, slug string) (*ActorRecord, error) {
	var a ActorRecord
	var storedKey string
	err := db.QueryRow(
		"SELECT id, org_id, org_slug, public_key_pem, private_key_pem FROM actors WHERE org_slug = ?",
		slug,
	).Scan(&a.ID, &a.OrgID, &a.OrgSlug, &a.PublicKeyPEM, &storedKey)
	if err != nil {
		return nil, err
	}
	a.PrivateKeyPEM, err = actorKeyDecrypt(storedKey)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func getActorByOrgID(db *sql.DB, orgID int) (*ActorRecord, error) {
	var a ActorRecord
	var storedKey string
	err := db.QueryRow(
		"SELECT id, org_id, org_slug, public_key_pem, private_key_pem FROM actors WHERE org_id = ?",
		orgID,
	).Scan(&a.ID, &a.OrgID, &a.OrgSlug, &a.PublicKeyPEM, &storedKey)
	if err != nil {
		return nil, err
	}
	a.PrivateKeyPEM, err = actorKeyDecrypt(storedKey)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ensureRelayActor creates (or fetches) the special relay actor with org_id=0.
func ensureRelayActor(db *sql.DB, relayName string) (*ActorRecord, error) {
	return ensureActor(db, 0, relayName)
}

func ensureActor(db *sql.DB, orgID int, orgSlug string) (*ActorRecord, error) {
	a, err := getActorByOrgID(db, orgID)
	if err == nil {
		// Check if slug has changed - this indicates an actor rename
		if a.OrgSlug != orgSlug {
			// Migrate to new slug while preserving cryptographic keys
			_, err = db.Exec(
				"UPDATE actors SET org_slug = ? WHERE org_id = ?",
				orgSlug, orgID,
			)
			if err != nil {
				return nil, err
			}
			// Return updated actor record
			return getActorByOrgID(db, orgID)
		}
		return a, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	pub, priv, err := generateRSAKeyPair()
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(
		"INSERT INTO actors (org_id, org_slug, public_key_pem, private_key_pem) VALUES (?, ?, ?, ?)",
		orgID, orgSlug, pub, actorKeyEncrypt(priv),
	)
	if err != nil {
		return nil, err
	}
	return getActorByOrgID(db, orgID)
}

// ensureActorWithMove is like ensureActor but also sends ActivityPub Move activities
// when the actor slug changes. Use this when you want followers to be notified.
func ensureActorWithMove(cfg *Config, db *sql.DB, orgID int, orgSlug string) (*ActorRecord, error) {
	a, err := getActorByOrgID(db, orgID)
	if err == nil {
		// Check if slug has changed - this indicates an actor rename
		if a.OrgSlug != orgSlug {
			oldSlug := a.OrgSlug
			// Migrate to new slug while preserving cryptographic keys
			_, err = db.Exec(
				"UPDATE actors SET org_slug = ? WHERE org_id = ?",
				orgSlug, orgID,
			)
			if err != nil {
				return nil, err
			}
			// Send Move activities to inform followers about the rename
			go deliverActorMove(cfg, db, oldSlug, orgSlug, orgID)
			// Return updated actor record
			return getActorByOrgID(db, orgID)
		}
		return a, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	pub, priv, err := generateRSAKeyPair()
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(
		"INSERT INTO actors (org_id, org_slug, public_key_pem, private_key_pem) VALUES (?, ?, ?, ?)",
		orgID, orgSlug, pub, actorKeyEncrypt(priv),
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

// markDeliveredWithType records delivery with specific type (update/transfer)
func markDeliveredWithType(db *sql.DB, eventID, orgID int, isUpdate bool) error {
	_, err := db.Exec(
		"INSERT OR IGNORE INTO delivered (event_id, org_id, is_update) VALUES (?, ?, ?)",
		eventID, orgID, isUpdate,
	)
	return err
}

// markEventTransferred updates delivery record to indicate transfer
func markEventTransferred(db *sql.DB, eventID, oldOrgID, newOrgID int) error {
	_, err := db.Exec(
		`UPDATE delivered SET transferred_to = ?, transferred_at = CURRENT_TIMESTAMP 
		 WHERE event_id = ? AND org_id = ?`,
		newOrgID, eventID, oldOrgID,
	)
	return err
}

// getEventDeliveryHistory gets delivery records for an event
func getEventDeliveryHistory(db *sql.DB, eventID int) ([]struct {
	OrgID         int
	DeliveredAt   string
	TransferredTo *int
	TransferredAt *string
	IsUpdate      bool
}, error) {
	rows, err := db.Query(
		`SELECT org_id, delivered_at, transferred_to, transferred_at, is_update 
		 FROM delivered WHERE event_id = ? ORDER BY delivered_at`,
		eventID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []struct {
		OrgID         int
		DeliveredAt   string
		TransferredTo *int
		TransferredAt *string
		IsUpdate      bool
	}

	for rows.Next() {
		var h struct {
			OrgID         int
			DeliveredAt   string
			TransferredTo *int
			TransferredAt *string
			IsUpdate      bool
		}
		var transferredTo sql.NullInt64
		var transferredAt sql.NullString

		if err := rows.Scan(&h.OrgID, &h.DeliveredAt, &transferredTo, &transferredAt, &h.IsUpdate); err != nil {
			return nil, err
		}

		if transferredTo.Valid {
			val := int(transferredTo.Int64)
			h.TransferredTo = &val
		}
		if transferredAt.Valid {
			h.TransferredAt = &transferredAt.String
		}

		history = append(history, h)
	}

	return history, nil
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

// getFederatedEventActor returns the actor_id stored for the given AP object ID,
// or sql.ErrNoRows when the event is not in the database.
func getFederatedEventActor(db *sql.DB, apID string) (string, error) {
	var actorID string
	err := db.QueryRow("SELECT actor_id FROM federated_events WHERE ap_id=?", apID).Scan(&actorID)
	return actorID, err
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

// listTemplatesForOrg returns every preset scoped to orgID, regardless of who
// created it — unlike listTemplates' owner-or-member rule, any member
// browsing /admin/organization/{slug} should see all of the org's presets.
func listTemplatesForOrg(db *sql.DB, orgID int) ([]EventTemplate, error) {
	rows, err := db.Query(
		"SELECT id, user_id, org_id, name, data, created_at FROM event_templates WHERE org_id = ? ORDER BY name",
		orgID,
	)
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

// pinTemplate/unpinTemplate/listPinnedTemplateIDs implement per-user dashboard
// pins — strictly scoped to userID, independent of who owns or can edit the
// template itself. Pinning a template you can only read (org-scoped, not
// yours) is fine; the pin is just a personal bookmark.
func pinTemplate(db *sql.DB, userID, templateID int) error {
	_, err := db.Exec("INSERT OR IGNORE INTO template_pins (user_id, template_id) VALUES (?, ?)", userID, templateID)
	return err
}

func unpinTemplate(db *sql.DB, userID, templateID int) error {
	_, err := db.Exec("DELETE FROM template_pins WHERE user_id = ? AND template_id = ?", userID, templateID)
	return err
}

func listPinnedTemplateIDs(db *sql.DB, userID int) (map[int]bool, error) {
	rows, err := db.Query("SELECT template_id FROM template_pins WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make(map[int]bool)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, nil
}

// listPinnedTemplates returns the caller's pinned templates, restricted to
// ones they still have access to (own or org member) — a pin to a template
// whose org access was later revoked silently drops off the dashboard rather
// than erroring.
func listPinnedTemplates(db *sql.DB, userID int, orgIDs []int) ([]EventTemplate, error) {
	accessible, err := listTemplates(db, userID, orgIDs)
	if err != nil {
		return nil, err
	}
	pinned, err := listPinnedTemplateIDs(db, userID)
	if err != nil {
		return nil, err
	}
	var out []EventTemplate
	for _, t := range accessible {
		if pinned[t.ID] {
			out = append(out, t)
		}
	}
	return out, nil
}

// DeliveryFailure holds a record of a failed ActivityPub inbox POST for retry.
type DeliveryFailure struct {
	ActivityID   string
	OrgID        int
	InboxURL     string
	ActivityJSON string
	Attempts     int
	LastError    string
	NextAttempt  int64
}

// insertDeliveryFailure records a new per-follower delivery failure.
// Uses INSERT OR IGNORE so repeated calls for the same (activity, org, inbox)
// do not reset the attempt counter set by retries.
func insertDeliveryFailure(db *sql.DB, activityID string, orgID int, inboxURL, activityJSON, lastError string, nextAttempt int64) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO delivery_failures
		(activity_id, org_id, inbox_url, activity_json, attempts, last_error, next_attempt_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)`,
		activityID, orgID, inboxURL, activityJSON, lastError, nextAttempt,
	)
	return err
}

// updateDeliveryFailure updates attempt count, error, and next retry time for an existing record.
func updateDeliveryFailure(db *sql.DB, activityID string, orgID int, inboxURL, lastError string, attempts int, nextAttempt int64) error {
	_, err := db.Exec(
		`UPDATE delivery_failures SET attempts=?, last_error=?, next_attempt_at=?
		 WHERE activity_id=? AND org_id=? AND inbox_url=?`,
		attempts, lastError, nextAttempt, activityID, orgID, inboxURL,
	)
	return err
}

// deleteDeliveryFailure removes a failure record after successful delivery.
func deleteDeliveryFailure(db *sql.DB, activityID string, orgID int, inboxURL string) {
	db.Exec(
		"DELETE FROM delivery_failures WHERE activity_id=? AND org_id=? AND inbox_url=?",
		activityID, orgID, inboxURL,
	)
}

// pendingDeliveryFailures returns up to 100 failure records whose retry time has passed.
func pendingDeliveryFailures(db *sql.DB, nowUnix int64) ([]DeliveryFailure, error) {
	rows, err := db.Query(`
		SELECT activity_id, org_id, inbox_url, activity_json, attempts, COALESCE(last_error, '')
		FROM delivery_failures WHERE next_attempt_at <= ?
		ORDER BY next_attempt_at LIMIT 100`,
		nowUnix,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliveryFailure
	for rows.Next() {
		var f DeliveryFailure
		if err := rows.Scan(&f.ActivityID, &f.OrgID, &f.InboxURL, &f.ActivityJSON, &f.Attempts, &f.LastError); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
