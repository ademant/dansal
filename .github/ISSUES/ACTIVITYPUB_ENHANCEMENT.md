# ActivityPub Federation Enhancement

## Issue
Improve ActivityPub integration for better event federation and compatibility with the fediverse.

## Current State
- Basic ActivityPub support is implemented
- Events are published to fediverse
- Limited actor types and interaction support
- Basic WebFinger implementation

## Proposed Improvements

### 1. Enhanced Federation
- **Better event announcements**: Richer event data in ActivityPub format
- **RSVP support**: Allow federated event responses
- **Event updates**: Proper handling of event changes
- **Event cancellations**: Federated cancellation notices

### 2. Actor Improvements
- **Organization actors**: Each organization gets its own actor
- **Location actors**: Optional actors for venues
- **Musician actors**: For bands/artists (linked to MusicBrainz)
- **Better actor profiles**: Richer metadata and links

### 3. WebFinger Enhancements
- **Multiple identifier formats**: Support more username formats
- **Alias support**: Multiple WebFinger identifiers per actor
- **Discovery improvements**: Better actor discovery
- **Redirect handling**: Proper handling of moved actors

### 4. Interaction Support
- **Comments**: Federated event comments
- **Likes/Reactions**: Support for federated reactions
- **Shares/Boosts**: Allow event sharing across instances
- **Mentions**: Proper @mention handling

### 5. Compatibility Improvements
- **Mastodon compatibility**: Better Mastodon integration
- **Pleroma compatibility**: Support Pleroma-specific features
- **Pixelfed compatibility**: For image-focused instances
- **PeerTube compatibility**: For video event streams

## Technical Implementation

### Backend Changes
```go
// Enhanced ActivityPub event structure
type ActivityPubEvent struct {
    BaseEvent      // Existing fields
    AttributedTo   []string `json:"attributedTo,omitempty"`  // Musicians
    Location       *APLocation `json:"location,omitempty"`    // Enhanced location
    StartTime      time.Time `json:"startTime,omitempty"`     // ISO8601
    EndTime        time.Time `json:"endTime,omitempty"`       // ISO8601
    Duration       string    `json:"duration,omitempty"`      // PnYnMnDTnHnMnS
    Image          *APImage  `json:"image,omitempty"`         // Enhanced images
    Icon          *APImage  `json:"icon,omitempty"`          // Event icon
    Category      []string  `json:"category,omitempty"`      // Event categories
    Tags          []APTag   `json:"tag,omitempty"`            // Enhanced tags
}

// Organization actor support
func (s *Server) GetOrganizationActor(w http.ResponseWriter, r *http.Request, orgID int) {
    org, err := s.db.GetOrganization(orgID)
    if err != nil {
        http.Error(w, "Organization not found", http.StatusNotFound)
        return
    }
    
    actor := s.createActorFromOrganization(org)
    render.JSON(w, r, actor)
}

// Enhanced WebFinger with aliases
func (s *Server) WebFingerHandler(w http.ResponseWriter, r *http.Request) {
    resource := r.URL.Query().Get("resource")
    
    // Support multiple formats
    if strings.HasPrefix(resource, "acct:") {
        resource = strings.TrimPrefix(resource, "acct:")
    }
    
    // Check for aliases
    if alias, exists := s.aliases[resource]; exists {
        resource = alias
    }
    
    // Existing WebFinger logic
    // ...
}
```

### Configuration Additions
```yaml
activitypub:
  # Federation settings
  enabled: true
  domain: "yourdomain.example.com"
  actor_types: ["person", "organization", "location"]
  
  # Compatibility settings
  mastodon_compatible: true
  pleroma_compatible: true
  pixelfed_compatible: true
  peertube_compatible: true
  
  # Federation limits
  max_federated_events: 1000
  federation_timeout: 30
  
  # WebFinger settings
  webfinger_aliases:
    "admin": "main-admin"
    "old-org": "new-org-name"
```

## Acceptance Criteria
- [ ] Organization actors work and can be discovered via WebFinger
- [ ] Events include rich metadata in ActivityPub format
- [ ] RSVP and reactions are properly federated
- [ ] Event updates and cancellations propagate correctly
- [ ] Compatible with Mastodon, Pleroma, and Pixelfed
- [ ] WebFinger supports multiple identifier formats
- [ ] All ActivityPub endpoints pass validation

## Priority
Medium - Important for fediverse integration but not critical for core functionality

## Dependencies
- None (can be implemented independently)

## Estimated Effort
Large (6-10 days)

## Related Issues
- None currently

## Testing Requirements
- Test with multiple ActivityPub implementations
- Verify WebFinger compatibility with different clients
- Test event lifecycle (create, update, cancel)
- Verify media attachment handling

## Notes
- Follow ActivityPub specification closely
- Monitor federation logs for compatibility issues
- Consider implementing ActivityPub test suite
- Document federation capabilities for instance admins