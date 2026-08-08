package main

import (
	"context"
	"net/http"
	"time"

	"github.com/ademant/dansal/internal/websession"
)

// authRefreshMiddleware transparently re-establishes a session when the
// dsw_user HMAC cookie is missing or invalid (e.g. after a process restart)
// but the raw dsw_token cookie is still valid in the API's database.
// It calls GET /api/v1/me to validate the token, re-issues the signed user
// cookie with the correct expiry, and injects the user into the request
// context so requireLogin sees an authenticated session within this request.
func authRefreshMiddleware(client *DansalClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if getSessionUser(r) == nil {
				if token := getSessionToken(r); token != "" {
					if me, err := client.GetMe(r.Context(), token); err == nil {
						su := &SessionUser{
							ID:          me.ID,
							Email:       me.Email,
							DisplayName: me.DisplayName,
							Role:        me.Role,
						}
						sessionCookies.SetUser(w, su, parseSessionExpiry(me.TokenExpiresAt))
						r = withSessionUser(r, su)
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

type sessionContextKey int

const ctxSessionUser sessionContextKey = 1

var sessionCookies = websession.New("dsw_token", "dsw_user")

// defaultSessionTTL is the fallback session lifetime when the API response
// carries no parseable expires_at.
const defaultSessionTTL = 24 * time.Hour

// parseSessionExpiry parses the API-provided session expiry (RFC3339),
// defaulting to defaultSessionTTL when it is missing or malformed.
func parseSessionExpiry(s string) time.Time {
	expiresAt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().Add(defaultSessionTTL)
	}
	return expiresAt
}

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
