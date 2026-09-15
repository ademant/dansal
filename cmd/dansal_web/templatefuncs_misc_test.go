package main

import (
	"html/template"
	"strings"
	"testing"
)

// TestMarkdownPreviewHTMLUnwrapsLinks is a regression test: orgs.html wraps
// each org-card-desc preview inside a card that's itself one big <a> (the
// whole card is clickable). markdownHTML on a description containing a link
// (e.g. Folkclub Marburg's "Zum [Tanzen](...)") used to render a nested <a>
// inside that outer <a> — invalid HTML that browsers handle by truncating or
// splitting the outer card link, i.e. the reported "broken" org-card.
// markdownPreviewHTML must keep the link's text but drop the tag itself.
func TestMarkdownPreviewHTMLUnwrapsLinks(t *testing.T) {
	md := "Folk. Wir lieben Folk.\n\nZum [Tanzen](https://www.folkclub-marburg.de/wp/balfolk).\n"

	fullFn := tmplFuncsMisc["markdownHTML"].(func(string) template.HTML)
	full := string(fullFn(md))
	if !strings.Contains(full, "<a href=") {
		t.Fatalf("markdownHTML: expected a real <a href> link, got: %s", full)
	}

	previewFn := tmplFuncsMisc["markdownPreviewHTML"].(func(string) template.HTML)
	preview := string(previewFn(md))
	if strings.Contains(preview, "<a") {
		t.Errorf("markdownPreviewHTML: expected no <a> tag (would nest inside the card's own <a>), got: %s", preview)
	}
	if !strings.Contains(preview, "Tanzen") {
		t.Errorf("markdownPreviewHTML: expected the link's visible text to survive, got: %s", preview)
	}
}

// TestMarkdownPreviewHTMLNoLinks confirms plain descriptions (the common
// case) render identically either way.
func TestMarkdownPreviewHTMLNoLinks(t *testing.T) {
	md := "Just a plain description, no links."
	previewFn := tmplFuncsMisc["markdownPreviewHTML"].(func(string) template.HTML)
	preview := string(previewFn(md))
	if !strings.Contains(preview, "Just a plain description") {
		t.Errorf("markdownPreviewHTML: expected text to survive unchanged, got: %s", preview)
	}
}
