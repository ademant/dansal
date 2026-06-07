# Respect Privacy-Related HTTP Headers

## Overview
Enhance Dansal's privacy posture by respecting standard privacy-related HTTP headers including DNT (Do Not Track), Global Privacy Control, and other privacy signals. This will improve compliance with privacy best practices and user expectations.

## Current State

### Privacy Header Handling
- **DNT (Do Not Track)**: Not currently respected
- **GPC (Global Privacy Control)**: Not currently respected  
- **Sec-GPC**: Not currently respected
- **Privacy preferences**: No header-based opt-out mechanism

### Current Tracking Behavior
- Session tracking via cookies (required for functionality)
- Basic access logging for all requests
- No explicit analytics or third-party tracking identified
- Language preference via cookies (could use headers)

## Proposed Enhancements

### 1. DNT (Do Not Track) Header

**Header**: `DNT: 1` or `DNT: yes`

**Implementation**:
```go
func shouldTrackRequest(r *http.Request) bool {
    // Check DNT header
    dnt := r.Header.Get("DNT")
    if dnt == "1" || strings.EqualFold(dnt, "yes") {
        return false
    }
    
    // Check Global Privacy Control
    gpc := r.Header.Get("Sec-GPC")
    if gpc == "1" {
        return false
    }
    
    return true // Tracking allowed
}
```

**Impact Areas**:
- **Logging**: Reduce detail in access logs for DNT users
- **Analytics**: Disable non-essential tracking
- **Cookies**: Only set essential cookies (auth/session)
- **Personalization**: Fall back to header-based preferences

### 2. Global Privacy Control (GPC)

**Header**: `Sec-GPC: 1`

**Implementation**:
- Treat as stronger signal than DNT
- Same opt-out behavior as DNT
- Legal recognition in some jurisdictions

### 3. Privacy Preference Signals

**Headers to Consider**:
- `Save-Data: on` - Indicate slow connection preference
- `Device-Memory: low` - Adapt to low-memory devices
- `Viewport-Width: small` - Responsive design hints

**Implementation**:
```go
func getPrivacyPreferences(r *http.Request) PrivacyPrefs {
    return PrivacyPrefs{
        DoNotTrack:    checkDNT(r),
        GlobalPrivacy:  checkGPC(r),
        SaveData:       r.Header.Get("Save-Data") == "on",
        LowMemory:      r.Header.Get("Device-Memory") == "low",
        SmallViewport:  r.Header.Get("Viewport-Width") == "small"
    }
}
```

### 4. Enhanced Cookie Management

**Current**: All or nothing cookie approach

**Proposed**: Tiered cookie system
```go
const (
    CookieEssential = iota // Required for functionality
    CookiePreferences       // User choices (language, etc.)
    CookieAnalytics        // Tracking/analytics
    CookieThirdParty        // External integrations
)

func canSetCookie(r *http.Request, cookieType int) bool {
    if cookieType == CookieEssential {
        return true // Always allowed
    }
    
    if checkDNT(r) || checkGPC(r) {
        return cookieType == CookiePreferences // Only preferences
    }
    
    return true // All cookies allowed
}
```

## Privacy Impact Assessment

### ✅ Benefits
1. **User Trust**: Respects explicit privacy preferences
2. **Compliance**: Aligns with GDPR/ePrivacy principles
3. **Transparency**: Clear privacy signaling
4. **Flexibility**: Users control their data

### ⚠️ Considerations
1. **Functionality**: Core features unaffected
2. **Logging**: Reduced detail for opt-out users
3. **Personalization**: Header-based fallbacks needed
4. **Complexity**: Minimal code changes required

## Implementation Plan

### Phase 1: Core Privacy Headers (2-3 days)
- [ ] Implement DNT header checking
- [ ] Implement GPC header checking
- [ ] Add logging level reduction for opt-out users
- [ ] Document privacy policy updates

### Phase 2: Enhanced Privacy (3-5 days)
- [ ] Tiered cookie system
- [ ] Privacy preference detection
- [ ] Adaptive content delivery
- [ ] Admin interface for privacy settings

### Phase 3: Monitoring & Compliance (2 days)
- [ ] Privacy header testing
- [ ] Compliance verification
- [ ] User education materials

## Success Criteria

1. ✅ DNT header properly detected and respected
2. ✅ GPC header properly detected and respected
3. ✅ Logging system adapts to privacy preferences
4. ✅ Cookie system respects opt-out signals
5. ✅ No regression in core functionality
6. ✅ Performance impact is negligible
7. ✅ Documentation updated

## Priority
**Medium-High** - Important for privacy compliance and user trust, but not urgent for core functionality.

## Estimated Effort
- **Total**: 7-10 days
- **Phase 1**: 2-3 days (core headers)
- **Phase 2**: 3-5 days (enhanced features)
- **Phase 3**: 2 days (testing/compliance)

## Dependencies
- Existing logging system
- Current cookie management
- Session handling code

## Acceptance Criteria
- [ ] DNT header detection works correctly
- [ ] GPC header detection works correctly
- [ ] Logging adapts to privacy preferences
- [ ] Cookie system respects opt-out signals
- [ ] No functional regression for opt-out users
- [ ] Performance benchmarks pass
- [ ] Privacy policy documentation updated
- [ ] Admin interface shows privacy status

## Related Issues
- #494 (Accept-Language header)
- Potential future privacy enhancements

## Technical Notes
- RFC 7231 defines DNT header format
- GPC is emerging standard with legal recognition
- Should handle missing/malformed headers gracefully
- Consider caching privacy preferences for performance
- Log opt-out status for compliance auditing