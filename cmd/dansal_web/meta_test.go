package main

import "testing"

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
