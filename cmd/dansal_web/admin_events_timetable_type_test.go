package main

import "testing"

func TestNormalizeTimetableEntryType(t *testing.T) {
	cases := map[string]string{
		"meal":             "meal",
		" dance-workshop ": "dance-workshop",
		"workshop":         "workshop",
		"my-custom-track":  "my-custom-track",
		"":                 "bal",
		"   ":              "bal",
	}
	for in, want := range cases {
		if got := normalizeTimetableEntryType(in); got != want {
			t.Errorf("normalizeTimetableEntryType(%q) = %q, want %q", in, got, want)
		}
	}
}
