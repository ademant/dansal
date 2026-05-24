# Database Schema Optimization

## Issue: Improve SQLite Schema and Indexes for Better Performance

### Description
The current SQLite schema can be optimized to improve query performance, especially for common REST API patterns. This includes adding missing indexes, optimizing data types, and improving query efficiency.

### Current Problems
1. Missing composite indexes for common query patterns
2. Suboptimal indexing for geospatial queries
3. Some queries likely perform full table scans
4. Opportunity to use SQLite 3.23+ features (BOOLEAN type, generated columns)

### Proposed Solutions

#### 1. Add Missing Indexes
```sql
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
```

#### 2. Schema Improvements
- Consider normalizing large text fields from events table
- Use BOOLEAN type for boolean columns (SQLite 3.23+)
- Add generated columns for common computations

#### 3. Query Optimization
- Implement keyset pagination instead of LIMIT/OFFSET for event listing
- Use covering indexes for common API queries
- Consider FTS5 for full-text search on event titles/descriptions

### Expected Benefits
- Faster event listing and filtering
- Improved geospatial query performance
- Better organization-based queries
- Reduced I/O for common operations

### Implementation Plan
1. Add new indexes to database initialization
2. Update migration scripts for existing installations
3. Test performance with large datasets
4. Consider adding PRAGMA optimizations for production

### Testing
- Verify all existing queries still work
- Test performance with 10K+ events
- Ensure backward compatibility
