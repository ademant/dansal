package main

import (
	"html"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

// feedHTMLPolicy strips all markup rather than allowing a safe subset:
// descriptions are stored as Markdown source and rendered via goldmark's
// default (raw HTML tags omitted entirely, not escaped/passed through), so
// an allowlisted tag surviving sanitization would still never reach the
// rendered page — it would just be silently dropped at render time instead
// of at import time. Stripping to plain text here matches what actually
// survives rendering anyway, and avoids needing an HTML-to-Markdown
// conversion step to preserve formatting that would otherwise be lost.
var feedHTMLPolicy = bluemonday.StrictPolicy()

// sanitizeFeedHTML removes HTML markup from untrusted feed-sourced text
// before it's treated as Markdown source and stored (#1447, compliance
// G10) — RSS/Atom descriptions in particular routinely carry real HTML.
// Tags are dropped entirely, including the content of tags like <script>
// whose content was never meant to be read as text. bluemonday's output is
// a safe HTML fragment, not decoded plain text (e.g. a literal "&" becomes
// "&amp;"), so callers must still run decodeHTMLEntities on the result to
// get back to plain literal text rather than storing double-escaped
// entities.
func sanitizeFeedHTML(s string) string {
	if !strings.ContainsRune(s, '<') {
		return s
	}
	return feedHTMLPolicy.Sanitize(s)
}

// decodeHTMLEntities decodes HTML entities (numeric like &#8217; and named
// like &amp;) out of a plain-text field. dansal never renders title,
// description, or name/address fields as HTML — they're always HTML-escaped
// again at output time (event.html, meta tags, JSON-LD) — so a producer
// that runs such a field through an HTML-rendering filter before sending it
// (e.g. WordPress's get_the_title(), which HTML-entity-encodes typographic
// characters for display: "'" -> "&#8217;") would otherwise have the
// literal entity text stored and re-escaped, corrupting the display (#1405,
// confirmed live via wp-dansal#144). A well-formed HTML entity sequence in
// a plain-text field is essentially always a producer mistake, never
// intentional content, so decoding it is safe.
func decodeHTMLEntities(s string) string {
	if !strings.ContainsRune(s, '&') {
		return s
	}
	return html.UnescapeString(s)
}
