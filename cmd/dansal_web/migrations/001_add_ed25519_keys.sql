-- Migration to add Ed25519 key columns to actors table
-- This migration adds support for modern ActivityPub key formats
-- Required for WebFinger implementation to work correctly

-- Add the missing columns if they don't exist
ALTER TABLE actors ADD COLUMN IF NOT EXISTS public_key_ed25519_pem TEXT;
ALTER TABLE actors ADD COLUMN IF NOT EXISTS private_key_ed25519_pem TEXT;
ALTER TABLE actors ADD COLUMN IF NOT EXISTS public_key_multibase TEXT;

-- Create a function to apply this migration
CREATE TABLE IF NOT EXISTS schema_migrations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    migration_name TEXT UNIQUE NOT NULL,
    applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Insert migration record
INSERT OR IGNORE INTO schema_migrations (migration_name) VALUES ('001_add_ed25519_keys');