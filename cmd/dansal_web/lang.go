package main

import "net/http"

func langHandler(i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if i18n.HasLang(code) && !doNotTrack(r) {
			setLangCookie(w, code)
		}
		ref := r.Referer()
		if ref == "" {
			ref = "/"
		}
		http.Redirect(w, r, ref, http.StatusSeeOther)
	}
}
