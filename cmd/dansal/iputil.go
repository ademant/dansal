package main

import (
	"net"
	"net/http"
	"strings"
)

// getClientIP returns the caller's IP, honouring X-Forwarded-For/X-Real-IP
// only when the request's actual TCP peer is loopback — i.e. it arrived via
// the local nginx reverse proxy, which dansal always binds behind (127.0.0.1
// listen addresses, see CLAUDE.md). Anything else is a direct connection, so
// those headers are client-controlled and must not be trusted: otherwise a
// caller can spoof a fresh X-Forwarded-For per request to defeat IP-keyed
// rate limiting (loginRateLimiter) and IP-pinned publisher tokens (#1534).
func getClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		if host != "" {
			return host
		}
		return r.RemoteAddr
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	return host
}
