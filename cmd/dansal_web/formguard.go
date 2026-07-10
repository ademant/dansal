package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// formHMACKey is generated once at startup; used to sign form timestamps.
var formHMACKey []byte

func init() {
	formHMACKey = make([]byte, 32)
	if _, err := rand.Read(formHMACKey); err != nil {
		log.Fatal("formguard: generate HMAC key: ", err)
	}
}

// newFormToken returns a "ts.mac" token to embed as a hidden field in HTML forms.
// On submission, validFormToken checks that the token is genuine and that the
// form was not submitted faster than the configured minimum time.
func newFormToken() string {
	ts := time.Now().Unix()
	return fmt.Sprintf("%d.%s", ts, formMAC(ts))
}

// validFormToken returns true if token has a valid HMAC and is at least minSecs old.
// When minSecs == 0 the age check is skipped (feature disabled).
func validFormToken(token string, minSecs int) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return minSecs == 0
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	if !hmac.Equal([]byte(parts[1]), []byte(formMAC(ts))) {
		return false
	}
	if minSecs == 0 {
		return true
	}
	return time.Now().Unix()-ts >= int64(minSecs)
}

func formMAC(ts int64) string {
	mac := hmac.New(sha256.New, formHMACKey)
	fmt.Fprintf(mac, "%d", ts)
	return hex.EncodeToString(mac.Sum(nil))
}

// ── One-time form tokens ──────────────────────────────────────────────────────
//
// issueFormToken generates a random one-time token stored server-side. The
// token is valid until consumeFormToken is called or it exceeds its max age.
// Optionally bound to the client IP when FormTokenBindIP is configured.
// Returns "" when the outstanding-token cap is reached (cap == 0 disables).

type oneTimeToken struct {
	createdAt time.Time
	ip        string
}

var oneTimeTokens sync.Map   // key: hex string, value: oneTimeToken
var outstandingTokens atomic.Int64
var formTokenCap int64 // set from config in main(); 0 = no cap

func issueFormToken(ip string) string {
	if formTokenCap > 0 && outstandingTokens.Load() >= formTokenCap {
		return "" // cap reached — caller shows load-shedding page
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	id := hex.EncodeToString(b)
	oneTimeTokens.Store(id, oneTimeToken{createdAt: time.Now(), ip: ip})
	outstandingTokens.Add(1)
	return id
}

// consumeFormToken validates and atomically deletes a token.
// minAge: token must be at least this old (0 = skip check).
// maxAge: token must not be older than this (0 = skip check).
// Returns false when the token is missing, too young, expired, or IP-mismatched.
func consumeFormToken(tokenID, ip string, minAge, maxAge time.Duration, bindIP bool) bool {
	if tokenID == "" {
		return false
	}
	val, loaded := oneTimeTokens.LoadAndDelete(tokenID)
	if !loaded {
		return false
	}
	outstandingTokens.Add(-1)
	tok := val.(oneTimeToken)
	age := time.Since(tok.createdAt)
	if minAge > 0 && age < minAge {
		return false
	}
	if maxAge > 0 && age > maxAge {
		return false
	}
	if bindIP && tok.ip != ip {
		return false
	}
	return true
}

// startFormTokenCleanup removes expired tokens every cleanupMins minutes.
func startFormTokenCleanup(maxAge time.Duration, cleanupMins int) {
	interval := time.Duration(cleanupMins) * time.Minute
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			var swept int64
			oneTimeTokens.Range(func(k, v any) bool {
				if time.Since(v.(oneTimeToken).createdAt) > maxAge {
					oneTimeTokens.Delete(k)
					swept++
				}
				return true
			})
			if swept > 0 {
				outstandingTokens.Add(-swept)
			}
		}
	}()
}

// ── Per-IP+UA pending-submission guard (item 7) ───────────────────────────────
//
// pendingSubmissions tracks one active unverified submission per IP+UA hash.
// A second submission from the same visitor is rejected until the first
// verification token expires (same TTL as the form token, ~5 min).

var pendingSubmissions sync.Map // key: sha256(ip+"|"+ua) hex, value: time.Time (expiry)

func ipUAKey(ip, ua string) string {
	sum := sha256.Sum256([]byte(ip + "|" + ua))
	return hex.EncodeToString(sum[:])
}

// setPendingSubmission marks this visitor as having an outstanding submission.
func setPendingSubmission(ip, ua string, ttl time.Duration) {
	pendingSubmissions.Store(ipUAKey(ip, ua), time.Now().Add(ttl))
}

// hasPendingSubmission returns true when an unexpired submission exists.
func hasPendingSubmission(ip, ua string) bool {
	v, ok := pendingSubmissions.Load(ipUAKey(ip, ua))
	if !ok {
		return false
	}
	if time.Now().After(v.(time.Time)) {
		pendingSubmissions.Delete(ipUAKey(ip, ua))
		return false
	}
	return true
}

// clearPendingSubmission removes the pending mark (e.g. after verification).
func clearPendingSubmission(ip, ua string) {
	pendingSubmissions.Delete(ipUAKey(ip, ua))
}

// startPendingSubmissionCleanup sweeps expired entries periodically.
func startPendingSubmissionCleanup(interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			pendingSubmissions.Range(func(k, v any) bool {
				if now.After(v.(time.Time)) {
					pendingSubmissions.Delete(k)
				}
				return true
			})
		}
	}()
}

// ── Email send-rate throttle (item 8) ────────────────────────────────────────
//
// emailSendThrottle tracks how many verification-triggering requests have been
// made in a sliding window and returns the backpressure tier:
//   0 = normal
//   1 = soft (add 500 ms latency to GET renders)
//   2 = hard (return load-shedding error)

type emailSendThrottle struct {
	mu        sync.Mutex
	times     []time.Time
	window    time.Duration
	softLimit int
	hardLimit int
}

func newEmailSendThrottle(window time.Duration, softLimit, hardLimit int) *emailSendThrottle {
	return &emailSendThrottle{
		window:    window,
		softLimit: softLimit,
		hardLimit: hardLimit,
	}
}

func (t *emailSendThrottle) record() {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-t.window)
	i := 0
	for i < len(t.times) && t.times[i].Before(cutoff) {
		i++
	}
	t.times = append(t.times[i:], now)
}

// tier returns 0 (normal), 1 (soft-delay), or 2 (hard-reject).
func (t *emailSendThrottle) tier() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := time.Now().Add(-t.window)
	count := 0
	for _, ts := range t.times {
		if ts.After(cutoff) {
			count++
		}
	}
	if t.hardLimit > 0 && count >= t.hardLimit {
		return 2
	}
	if t.softLimit > 0 && count >= t.softLimit {
		return 1
	}
	return 0
}

var globalEmailSendRate *emailSendThrottle // initialised in main()

// applyEmailBackpressure checks the global email send rate and either:
//   - tier 1: sleeps 500ms before the caller serves the page
//   - tier 2: writes a 503 and sets w to signal the caller to abort
//
// Callers must check ctx.Err() after calling this.
func applyEmailBackpressure(ctx context.Context, rate *emailSendThrottle, w http.ResponseWriter) {
	if rate == nil {
		return
	}
	switch rate.tier() {
	case 1:
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
		}
	case 2:
		http.Error(w, "server overloaded — please try again in a few minutes", http.StatusServiceUnavailable)
	}
}
