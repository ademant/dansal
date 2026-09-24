package main

import (
	"net/http"
	"time"
)

// NEVER use a conditional GET (304) for an HTML page rendered through
// base.html: every response carries a fresh per-request CSP nonce (#1141)
// both in its inline <script nonce=...> tags and in its
// Content-Security-Policy header. A 304 keeps the browser's cached body (old
// nonce) but the browser adopts the 304's headers (new nonce), so every inline
// script and base.js is then blocked by CSP: no map, no events, an empty
// week grid. A returning visitor's browser hit exactly that after the weak-ETag
// fix made #1129's conditional GETs actually fire (#1367). Conditional GET is
// only safe for responses with no nonce-carrying body, like the sitemap.

// checkLastModified sets the Last-Modified response header from t and, when
// the request's If-Modified-Since indicates the resource hasn't changed
// since, writes a 304 and returns true. Callers must return immediately when
// this returns true.
func checkLastModified(w http.ResponseWriter, r *http.Request, t time.Time) bool {
	if t.IsZero() {
		return false
	}
	lastMod := t.UTC().Truncate(time.Second)
	w.Header().Set("Last-Modified", lastMod.Format(http.TimeFormat))
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		if since, err := http.ParseTime(ims); err == nil && !lastMod.After(since) {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	return false
}
