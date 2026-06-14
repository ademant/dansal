package main

import (
	"context"
	"net/http"
	"time"

	"github.com/ademant/dansal/internal/websession"
)

type sessionContextKey int

const ctxSessionUser sessionContextKey = 1

var sessionCookies = websession.New("dsw_token", "dsw_user")

type SessionUser struct {
	ID          int    `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role"`
}

func withSessionUser(r *http.Request, u *SessionUser) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxSessionUser, u))
}

func getSessionUser(r *http.Request) *SessionUser {
	// Check request context first (set by cert-auth middleware)
	if u, ok := r.Context().Value(ctxSessionUser).(*SessionUser); ok && u != nil {
		return u
	}
	var u SessionUser
	if !sessionCookies.GetUser(r, &u) {
		return nil
	}
	return &u
}

func getSessionToken(r *http.Request) string {
	return sessionCookies.GetToken(r)
}

func setSession(w http.ResponseWriter, token string, user SessionUser, expiresAt time.Time) {
	sessionCookies.Set(w, token, user, expiresAt)
}

func clearSession(w http.ResponseWriter) {
	sessionCookies.Clear(w)
}
