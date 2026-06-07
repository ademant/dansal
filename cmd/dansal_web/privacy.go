package main

import (
	"net/http"
	"strings"
	"time"
)

// doNotTrack returns true when the request signals the user doesn't want tracking.
// Checks DNT and Sec-GPC headers.
func doNotTrack(r *http.Request) bool {
	dnt := r.Header.Get("DNT")
	if dnt == "1" || strings.ToLower(dnt) == "1" {
		return true
	}
	gpc := r.Header.Get("Sec-GPC")
	if gpc == "1" {
		return true
	}
	return false
}

// saveDataOn returns true if the client requested reduced data usage.
func saveDataOn(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Save-Data"), "on")
}

// cookieConsentHandler lets users accept non-essential cookies. Respect DNT.
func cookieConsentHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if doNotTrack(r) {
			http.Error(w, "DNT enabled; cannot set consent", http.StatusForbidden)
			return
		}
		// Set a 1-year consent cookie (not HttpOnly so JS can read if needed)
		http.SetCookie(w, &http.Cookie{
			Name:     "dsw_cookie_consent",
			Value:    "1",
			Path:     "/",
			MaxAge:   int((365 * 24 * time.Hour).Seconds()),
			SameSite: http.SameSiteLaxMode,
			Secure:   true,
		})
		ref := r.Referer()
		if ref == "" {
			ref = "/"
		}
		http.Redirect(w, r, ref, http.StatusSeeOther)
	}
}
