package main

import (
	"strings"
	"testing"
)

// TestWebminCSPFormAction guards against #1146 regressing: form-action must
// be explicitly declared so a CSP audit doesn't flag it as implied-only.
func TestWebminCSPFormAction(t *testing.T) {
	csp := webminCSP("test-nonce")
	if !strings.Contains(csp, "form-action 'self';") {
		t.Errorf("webminCSP() missing explicit form-action directive: %q", csp)
	}
}
