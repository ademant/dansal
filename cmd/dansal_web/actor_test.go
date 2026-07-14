package main

import "testing"

func TestWebfingerSlug(t *testing.T) {
	cfg := testCfg()

	cases := []struct {
		name         string
		resource     string
		wantSlug     string
		wantNotFound bool
		wantOK       bool
	}{
		{"acct valid", "acct:myorg@example.com", "myorg", false, true},
		{"acct wrong domain", "acct:myorg@other.example", "", true, false},
		{"acct missing user part", "acct:@example.com", "", true, false},
		{"acct malformed no @", "acct:myorg", "", true, false},
		{"https org url valid", "https://example.com/org/myorg", "myorg", false, true},
		{"https org url trailing slash empty slug", "https://example.com/org/", "", true, false},
		{"https unrelated url", "https://example.com/federation/u/relay", "", false, false},
		{"unsupported scheme", "mailto:foo@example.com", "", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slug, notFound, ok := webfingerSlug(cfg, tc.resource)
			if slug != tc.wantSlug || notFound != tc.wantNotFound || ok != tc.wantOK {
				t.Errorf("webfingerSlug(%q) = (%q, %v, %v), want (%q, %v, %v)",
					tc.resource, slug, notFound, ok, tc.wantSlug, tc.wantNotFound, tc.wantOK)
			}
		})
	}
}
