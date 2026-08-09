package main

import (
	"net/http"
	"time"

	"github.com/ademant/dansal/internal/webcommon"
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
						session.SetUser(w, su, parseSessionExpiry(me.TokenExpiresAt))
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

type SessionUser struct {
	ID          int    `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role"`
}

// session is the web binary's shared session helper; the wrappers below keep
// call sites short while delegating all cookie/context logic to webcommon so
// it can't drift from webmin (#1035).
var session = webcommon.NewSession[SessionUser]("dsw_token", "dsw_user", ctxSessionUser)

func withSessionUser(r *http.Request, u *SessionUser) *http.Request {
	return session.WithUser(r, u)
}

func getSessionUser(r *http.Request) *SessionUser {
	return session.User(r)
}

func getSessionToken(r *http.Request) string {
	return session.Token(r)
}

func setSession(w http.ResponseWriter, token string, user SessionUser, expiresAt time.Time) {
	session.Set(w, token, user, expiresAt)
}

func clearSession(w http.ResponseWriter) {
	session.Clear(w)
}

// defaultSessionTTL is the fallback session lifetime when the API response
// carries no parseable expires_at.
const defaultSessionTTL = 24 * time.Hour

// parseSessionExpiry parses the API-provided session expiry (RFC3339),
// defaulting to defaultSessionTTL when it is missing or malformed.
func parseSessionExpiry(s string) time.Time {
	return webcommon.ParseExpiry(s, defaultSessionTTL)
}

// establishSession issues the session cookies for a successful login response.
// The SessionUser↔LoginResponse mapping used to be copy-pasted at every login
// entry point (password, magic link, invite, mTLS); all four now go through
// here so the mapping can't drift (#1035).
func establishSession(w http.ResponseWriter, lr *LoginResponse) {
	setSession(w, lr.Token, SessionUser{
		ID:          lr.User.ID,
		Email:       lr.User.Email,
		DisplayName: lr.User.DisplayName,
		Role:        lr.User.Role,
	}, parseSessionExpiry(lr.ExpiresAt))
}
