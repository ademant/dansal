package main

import (
	"net/http"
	"time"
)

// checkETag sets the ETag response header and, when the request's
// If-None-Match matches it, writes a 304 and returns true. Mirrors the
// pattern the API already uses in checkPublicCacheHeaders (cmd/dansal) so
// crawlers get the same conditional-GET treatment on the HTML pages that
// render this data (#1129). Callers must return immediately when this
// returns true.
func checkETag(w http.ResponseWriter, r *http.Request, etag string) bool {
	if etag == "" {
		return false
	}
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

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
