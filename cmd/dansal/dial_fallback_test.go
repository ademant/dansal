package main

import (
	"context"
	"strings"
	"testing"
)

// The SSRF guard is unchanged: one private address in the set denies the
// request outright, before any dial.
func TestSafeDialContextStillDeniesPrivate(t *testing.T) {
	_, err := safeDialContext(context.Background(), "tcp", "localhost:80")
	if err == nil || !strings.Contains(err.Error(), "private/internal") {
		t.Fatalf("expected private-address denial, got %v", err)
	}
}
