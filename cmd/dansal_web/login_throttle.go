package main

import (
	"sync"
	"time"
)

const loginMaxDelay = 32 * time.Second

// authBlock is the log prefix matched by the fail2ban filter.
const authBlock = "dansal-web: AUTH_BLOCK"

// authFail is the log prefix for individual failed login attempts.
const authFail = "dansal-web: AUTH_FAIL"

// publicBlock is the log prefix for public form endpoint rate limits.
const publicBlock = "dansal-web: PUBLIC_BLOCK"

type loginThrottle struct {
	mu          sync.Mutex
	entries     map[string]*throttleEntry
	maxFailures int
	window      time.Duration
}

type throttleEntry struct {
	failures    int
	windowStart time.Time
}

func newLoginThrottle(maxFailures int, window time.Duration) *loginThrottle {
	lt := &loginThrottle{
		entries:     make(map[string]*throttleEntry),
		maxFailures: maxFailures,
		window:      window,
	}
	go lt.sweep()
	return lt
}

// isBlocked returns true when the IP has hit the failure cap within the window.
func (lt *loginThrottle) isBlocked(ip string) bool {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	e := lt.entries[ip]
	return e != nil && time.Since(e.windowStart) <= lt.window && e.failures >= lt.maxFailures
}

// recordFailure increments the counter and returns the backoff delay to sleep.
// Delay is 2^(n-1) seconds: 1 s, 2 s, 4 s, 8 s, 16 s, capped at loginMaxDelay.
func (lt *loginThrottle) recordFailure(ip string) time.Duration {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	e := lt.entries[ip]
	if e == nil {
		e = &throttleEntry{windowStart: time.Now()}
		lt.entries[ip] = e
	} else if time.Since(e.windowStart) > lt.window {
		e.failures = 0
		e.windowStart = time.Now()
	}
	if e.failures < lt.maxFailures {
		e.failures++
	}
	delay := time.Duration(1<<uint(e.failures-1)) * time.Second
	if delay > loginMaxDelay {
		delay = loginMaxDelay
	}
	return delay
}

// reset clears the failure counter on a successful login.
func (lt *loginThrottle) reset(ip string) {
	lt.mu.Lock()
	delete(lt.entries, ip)
	lt.mu.Unlock()
}

func (lt *loginThrottle) sweep() {
	ticker := time.NewTicker(lt.window)
	defer ticker.Stop()
	for range ticker.C {
		lt.mu.Lock()
		now := time.Now()
		for ip, e := range lt.entries {
			if now.Sub(e.windowStart) > lt.window {
				delete(lt.entries, ip)
			}
		}
		lt.mu.Unlock()
	}
}
