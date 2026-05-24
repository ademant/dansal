-- Migration: Add performance indexes for common query patterns
-- This migration adds indexes to improve performance of REST API endpoints

BEGIN TRANSACTION;

-- Composite index for event filtering (most common API pattern)
CREATE INDEX IF NOT EXISTS idx_events_filter ON events(is_published, start_time, end_time, location_id);

-- Geospatial index for location-based queries
CREATE INDEX IF NOT EXISTS idx_locations_geo ON locations(latitude, longitude) WHERE latitude IS NOT NULL AND longitude IS NOT NULL;

-- Organization-based event queries
CREATE INDEX IF NOT EXISTS idx_events_org_published ON events(organization_id, is_published, start_time) WHERE organization_id IS NOT NULL;

-- Tag search optimization
CREATE INDEX IF NOT EXISTS idx_events_tag_search ON event_tags(tag, event_id);

-- Musician and organization name searches
CREATE INDEX IF NOT EXISTS idx_musicians_name ON musicians(bandname);
CREATE INDEX IF NOT EXISTS idx_organizations_name ON organizations(name);

-- Create schema_migrations table if it doesn't exist
CREATE TABLE IF NOT EXISTS schema_migrations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    migration_name TEXT UNIQUE NOT NULL,
    applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Record that this migration was applied
INSERT OR IGNORE INTO schema_migrations (migration_name) VALUES ('001_add_performance_indexes');

COMMIT;