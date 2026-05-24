-- Migration: Add ActivityPub Federation Enhancements
-- This migration adds support for RSVP, event updates, interactions, and actor improvements

BEGIN TRANSACTION;

-- Step 1: Create schema_migrations table if it doesn't exist
CREATE TABLE IF NOT EXISTS schema_migrations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    migration_name TEXT UNIQUE NOT NULL,
    applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Step 2: Add RSVP support table
CREATE TABLE IF NOT EXISTS rsvps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ap_id TEXT UNIQUE NOT NULL,
    event_ap_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    rsvp_type TEXT NOT NULL, -- "Yes", "No", "Maybe", "Interested"
    status TEXT NOT NULL DEFAULT 'pending', -- pending, accepted, rejected
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (event_ap_id) REFERENCES federated_events(ap_id)
);

-- Step 3: Add interactions table for likes, comments, shares
CREATE TABLE IF NOT EXISTS interactions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ap_id TEXT UNIQUE NOT NULL,
    target_type TEXT NOT NULL, -- "Event", "Comment", "RSVP"
    target_id TEXT NOT NULL, -- AP ID of the target
    actor_id TEXT NOT NULL,
    interaction_type TEXT NOT NULL, -- "Like", "Dislike", "Announce", "Comment"
    content TEXT, -- For comments
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (target_id) REFERENCES federated_events(ap_id)
);

-- Step 4: Add location_actors table
CREATE TABLE IF NOT EXISTS location_actors (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    address TEXT,
    latitude REAL,
    longitude REAL,
    public_key_pem TEXT NOT NULL,
    private_key_pem TEXT NOT NULL,
    public_key_ed25519_pem TEXT,
    private_key_ed25519_pem TEXT,
    public_key_multibase TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Step 5: Add musician_actors table with MusicBrainz support
CREATE TABLE IF NOT EXISTS musician_actors (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL,
    musicbrainz_id TEXT, -- MusicBrainz artist ID
    description TEXT,
    image_url TEXT,
    public_key_pem TEXT NOT NULL,
    private_key_pem TEXT NOT NULL,
    public_key_ed25519_pem TEXT,
    private_key_ed25519_pem TEXT,
    public_key_multibase TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Step 6: Add webfinger_aliases table for multiple identifier support
CREATE TABLE IF NOT EXISTS webfinger_aliases (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    alias TEXT UNIQUE NOT NULL,
    target_type TEXT NOT NULL, -- "organization", "location", "musician"
    target_id INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Step 7: Add event_updates table for tracking event changes
CREATE TABLE IF NOT EXISTS event_updates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_ap_id TEXT NOT NULL,
    update_type TEXT NOT NULL, -- "Update", "Delete"
    update_ap_id TEXT, -- AP ID of the update activity
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (event_ap_id) REFERENCES federated_events(ap_id)
);

-- Step 8: Add indexes for performance
CREATE INDEX IF NOT EXISTS idx_rsvps_event_ap_id ON rsvps(event_ap_id);
CREATE INDEX IF NOT EXISTS idx_rsvps_actor_id ON rsvps(actor_id);
CREATE INDEX IF NOT EXISTS idx_interactions_target ON interactions(target_id);
CREATE INDEX IF NOT EXISTS idx_interactions_actor ON interactions(actor_id);
CREATE INDEX IF NOT EXISTS idx_webfinger_aliases ON webfinger_aliases(alias);
CREATE INDEX IF NOT EXISTS idx_event_updates_event ON event_updates(event_ap_id);

-- Step 9: Record that this migration was applied
INSERT OR IGNORE INTO schema_migrations (migration_name) VALUES ('003_add_activitypub_enhancements');

COMMIT;