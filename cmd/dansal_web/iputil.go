package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/ademant/dansal/internal/webcommon"
)

// getClientIP returns the caller's IP. X-Forwarded-For and X-Real-IP are
// honoured only when the TCP peer is loopback (the local nginx proxy), via
// webcommon.ClientIP. Trusting them from any peer would let a client spoof its
// IP and bypass the login throttle, form-token IP binding and IP-keyed rate
// limits (#1703).
func getClientIP(r *http.Request) string {
	return webcommon.ClientIP(r)
}

// hashIP returns the first 8 hex characters of the SHA-256 hash of ip.
// Used in logs to avoid storing raw IPs.
func hashIP(ip string) string {
	h := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(h[:4])
}
