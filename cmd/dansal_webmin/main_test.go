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

// scriptSrcDirective extracts the script-src directive (up to the next ';')
// out of a full CSP header value.
func scriptSrcDirective(csp string) string {
	i := strings.Index(csp, "script-src ")
	if i == -1 {
		return ""
	}
	rest := csp[i:]
	if j := strings.Index(rest, ";"); j != -1 {
		return rest[:j]
	}
	return rest
}

// TestWebminCSPScriptSrc guards against #1141/#1147 regressing: script-src
// must carry the per-request nonce and 'strict-dynamic', and must no longer
// allow 'unsafe-inline' now that every <script> element carries a nonce
// (#1149 cleared the inline-handler blocker).
func TestWebminCSPScriptSrc(t *testing.T) {
	scriptSrc := scriptSrcDirective(webminCSP("abc123"))
	if !strings.Contains(scriptSrc, "'nonce-abc123'") {
		t.Errorf("webminCSP() script-src missing the per-request nonce: %q", scriptSrc)
	}
	if !strings.Contains(scriptSrc, "'strict-dynamic'") {
		t.Errorf("webminCSP() script-src missing 'strict-dynamic': %q", scriptSrc)
	}
	if strings.Contains(scriptSrc, "'unsafe-inline'") {
		t.Errorf("webminCSP() script-src must not contain 'unsafe-inline': %q", scriptSrc)
	}
}
