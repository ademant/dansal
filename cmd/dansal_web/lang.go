package main

import (
	"net/http"
	"net/url"
)

func langHandler(i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if i18n.HasLang(code) && !doNotTrack(r) {
			setLangCookie(w, code)
		}
		// Referer is attacker-influenced (whatever page linked here), so it
		// must not be redirected to verbatim — an off-site Referer would be
		// an open redirect (#1003). Only redirect back to a same-site path.
		ref := "/"
		if u, err := url.Parse(r.Referer()); err == nil && u.Host == r.Host {
			if safe := safeReturnPath(u.RequestURI()); safe != "" {
				ref = safe
			}
		}
		http.Redirect(w, r, ref, http.StatusSeeOther)
	}
}
