package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
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
// token is valid until consumeFormToken is called or it exceeds maxAgeMins.
// Optionally bound to the client IP when FormTokenBindIP is configured.

type oneTimeToken struct {
	createdAt time.Time
	ip        string
}

var oneTimeTokens sync.Map // key: hex string, value: oneTimeToken

func issueFormToken(ip string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	id := hex.EncodeToString(b)
	oneTimeTokens.Store(id, oneTimeToken{createdAt: time.Now(), ip: ip})
	return id
}

// consumeFormToken validates and atomically deletes a token.
// Returns false when the token is missing, expired, or the IP doesn't match
// (when bindIP is true). An empty tokenID always returns false.
func consumeFormToken(tokenID, ip string, maxAgeMins int, bindIP bool) bool {
	if tokenID == "" {
		return false
	}
	val, loaded := oneTimeTokens.LoadAndDelete(tokenID)
	if !loaded {
		return false
	}
	tok := val.(oneTimeToken)
	if maxAgeMins > 0 && time.Since(tok.createdAt) > time.Duration(maxAgeMins)*time.Minute {
		return false
	}
	if bindIP && tok.ip != ip {
		return false
	}
	return true
}

// startFormTokenCleanup removes expired tokens every cleanupMins minutes.
func startFormTokenCleanup(maxAgeMins, cleanupMins int) {
	interval := time.Duration(cleanupMins) * time.Minute
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			cutoff := time.Duration(maxAgeMins) * time.Minute
			oneTimeTokens.Range(func(k, v any) bool {
				if time.Since(v.(oneTimeToken).createdAt) > cutoff {
					oneTimeTokens.Delete(k)
				}
				return true
			})
		}
	}()
}
