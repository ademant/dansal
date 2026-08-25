package main

import (
	"strings"
	"testing"
)

// TestBaselineCSPFormAction guards against #1146 regressing: form-action
// must be explicitly declared so a CSP audit doesn't flag it as implied-only.
func TestBaselineCSPFormAction(t *testing.T) {
	csp := baselineCSP("test-nonce")
	if !strings.Contains(csp, "form-action 'self';") {
		t.Errorf("baselineCSP() missing explicit form-action directive: %q", csp)
	}
}

// TestCorpForPath locks in the route-aware Cross-Origin-Resource-Policy
// values from #1148: static assets meant to be hotlinked get 'cross-origin',
// private/admin areas get 'same-origin', and everything else (ordinary
// pages, ActivityPub JSON, embed pages) is left unset.
func TestCorpForPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/logo.avif", "cross-origin"},
		{"/banner.avif", "cross-origin"},
		{"/favicon.svg", "cross-origin"},
		{"/relay-icon", "cross-origin"},
		{"/relay-banner", "cross-origin"},
		{"/ai-badge", "cross-origin"},
		{"/admin/events", "same-origin"},
		{"/settings", "same-origin"},
		{"/internal/relay/redeliver", "same-origin"},
		{"/", ""},
		{"/events/42", ""},
		{"/org/example", ""},
		{"/.well-known/webfinger", ""},
		{"/embed/events", ""},
	}
	for _, c := range cases {
		if got := corpForPath(c.path); got != c.want {
			t.Errorf("corpForPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
