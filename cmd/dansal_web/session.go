package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"
)

type sessionContextKey int

const ctxSessionUser sessionContextKey = 1

const (
	cookieToken = "dsw_token"
	cookieUser  = "dsw_user"
)

type SessionUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func withSessionUser(r *http.Request, u *SessionUser) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxSessionUser, u))
}

func getSessionUser(r *http.Request) *SessionUser {
	// Check request context first (set by cert-auth middleware)
	if u, ok := r.Context().Value(ctxSessionUser).(*SessionUser); ok && u != nil {
		return u
	}
	c, err := r.Cookie(cookieUser)
	if err != nil {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	var u SessionUser
	if json.Unmarshal(decoded, &u) != nil {
		return nil
	}
	return &u
}

func getSessionToken(r *http.Request) string {
	c, err := r.Cookie(cookieToken)
	if err != nil {
		return ""
	}
	return c.Value
}

func setSession(w http.ResponseWriter, token string, user SessionUser, expiresAt time.Time) {
	userJSON, _ := json.Marshal(user)
	userEncoded := base64.StdEncoding.EncodeToString(userJSON)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieToken,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cookieUser,
		Value:    userEncoded,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSession(w http.ResponseWriter) {
	for _, name := range []string{cookieToken, cookieUser} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
			HttpOnly: true,
			Secure:   true,
		})
	}
}
