// Package webcommon holds helpers shared by the dansal-web and dansal-webmin
// binaries: session-cookie helpers, client-IP extraction, site-settings
// accessors, and session-expiry parsing. The dansal API also imports ClientIP.
// Each helper was previously copy-pasted between the binaries and had started
// to drift (#1035).
package webcommon

import (
	"context"
	"database/sql"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ademant/dansal/internal/websession"
)

// Session wraps HMAC-signed session cookies (see websession) together with a
// request-context key for per-request user injection. dansal-web and
// dansal-webmin each instantiate one with their own cookie names, signing key,
// and user type; the login helpers themselves are shared so they can't drift.
type Session[T any] struct {
	cookies websession.Cookies
	ctxKey  any
}

// NewSession returns a Session backed by freshly generated websession cookies.
// tokenCookie and userCookie are the HTTP cookie names; ctxKey identifies the
// user in the request context. Each binary must pass a distinct ctxKey type so
// the two binaries' sessions can't collide.
func NewSession[T any](tokenCookie, userCookie string, ctxKey any) Session[T] {
	return Session[T]{cookies: websession.New(tokenCookie, userCookie), ctxKey: ctxKey}
}

// WithUser injects u into the request context for the remainder of the request.
func (s Session[T]) WithUser(r *http.Request, u *T) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), s.ctxKey, u))
}

// User returns the session user, preferring the request-context value (set by
// auth middleware) over the signed user cookie. Returns nil when neither holds
// a valid session.
func (s Session[T]) User(r *http.Request) *T {
	if u, ok := r.Context().Value(s.ctxKey).(*T); ok && u != nil {
		return u
	}
	var u T
	if !s.cookies.GetUser(r, &u) {
		return nil
	}
	return &u
}

// Token returns the raw session token cookie value, or "" when unset.
func (s Session[T]) Token(r *http.Request) string {
	return s.cookies.GetToken(r)
}

// Set issues the token and signed-user cookies, both expiring at expiresAt.
func (s Session[T]) Set(w http.ResponseWriter, token string, user T, expiresAt time.Time) {
	s.cookies.Set(w, token, user, expiresAt)
}

// SetUser re-signs and overwrites only the user cookie, leaving the token
// cookie untouched. Used to refresh the signed user blob after a process
// restart without disturbing the raw session token in the browser.
func (s Session[T]) SetUser(w http.ResponseWriter, user *T, expiresAt time.Time) {
	s.cookies.SetUser(w, user, expiresAt)
}

// Clear deletes both session cookies.
func (s Session[T]) Clear(w http.ResponseWriter) {
	s.cookies.Clear(w)
}

// ClientIP returns the client's IP address, honouring X-Forwarded-For and
// X-Real-IP set by the reverse proxy and falling back to the remote peer.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if parts := strings.Split(xff, ","); len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ParseExpiry parses an API-provided session expiry (RFC3339), falling back to
// ttl from now when it is missing or malformed.
func ParseExpiry(s string, ttl time.Duration) time.Time {
	expiresAt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().Add(ttl)
	}
	return expiresAt
}

// GetSiteSetting returns the raw value for a site_settings key, or "" when the
// key is missing, db is nil (webmin without a web DB), or the query fails. A
// real DB error is logged rather than silently swallowed so a broken settings
// store is visible in the logs; a missing table is expected on a brand-new DB
// before createTables has run.
func GetSiteSetting(db *sql.DB, key string) string {
	if db == nil {
		return ""
	}
	var v string
	err := db.QueryRow("SELECT value FROM site_settings WHERE key = ?", key).Scan(&v)
	if err != nil {
		if err != sql.ErrNoRows && !strings.Contains(err.Error(), "no such table") {
			log.Printf("get site setting %s: %v", key, err)
		}
		return ""
	}
	return v
}

// SetSiteSetting upserts a site_settings row, logging (not propagating) errors
// and returning early when db is nil.
func SetSiteSetting(db *sql.DB, key, value string) {
	if db == nil {
		return
	}
	if _, err := db.Exec(
		"INSERT INTO site_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		key, value); err != nil {
		log.Printf("set site setting %s: %v", key, err)
	}
}
