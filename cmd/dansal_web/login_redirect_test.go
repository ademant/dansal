package main

import "testing"

// TestIsLocalPath covers code scanning alert #132: login redirects may only go
// to local paths. Protocol-relative and backslash forms must not pass.
func TestIsLocalPath(t *testing.T) {
	cases := map[string]bool{
		"/dashboard":           true,
		"/events/12?x=1":       true,
		"//evil.example":       false,
		"/\\evil.example":      false,
		"https://evil.example": false,
		"evil.example":         false,
		"":                     false,
	}
	for in, want := range cases {
		if got := isLocalPath(in); got != want {
			t.Errorf("isLocalPath(%q) = %v, want %v", in, got, want)
		}
	}
}
