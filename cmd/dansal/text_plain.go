package main

import (
	"html"
	"strings"
)

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
