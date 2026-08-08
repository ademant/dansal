package main

import (
	"crypto/sha256"
	"encoding/hex"
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

// tokenBlock is the log prefix for GET-side token-issuance rate limit hits.
const tokenBlock = "dansal-web: TOKEN_BLOCK"

type loginThrottle struct {
	mu          sync.Mutex
	entries     map[string]*throttleEntry
	maxFailures int
	window      time.Duration
}

type throttleEntry struct {
	failures     int
	windowStart  time.Time
	lastEmail    string // email of the most recent failed attempt
	lastPassHash string // SHA-256 hex of the most recent failed password; never the plaintext
}

func hashLoginPassword(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

func newLoginThrottle(maxFailures int, window time.Duration) *loginThrottle {
	lt := &loginThrottle{
		entries:     make(map[string]*throttleEntry),
		maxFailures: maxFailures,
		window:      window,
	}
	if window > 0 {
		go lt.sweep()
	}
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
//
// A failed attempt that repeats the exact same email+password as the
// immediately preceding failure from this IP doesn't increment the counter
// or escalate the delay (still applies the current, frozen delay) — it
// doesn't help an actual credential-guessing attempt, which needs distinct
// guesses to make progress, so excluding exact repeats only helps the
// common "retried the same typo" case without weakening brute-force
// protection.
func (lt *loginThrottle) recordFailure(ip, email, password string) time.Duration {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	e := lt.entries[ip]
	if e == nil {
		e = &throttleEntry{windowStart: time.Now()}
		lt.entries[ip] = e
	} else if time.Since(e.windowStart) > lt.window {
		e.failures = 0
		e.windowStart = time.Now()
		e.lastEmail = ""
		e.lastPassHash = ""
	}
	passHash := hashLoginPassword(password)
	isRepeat := e.lastPassHash != "" && e.lastEmail == email && e.lastPassHash == passHash
	if !isRepeat && e.failures < lt.maxFailures {
		e.failures++
	}
	e.lastEmail = email
	e.lastPassHash = passHash
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
