# Dansal-Web Data Storage Analysis and Optimization

## Current Situation Analysis

### Data Stored in Dansal-Web Database (`web.db`)

1. **Actors Table** - Essential for ActivityPub federation
   - Stores cryptographic keys for organizations
   - Required for signing/verifying ActivityPub messages
   - `org_id`, `org_slug`, public/private keys

2. **Followers Table** - Essential for federation
   - Tracks who follows which organizations
   - Required for delivering updates via ActivityPub
   - `org_id`, `actor_uri`, `inbox_url`

3. **Delivered Table** - Operational data
   - Tracks which events have been delivered to which followers
   - Prevents duplicate deliveries
   - Could potentially be minimized or moved

4. **Follows Table** - Federation relationships
   - Tracks which actors this instance follows
   - Required for federation functionality

5. **Federated Events Table** - **POTENTIAL OPTIMIZATION TARGET**
   - Stores copies of events received via ActivityPub
   - Contains: `ap_id`, `actor_id`, `name`, `start_time`, `end_time`, `url`, `location_name`, `description`, `image_url`, `tags`, `raw_json`, `received_at`
   - **Issue**: This duplicates event data that should be stored in main dansal database

6. **Site Settings & Event Templates** - UI/configuration data
   - Minimal storage, not a concern

### Data Stored in Main Dansal Database (`calendar.db`)

- **Events**: Complete event information with all details
- **Organizations**: Organization information
- **Locations, Musicians, etc.**: All related entities
- **All essential business data**

## Optimization Recommendations

### 1. Federated Events Table - CRITICAL OPTIMIZATION

**Current Problem:**
- Dansal-web stores complete copies of federated events
- This duplicates data that should be in the main dansal database
- Violates the principle that "all essential information should be stored at dansal"

**Recommended Solution:**
```
# Option A: Store only references in dansal-web
ALTER TABLE federated_events DROP COLUMN name;
ALTER TABLE federated_events DROP COLUMN start_time;
ALTER TABLE federated_events DROP COLUMN end_time;
ALTER TABLE federated_events DROP COLUMN url;
ALTER TABLE federated_events DROP COLUMN location_name;
ALTER TABLE federated_events DROP COLUMN description;
ALTER TABLE federated_events DROP COLUMN image_url;
ALTER TABLE federated_events DROP COLUMN tags;

# Keep only:
# - ap_id (ActivityPub ID - unique identifier)
# - actor_id (who created it)
# - received_at (when we received it)
# - dansal_event_id (reference to main database)
# - raw_json (optional, for debugging)

ALTER TABLE federated_events ADD COLUMN dansal_event_id INTEGER;
```

**Implementation Plan:**
1. When dansal-web receives an ActivityPub event:
   - Extract essential data (AP ID, actor ID, received timestamp)
   - Forward complete event data to dansal API for storage
   - Store only reference in dansal-web database
   - Use dansal API to retrieve full event details when needed

### 2. Delivered Table - POTENTIAL OPTIMIZATION

**Current Problem:**
- Stores delivery status for event-org combinations
- Could grow large over time

**Recommended Solution:**
- Add automatic cleanup of old delivery records
- Consider moving to dansal main database if it becomes performance-critical

### 3. API Integration Strategy

**Recommended Approach:**
```
# Dansal-web should:
1. Receive ActivityPub messages
2. Extract minimal metadata (AP ID, actor, timestamp)
3. Forward complete event to dansal API: POST /api/v1/events
4. Store only reference: INSERT INTO federated_events (ap_id, actor_id, dansal_event_id, received_at)
5. Use dansal API for all event data retrieval

# Benefits:
- Single source of truth (dansal database)
- No data duplication
- Consistent data across both components
- Easier backup and maintenance
```

## Implementation Steps

### Phase 1: Database Schema Changes
1. Modify `federated_events` table to remove duplicated data
2. Add `dansal_event_id` foreign key reference
3. Create migration script for existing installations

### Phase 2: Code Changes in dansal-web
1. Modify ActivityPub event reception handler
2. Add dansal API client for event forwarding
3. Update event retrieval to use dansal API
4. Add error handling for dansal API failures

### Phase 3: Data Migration
1. For existing federated events:
   - Forward to dansal API if not already present
   - Update with dansal_event_id reference
   - Clean up duplicated data

## Expected Benefits

1. **Data Consistency**: Single source of truth in dansal
2. **Storage Efficiency**: Eliminate data duplication
3. **Maintenance**: Simpler backup and database management
4. **Performance**: Reduced storage requirements in dansal-web
5. **Reliability**: Atomic operations through API transactions

## Backward Compatibility

- Existing functionality preserved
- Gradual migration possible
- Fallback mechanisms can be implemented
- No breaking changes to external interfaces

## Monitoring and Validation

- Add metrics to track federated event storage
- Monitor dansal API usage from dansal-web
- Validate data consistency between components
- Performance testing with large federated event volumes

This optimization ensures that "all essential information is stored at dansal" while maintaining the necessary federation capabilities in dansal-web.