package main

import "testing"

// TestBuildTimezoneOptions covers #1394's dropdown source: the curated list
// must always include the instance's actual effective value, even when an
// admin configured something not in the curated set, so the page never
// misrepresents what's really active.
func TestBuildTimezoneOptions(t *testing.T) {
	t.Run("current value already in the list", func(t *testing.T) {
		opts := buildTimezoneOptions("Europe/Berlin")
		if len(opts) != len(commonTimezones) {
			t.Fatalf("got %d options, want the unmodified curated list (%d)", len(opts), len(commonTimezones))
		}
		if !contains(opts, "Europe/Berlin") {
			t.Error("Europe/Berlin missing from its own list")
		}
	})

	t.Run("current value not in the curated list is added", func(t *testing.T) {
		const exotic = "Pacific/Kiritimati"
		opts := buildTimezoneOptions(exotic)
		if len(opts) != len(commonTimezones)+1 {
			t.Fatalf("got %d options, want %d (curated + the exotic value)", len(opts), len(commonTimezones)+1)
		}
		if !contains(opts, exotic) {
			t.Errorf("%s missing from options: %v", exotic, opts)
		}
	})

	t.Run("empty current value adds nothing", func(t *testing.T) {
		opts := buildTimezoneOptions("")
		if len(opts) != len(commonTimezones) {
			t.Fatalf("got %d options, want the unmodified curated list (%d)", len(opts), len(commonTimezones))
		}
	})
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
