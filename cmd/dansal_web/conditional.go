package main

import (
	"net/http"
	"strconv"
	"strings"
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
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

// etagMatches implements the weak comparison If-None-Match requires: nginx's
// gzip/brotli filters rewrite a strong ETag to W/"..." for compressed
// responses, so a browser's If-None-Match arrives as W/"x" while we generate
// "x" -- an exact string compare never matched for any compressed client, so
// the conditional GETs below silently always answered 200. It also handles
// "*" and comma-separated lists (#1354).
func etagMatches(inm, etag string) bool {
	if inm == "" || etag == "" {
		return false
	}
	if strings.TrimSpace(inm) == "*" {
		return true
	}
	strip := func(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "W/") }
	want := strip(etag)
	for _, c := range strings.Split(inm, ",") {
		if strip(c) == want {
			return true
		}
	}
	return false
}

// checkPublicPage is the conditional GET for anonymous, session-independent
// HTML pages (event, location, musician, instructor) (#1354). Logged-in
// sessions skip it (their page carries personalized controls and a
// logged-in nav, see #1338). base is the page's own last-change time (unix);
// events are the events it lists: the newest of base, their changed_at and
// the end time of the most recently *ended* one is the page's Last-Modified,
// since the upcoming/past split moves with the clock, not only with data
// changes. The ETag also encodes the response language, and a 304 is only
// given on an ETag match: the same URL renders in the language from the
// dsw_lang cookie / Accept-Language, and a Last-Modified-only 304 kept
// serving a browser its cached page in the old language after switching.
// Cache-Control: no-cache forces revalidation (#1338). Callers must return
// when this returns true.
func checkPublicPage(w http.ResponseWriter, r *http.Request, i18n *I18n, base int64, events []Event) bool {
	if getSessionUser(r) != nil {
		return false
	}
	latest := base
	now := time.Now()
	for _, ev := range events {
		if ca := parseChangedAt(ev.ChangedAt); ca > latest {
			latest = ca
		}
		if end, err := time.Parse(time.RFC3339, ev.EndTime); err == nil && end.Before(now) && end.Unix() > latest {
			latest = end.Unix()
		}
	}
	if latest <= 0 {
		return false
	}
	etag := `"` + strconv.FormatInt(latest, 36) + "-" + i18n.detectLang(r) + `"`
	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("Last-Modified", time.Unix(latest, 0).UTC().Format(http.TimeFormat))
	h.Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
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
