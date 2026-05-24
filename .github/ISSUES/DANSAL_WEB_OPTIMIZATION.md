# Dansal-Web Data Storage Optimization

## Issue: Minimize Data Duplication Between Dansal and Dansal-Web

### Description
Currently, dansal-web stores complete copies of federated events in its database, duplicating data that should be stored in the main dansal database. This violates the architectural principle that "all essential information should be stored at dansal" and creates data consistency challenges.

### Current Problems

1. **Data Duplication**: Federated events are stored in both databases
2. **Consistency Risks**: Updates in one database may not be reflected in the other
3. **Storage Inefficiency**: Wasted disk space and memory
4. **Maintenance Complexity**: Backup and migration challenges
5. **Performance Impact**: Larger database size affects query performance

### Proposed Solution

**Optimize federated_events table to store only references:**

1. **Remove duplicated event data** from dansal-web:
   - `name`, `start_time`, `end_time`, `url`, `location_name`, `description`, `image_url`, `tags`

2. **Add reference to main database:**
   - `dansal_event_id` - Foreign key to events table in main dansal database

3. **Implementation strategy:**
   - Dansal-web receives ActivityPub events
   - Extracts minimal metadata (AP ID, actor ID, timestamp)
   - Forwards complete event to dansal API for storage
   - Stores only reference in dansal-web database
   - Uses dansal API to retrieve full event details when needed

### Expected Benefits

1. **Single Source of Truth**: All event data in main dansal database
2. **Eliminated Duplication**: No redundant event storage
3. **Improved Consistency**: Atomic updates through API
4. **Better Performance**: Smaller database, faster queries
5. **Simpler Maintenance**: Easier backups and migrations
6. **Scalability**: Better handling of large federated event volumes

### Implementation Plan

**Phase 1: Database Schema Changes**
- Modify `federated_events` table structure
- Add `dansal_event_id` column
- Create migration for existing installations

**Phase 2: Code Changes**
- Modify ActivityPub event reception handler
- Add dansal API integration for event forwarding
- Update event retrieval to use dansal API
- Add proper error handling

**Phase 3: Data Migration**
- Migrate existing federated events to new structure
- Forward events to dansal API if not already present
- Update references and clean up old data

### Backward Compatibility

- Existing functionality preserved during transition
- Gradual migration possible
- Fallback mechanisms can be implemented
- No breaking changes to external interfaces

### Testing Requirements

- Verify event forwarding to dansal API works correctly
- Test reference-based event retrieval
- Validate data consistency between components
- Performance testing with various event volumes
- Error handling and recovery testing

### Risk Assessment

**Low Risk:**
- Changes are additive and reversible
- Existing data preserved during migration
- Fallback to current behavior possible
- No impact on external federation interfaces

### Success Criteria

1. ✅ No event data duplicated between databases
2. ✅ All federated events stored in main dansal database
3. ✅ Dansal-web stores only references
4. ✅ Event retrieval works correctly via API
5. ✅ Performance improved or maintained
6. ✅ Data consistency verified

This optimization ensures architectural compliance while maintaining full federation functionality.