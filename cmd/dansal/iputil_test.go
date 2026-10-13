package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetClientIP_OnlyTrustsForwardedHeadersFromLoopback covers #1534: a
// direct (non-loopback) connection must never have its
// X-Forwarded-For/X-Real-IP honoured, since those are otherwise trivially
// spoofable to defeat IP-keyed rate limiting (loginRateLimiter) and
// IP-pinned publisher tokens. Only a request whose actual TCP peer is
// loopback (i.e. relayed by the local nginx reverse proxy) gets them
// trusted.
func TestGetClientIP_OnlyTrustsForwardedHeadersFromLoopback(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		xRealIP    string
		want       string
	}{
		{
			name:       "direct connection ignores spoofed X-Forwarded-For",
			remoteAddr: "203.0.113.5:1234",
			xff:        "9.9.9.9",
			want:       "203.0.113.5",
		},
		{
			name:       "direct connection ignores spoofed X-Real-IP",
			remoteAddr: "203.0.113.5:1234",
			xRealIP:    "9.9.9.9",
			want:       "203.0.113.5",
		},
		{
			name:       "loopback peer (reverse proxy) honours X-Forwarded-For",
			remoteAddr: "127.0.0.1:54321",
			xff:        "198.51.100.7, 10.0.0.1",
			want:       "198.51.100.7",
		},
		{
			name:       "loopback peer honours X-Real-IP when no XFF",
			remoteAddr: "127.0.0.1:54321",
			xRealIP:    "198.51.100.7",
			want:       "198.51.100.7",
		},
		{
			name:       "loopback peer with no forwarded headers falls back to peer",
			remoteAddr: "127.0.0.1:54321",
			want:       "127.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xRealIP != "" {
				r.Header.Set("X-Real-IP", tt.xRealIP)
			}
			if got := getClientIP(r); got != tt.want {
				t.Errorf("getClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
