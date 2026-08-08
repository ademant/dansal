package main

import (
	"net/http"
	"time"
)

// Cookie name for the verified-email board session (#1047).
const boardSessionCookie = "dsw_board"

// getBoardSessionToken returns the board session token from the cookie, or "".
func getBoardSessionToken(r *http.Request) string {
	ck, err := r.Cookie(boardSessionCookie)
	if err != nil {
		return ""
	}
	return ck.Value
}

// setBoardSessionCookie stores token in the dsw_board HttpOnly cookie,
// expiring at expiresAt.
func setBoardSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     boardSessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearBoardSessionCookie deletes the dsw_board cookie from the browser.
func clearBoardSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     boardSessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   true,
	})
}
