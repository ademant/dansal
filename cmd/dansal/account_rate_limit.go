package main

import (
	"net/http"
	"sync"
	"time"
)

// accountLimiter is an in-memory per-account rate limiter keyed by user ID.
// It tracks requests within a fixed sliding window and rejects once the limit
// is reached. Unlike the IP-based RateLimitMiddleware, this applies per
// authenticated account regardless of source IP — so a compromised account
// calling the API directly is capped the same as one using the web UI.
type accountLimiter struct {
	mu      sync.Mutex
	counts  map[int]int
	windows map[int]time.Time
	limit   int
	period  time.Duration
}

func newAccountLimiter(limit int, period time.Duration) *accountLimiter {
	return &accountLimiter{
		counts:  make(map[int]int),
		windows: make(map[int]time.Time),
		limit:   limit,
		period:  period,
	}
}

func (l *accountLimiter) Allow(userID int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if t, ok := l.windows[userID]; !ok || now.Sub(t) > l.period {
		l.counts[userID] = 1
		l.windows[userID] = now
		return true
	}
	l.counts[userID]++
	return l.counts[userID] <= l.limit
}

// createUpdateLimiter caps authenticated create/update mutations per account
// per minute. Bulk import and fetch-source paths are excluded — they have
// separate, already-reviewed flows.
var createUpdateLimiter = newAccountLimiter(30, time.Minute)

// accountMutationLimit rejects create/update requests once the per-account
// mutation ceiling is reached. Applied on top of auth() for individual-item
// create and update endpoints.
func accountMutationLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		callerID, _ := callerFromRequest(r)
		if callerID > 0 && !createUpdateLimiter.Allow(callerID) {
			writeError(w, "Rate limit exceeded for this account. Please slow down.", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}
