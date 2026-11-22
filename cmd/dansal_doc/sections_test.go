package main

import (
	"bytes"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

// The sidebar lists a page's level 2 and 3 headings under its link. The IDs
// must match the anchors goldmark writes into the HTML, or the links are dead.
func TestSectionsMatchRenderedAnchors(t *testing.T) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.Table),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	body := []byte("# Besucher\n\nIntro.\n\n## 🗺️ Veranstaltungen durchsuchen\n\n### Interaktive Karte\n\ntext\n\n## Cookies\n\n#### deep\n")

	got := sections(md, body)
	want := []struct {
		level int
		title string
	}{
		{2, "🗺️ Veranstaltungen durchsuchen"},
		{3, "Interaktive Karte"},
		{2, "Cookies"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sections, want %d: %+v", len(got), len(want), got)
	}

	var rendered bytes.Buffer
	if err := md.Convert(body, &rendered); err != nil {
		t.Fatal(err)
	}
	for i, w := range want {
		if got[i].Level != w.level || got[i].Title != w.title {
			t.Errorf("section %d = %+v, want level %d title %q", i, got[i], w.level, w.title)
		}
		anchor := []byte(`id="` + got[i].ID + `"`)
		if !bytes.Contains(rendered.Bytes(), anchor) {
			t.Errorf("section %d: rendered HTML has no %s", i, anchor)
		}
	}
}
