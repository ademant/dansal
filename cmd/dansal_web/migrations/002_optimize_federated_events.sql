-- Migration: Optimize federated_events table to store only references
-- This migration removes duplicated event data and adds reference to main dansal database

BEGIN TRANSACTION;

-- Step 1: Add dansal_event_id column if it doesn't exist
CREATE TABLE IF NOT EXISTS schema_migrations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    migration_name TEXT UNIQUE NOT NULL,
    applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Step 2: Check if this migration has already been applied
-- (We'll do this in code to handle the complex logic)

-- Step 3: Create new optimized table structure
CREATE TABLE IF NOT EXISTS federated_events_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ap_id TEXT UNIQUE NOT NULL,
    actor_id TEXT NOT NULL,
    dansal_event_id INTEGER,
    received_at INTEGER NOT NULL,
    raw_json TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (dansal_event_id) REFERENCES events(id)
);

-- Step 4: Copy essential data from old table to new table
-- Note: This is a simplified version - actual migration would need to:
-- 1. Forward each event to dansal API to get dansal_event_id
-- 2. Handle cases where events already exist in dansal
-- 3. Preserve all existing relationships

INSERT INTO federated_events_new (id, ap_id, actor_id, received_at, raw_json, created_at)
SELECT id, ap_id, actor_id, received_at, raw_json, created_at 
FROM federated_events;

-- Step 5: Drop old table
DROP TABLE federated_events;

-- Step 6: Rename new table to original name
ALTER TABLE federated_events_new RENAME TO federated_events;

-- Step 7: Create indexes for performance
CREATE INDEX IF NOT EXISTS idx_federated_events_ap_id ON federated_events(ap_id);
CREATE INDEX IF NOT EXISTS idx_federated_events_actor_id ON federated_events(actor_id);
CREATE INDEX IF NOT EXISTS idx_federated_events_dansal_id ON federated_events(dansal_event_id);
CREATE INDEX IF NOT EXISTS idx_federated_events_received ON federated_events(received_at);

-- Step 8: Record that this migration was applied
INSERT OR IGNORE INTO schema_migrations (migration_name) VALUES ('002_optimize_federated_events');

COMMIT;