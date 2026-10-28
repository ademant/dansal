package main

import (
	"net/http/httptest"
	"testing"
)

// TestGetClientIPTrustsForwardedHeadersOnlyFromLoopback covers #1703: the
// forwarded headers are client-controlled unless the TCP peer is the local
// nginx proxy, so only a loopback peer may supply the client IP through them.
func TestGetClientIPTrustsForwardedHeadersOnlyFromLoopback(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		xri        string
		want       string
	}{
		{"direct peer ignores spoofed X-Forwarded-For", "203.0.113.9:4000", "127.0.0.1", "", "203.0.113.9"},
		{"direct peer ignores spoofed X-Real-IP", "203.0.113.9:4000", "", "10.0.0.1", "203.0.113.9"},
		{"loopback peer uses X-Forwarded-For", "127.0.0.1:4000", "198.51.100.7, 127.0.0.1", "", "198.51.100.7"},
		{"loopback peer falls back to X-Real-IP", "127.0.0.1:4000", "", "198.51.100.8", "198.51.100.8"},
		{"loopback peer without headers", "127.0.0.1:4000", "", "", "127.0.0.1"},
		{"IPv6 loopback peer uses X-Forwarded-For", "[::1]:4000", "198.51.100.9", "", "198.51.100.9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.xri != "" {
				r.Header.Set("X-Real-IP", tc.xri)
			}
			if got := getClientIP(r); got != tc.want {
				t.Errorf("getClientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}
