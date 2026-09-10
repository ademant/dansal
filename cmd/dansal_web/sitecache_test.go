package main

import "testing"

// TestParseSameAs covers #1296's site_settings parsing: one URL per line,
// blank lines dropped, and nil (not an empty slice) for an empty/blank
// setting so callers can tell "not configured" from "configured empty".
func TestParseSameAs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   \n\n  \t\n", nil},
		{"single URL", "https://github.com/example", []string{"https://github.com/example"}},
		{
			"multiple URLs with blank lines and surrounding whitespace",
			"https://github.com/example\n\n  https://mas.to/@example  \n\nhttps://www.wikidata.org/wiki/Q123\n",
			[]string{"https://github.com/example", "https://mas.to/@example", "https://www.wikidata.org/wiki/Q123"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseSameAs(c.raw)
			if len(got) != len(c.want) {
				t.Fatalf("parseSameAs(%q) = %v, want %v", c.raw, got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("parseSameAs(%q)[%d] = %q, want %q", c.raw, i, got[i], c.want[i])
				}
			}
		})
	}
}
