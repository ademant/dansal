package main

import "testing"

func TestDecodeHTMLEntities(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"no ampersand: untouched", "Tanzlernabend", "Tanzlernabend"},
		{"numeric decimal entity", "Kost ar c&#8217;hoat", "Kost ar c’hoat"},
		{"numeric entity for a dash", "Tanzlernabend &#8211; Kost ar c'hoat", "Tanzlernabend – Kost ar c'hoat"},
		{"named entity", "Rock &amp; Roll", "Rock & Roll"},
		{"hex numeric entity", "Caf&#x65; au lait", "Cafe au lait"},
		{"nbsp decodes to the real non-breaking space", "a&nbsp;b", "a b"},
		{"plain ampersand, not part of any entity: left alone", "Bed & Breakfast", "Bed & Breakfast"},
		{"empty string", "", ""},
		{"multiple entities", "&quot;Fest&quot; &amp; &lt;Noz&gt;", `"Fest" & <Noz>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decodeHTMLEntities(c.in); got != c.want {
				t.Errorf("decodeHTMLEntities(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
