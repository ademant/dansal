package main

import (
	"testing"
	"unicode/utf8"
)

// TestTruncateUTF8 asserts truncateUTF8 never splits a multi-byte rune
// (#1015 — the Atom-feed summary truncation used to do plain s[:max], which
// can cut a UTF-8 sequence in half and produce invalid UTF-8 output).
func TestTruncateUTF8(t *testing.T) {
	cases := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"under limit", "hello", 10, "hello"},
		{"exact limit", "hello", 5, "hello"},
		{"ascii truncation", "hello world", 5, "hello"},
		// "café" is c-a-f-é where é is the 2-byte UTF-8 sequence 0xC3 0xA9.
		// max=4 would land mid-sequence (after 0xC3) with a naive s[:4].
		{"multi-byte rune boundary", "café", 4, "caf"},
		{"multi-byte rune boundary exact", "café", 5, "café"},
		{"empty string", "", 5, ""},
		{"max zero", "hello", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncateUTF8(c.s, c.max)
			if got != c.want {
				t.Fatalf("truncateUTF8(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateUTF8(%q, %d) = %q is not valid UTF-8", c.s, c.max, got)
			}
		})
	}
}

// TestGenerateTokenNoTrailingDashOrUnderscore asserts generateToken never
// returns a token ending in '-' or '_' (#1305): link auto-detection in chat
// and email clients commonly treats those as trailing punctuation and drops
// them when linkifying plain text, silently truncating the clickable
// verification/invite link by one character before the recipient ever sees
// it. Runs many iterations since the bug only manifests for the ~1-in-32
// tokens that would otherwise end that way.
func TestGenerateTokenNoTrailingDashOrUnderscore(t *testing.T) {
	for i := 0; i < 2000; i++ {
		tok, err := generateToken(24)
		if err != nil {
			t.Fatalf("generateToken: %v", err)
		}
		if tok == "" {
			t.Fatal("generateToken returned an empty token")
		}
		if last := tok[len(tok)-1]; last == '-' || last == '_' {
			t.Fatalf("generateToken returned %q, ending in %q", tok, last)
		}
	}
}
