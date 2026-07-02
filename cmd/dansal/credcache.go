package main

import (
	"sync"
	"time"
)

// credCacheTTL caps how long a validated credential stays cached.
// Role changes and API key deletions take effect within this window.
const credCacheTTL = 5 * time.Minute

type credEntry struct {
	userID    int
	userRole  string
	tokenID   int // 0 for API keys
	expiresAt time.Time
	pinnedIP  string // non-empty: credential is only valid when presented from this IP
}

type credCache struct {
	mu      sync.RWMutex
	entries map[string]credEntry
}

func newCredCache() *credCache {
	c := &credCache{entries: make(map[string]credEntry)}
	go c.sweepLoop()
	return c
}

// credentials is the process-global auth cache shared by token and API key validation.
var credentials = newCredCache()

// get returns a cached entry if it exists, has not expired, and — for
// IP-pinned credentials — was presented from the IP it was pinned to.
func (c *credCache) get(key, currentIP string) (userID int, role string, tokenID int, ok bool) {
	c.mu.RLock()
	e, found := c.entries[key]
	c.mu.RUnlock()
	if !found || time.Now().After(e.expiresAt) {
		return 0, "", 0, false
	}
	if e.pinnedIP != "" && e.pinnedIP != currentIP {
		return 0, "", 0, false
	}
	return e.userID, e.userRole, e.tokenID, true
}

// set stores a credential. tokenExpiry is the real token expiry from the DB;
// pass time.Time{} for API keys (no natural expiry) to use the TTL cap only.
// tokenID should be 0 for API keys. pinnedIP is empty for unpinned credentials.
func (c *credCache) set(key string, userID int, role string, tokenID int, tokenExpiry time.Time, pinnedIP string) {
	exp := time.Now().Add(credCacheTTL)
	if !tokenExpiry.IsZero() && tokenExpiry.Before(exp) {
		exp = tokenExpiry
	}
	c.mu.Lock()
	c.entries[key] = credEntry{userID: userID, userRole: role, tokenID: tokenID, expiresAt: exp, pinnedIP: pinnedIP}
	c.mu.Unlock()
}

// invalidate removes a single entry immediately (e.g. on API key deletion).
func (c *credCache) invalidate(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// pruneByUserID removes all cached entries for a user (e.g. when the account is disabled).
func (c *credCache) pruneByUserID(userID int) {
	c.mu.Lock()
	for k, e := range c.entries {
		if e.userID == userID {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

func (c *credCache) sweepLoop() {
	ticker := time.NewTicker(credCacheTTL)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		c.mu.Lock()
		for k, e := range c.entries {
			if now.After(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		c.mu.Unlock()
	}
}
