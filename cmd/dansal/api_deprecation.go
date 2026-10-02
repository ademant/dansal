package main

import (
	"net/http"
	"strings"
)

// apiDeprecatedSince is when the route aliases below were deprecated
// (2026-10-02, phase-22: #1379 syndication, #1380 org assignment, #1381
// current-user prefix), as an RFC 9745 Deprecation date.
const apiDeprecatedSince = "@1790899200"

// deprecatedAlias serves an old route through h unchanged, but marks every
// response as deprecated (RFC 9745 Deprecation header) and names the
// canonical route in a Link rel="successor-version" header, so a client can
// notice and migrate. successor is a route pattern; its {name} wildcards are
// filled from the request's path values when the response is written —
// late, because aliases whose old route carried the ids in the body set
// those path values themselves before delegating.
func deprecatedAlias(successor string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h(&deprecationWriter{ResponseWriter: w, r: r, successor: successor}, r)
	}
}

type deprecationWriter struct {
	http.ResponseWriter
	r         *http.Request
	successor string
	marked    bool
}

func (d *deprecationWriter) mark() {
	if d.marked {
		return
	}
	d.marked = true
	h := d.ResponseWriter.Header()
	h.Set("Deprecation", apiDeprecatedSince)
	h.Add("Link", "<"+fillPathValues(d.successor, d.r)+`>; rel="successor-version"`)
}

func (d *deprecationWriter) WriteHeader(code int) {
	d.mark()
	d.ResponseWriter.WriteHeader(code)
}

func (d *deprecationWriter) Write(b []byte) (int, error) {
	d.mark()
	return d.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (d *deprecationWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }

func fillPathValues(pattern string, r *http.Request) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(pattern, '{')
		j := strings.IndexByte(pattern, '}')
		if i < 0 || j < i {
			b.WriteString(pattern)
			return b.String()
		}
		b.WriteString(pattern[:i])
		if v := r.PathValue(pattern[i+1 : j]); v != "" {
			b.WriteString(v)
		} else {
			b.WriteString(pattern[i : j+1])
		}
		pattern = pattern[j+1:]
	}
}
