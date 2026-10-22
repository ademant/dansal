package main

import (
	"net/http"
	"testing"
	"time"
)

// withPlainSafeClient swaps safeClient for a plain client for one test. The
// Mastodon tests point at local httptest servers, which the SSRF guard
// correctly refuses; the guard itself has its own tests.
func withPlainSafeClient(t *testing.T) {
	t.Helper()
	old := safeClient
	safeClient = &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() { safeClient = old })
}
