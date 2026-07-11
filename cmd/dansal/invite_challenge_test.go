package main

import (
	"strings"
	"testing"
)

func TestExtractInviteChallenge(t *testing.T) {
	cases := []struct {
		name     string
		metadata string
		want     string
		wantErr  bool
	}{
		{"absent", "", "", false},
		{"no challenge field", `{"client_name":"wp-dansal"}`, "", false},
		{"valid challenge", `{"challenge":"0123456789abcdef"}`, "0123456789abcdef", false},
		{"too short", `{"challenge":"short"}`, "", true},
		{"too long", `{"challenge":"` + strings.Repeat("a", 300) + `"}`, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := extractInviteChallenge([]byte(c.metadata))
			if (err != nil) != c.wantErr {
				t.Fatalf("extractInviteChallenge(%q) error = %v, wantErr %v", c.metadata, err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("extractInviteChallenge(%q) = %q, want %q", c.metadata, got, c.want)
			}
		})
	}
}
