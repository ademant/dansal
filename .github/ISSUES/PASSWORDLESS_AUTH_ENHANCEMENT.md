# Passwordless Authentication Enhancement

## Issue
Enhance the existing passwordless authentication system to improve mobile user experience and add session persistence.

## Current State
- Magic link authentication via email is implemented
- Telegram and Matrix verification is prepared
- No session persistence for returning users
- No mobile-optimized authentication flow

## Proposed Improvements

### 1. Mobile Deep Linking
- Implement universal links for seamless mobile authentication
- Support both iOS and Android deep linking standards
- Auto-detect and open the app when available, fallback to browser

### 2. Session Persistence
- Add "Remember this device" option (30-day persistence)
- Implement biometric re-authentication for returning users
- Store refresh tokens securely with device binding

### 3. Guest Mode
- Allow temporary access without account creation
- Limited functionality (view-only by default)
- Optional account creation after using guest mode

### 4. Delivery Method Enhancements
- Email: Prominent call-to-action button with deep link
- Telegram: Instant login button in message
- Matrix: Formatted message with clickable link

## Technical Implementation

### Backend Changes Required
```go
// Add to auth handler
type AuthResponse struct {
    Token        string    `json:"token"`
    ExpiresAt    time.Time `json:"expires_at"`
    RefreshToken string    `json:"refresh_token,omitempty"` // New
    Persistent   bool      `json:"persistent,omitempty"`    // New
}

// Add device tracking
func (s *Server) createSession(userID int, persistent bool) (*Session, error) {
    // Generate tokens
    // If persistent, set longer expiration and create refresh token
    // Store device fingerprint if available
}
```

### Frontend Changes Required
- Update login page with persistent session checkbox
- Implement biometric authentication prompt
- Add guest mode entry point
- Mobile deep link detection and handling

### Configuration Additions
```yaml
server:
  # Add these new settings
  session_persistence_days: 30
  refresh_token_expiration_days: 90
  max_devices_per_user: 5
  guest_mode_enabled: true
```

## Acceptance Criteria
- [ ] Mobile users can authenticate with one tap using deep links
- [ ] Returning users can enable session persistence
- [ ] Biometric re-authentication works on supported devices
- [ ] Guest mode allows temporary access without account
- [ ] All delivery methods have optimized mobile experiences
- [ ] Security audit passed for new authentication flows

## Priority
High - This directly addresses the mobile user experience which is the primary use case

## Dependencies
- None (can be implemented independently)

## Estimated Effort
Medium (3-5 days)

## Related Issues
- None currently

## Notes
- Ensure backward compatibility with existing magic link system
- Security review required for session persistence implementation
- Consider GDPR implications for device tracking