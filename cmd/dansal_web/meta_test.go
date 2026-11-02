package main

import (
	"strings"
	"testing"
)

// #1280: a description carrying a literal "&nbsp;" (leftover from pasting
// out of a rich-text editor) must not leak into plain-text output as the
// literal entity string.
func TestPlainTextDescStripsNbspAndMarkdown(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			"lone nbsp paragraph",
			"Oida Gillamoos Festgelände auf der Liebesinsel\n\n&nbsp;\n\n",
			"Oida Gillamoos Festgelände auf der Liebesinsel",
		},
		{
			"markdown syntax still stripped",
			"**Bold** and _italic_ and [a link](https://example.com) and &nbsp; filler",
			"Bold and italic and a link and filler",
		},
		{
			"other named/numeric entities decode too",
			"Rock &amp; Roll &#8211; folk",
			"Rock & Roll – folk",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := plainTextDesc(c.in); got != c.want {
				t.Errorf("plainTextDesc(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestMetaDescStripsNbsp(t *testing.T) {
	got := metaDesc("Oida Gillamoos Festgelände auf der Liebesinsel\n\n&nbsp;\n\n", metaDescMaxLen)
	want := "Oida Gillamoos Festgelände auf der Liebesinsel"
	if got != want {
		t.Errorf("metaDesc(...) = %q, want %q", got, want)
	}
}

// TestTruncateMultibyteBeforeSpace covers #1740: a multibyte rune before the
// last space used to make truncate slice past the end of the rune slice.
func TestTruncateMultibyteBeforeSpace(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		maxLen int
		want   string
	}{
		{"umlauts before the space", "Größe Übergänge äöüß und Ähnliches hier weiter", 30, "Größe Übergänge äöüß und…"},
		{"emoji and separators", "🎶 Ball · Kassel – Samstag 20 Uhr mit Live-Musik und Tanz", 40, "🎶 Ball · Kassel – Samstag 20 Uhr mit…"},
		{"space exactly at the limit", "äöü äöü", 4, "äöü…"},
		{"no space in the cut", "ÄÖÜÄÖÜÄÖÜ", 4, "ÄÖÜÄ…"},
		{"short enough", "äöü", 5, "äöü"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.in, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.maxLen, got, tc.want)
			}
			if n := len([]rune(got)); n > tc.maxLen+1 {
				t.Errorf("result has %d runes, limit %d plus ellipsis", n, tc.maxLen)
			}
		})
	}
}

// TestEventMetaDescLongGermanDescription covers #1740 on the path that panicked:
// a long, umlaut-heavy description reaches truncate in eventMetaDesc.
func TestEventMetaDescLongGermanDescription(t *testing.T) {
	long := strings.Repeat("Übungsräume für Tänze und Öffnungszeiten sind schön · ", 20)
	got := truncate(long, metaDescMaxLen)
	if n := len([]rune(got)); n > metaDescMaxLen+1 {
		t.Errorf("description has %d runes, limit %d", n, metaDescMaxLen)
	}
}
