package main

import "strings"

// isValidEmail checks that s contains an "@" sign — a minimal sanity check.
func isValidEmail(s string) bool {
	return strings.Contains(s, "@")
}

// isValidMatrixID checks that s is a fully-qualified Matrix user ID: @localpart:server
func isValidMatrixID(s string) bool {
	if !strings.HasPrefix(s, "@") {
		return false
	}
	colon := strings.IndexByte(s, ':')
	return colon > 1 && colon < len(s)-1
}
