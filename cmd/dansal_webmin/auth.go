package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ademant/dansal/internal/websession"
)

type contextKey int

const ctxSessionUser contextKey = 1

var sessionCookies = websession.New("dwm_token", "dwm_user")

type SessionUser struct {
	ID          int    `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role"`
	CertAuth    bool   `json:"cert_auth,omitempty"`
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

// extractCN parses the CN value from a DN string like "CN=alice,O=dansal"
func extractCN(dn string) string {
	for _, part := range strings.Split(dn, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToUpper(part), "CN=") {
			return part[3:]
		}
	}
	return ""
}

// certAuthUser looks up a user by CN in the admin socket and returns them
// if they have role=admin. Returns nil if not found or not admin.
func certAuthUser(cfg *Config, cn string) *SessionUser {
	users, err := listAdminUsers(cfg.AdminSocket)
	if err != nil {
		log.Printf("cert auth: socket error: %v", err)
		return nil
	}
	for _, u := range users {
		if u.Email == cn && u.Role == "admin" {
			return &SessionUser{ID: u.ID, Email: u.Email, Role: u.Role, CertAuth: true}
		}
	}
	return nil
}

func getSessionToken(r *http.Request) string {
	return sessionCookies.GetToken(r)
}

// isLoopback reports whether r's direct peer (r.RemoteAddr, not any
// forwarded header) is 127.0.0.1/::1. X-Client-Verified/X-Client-DN are only
// trustworthy when nginx itself is the one talking to us — if webmin's
// listen address is ever widened beyond localhost, anyone on the network
// could otherwise forge those headers and become admin with zero
// credentials (#994).
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// safeNextPath validates that raw is a same-site relative path suitable for
// a post-login redirect. Returns "/" if raw is empty or not a safe path,
// preventing an off-site `?next=` from becoming an open redirect (#994).
func safeNextPath(raw string) string {
	if raw == "" || raw[0] != '/' || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/\\") {
		return "/"
	}
	if strings.ContainsAny(raw, "\\\t\r\n") {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "/"
	}
	return raw
}

// meResponse is the subset of GET /api/v1/me consumed for token revalidation.
type meResponse struct {
	ID             int    `json:"id"`
	Email          string `json:"email"`
	DisplayName    string `json:"display_name"`
	Role           string `json:"role"`
	TokenExpiresAt string `json:"token_expires_at"`
}

// apiMe calls GET /api/v1/me on the dansal API to revalidate token against
// the live session store — a token that was revoked, or whose owner was
// disabled/deleted, comes back as an error here.
func apiMe(ctx context.Context, dansalURL, token string) (*meResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dansalURL+"/api/v1/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("me: HTTP %d", resp.StatusCode)
	}
	var m meResponse
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// refreshedSessionUser returns the current session user, revalidating the
// raw token against the dansal API when the signed user-blob cookie is
// missing or invalid (e.g. after a webmin restart rotates the HMAC signing
// key, or because a session was revoked and the blob cookie just hasn't
// expired yet). A successful revalidation re-issues the signed blob with
// the correct expiry; a failed one (revoked token, disabled/deleted user)
// leaves the caller unauthenticated (#995).
func refreshedSessionUser(cfg *Config, w http.ResponseWriter, r *http.Request) *SessionUser {
	if su := getSessionUser(r); su != nil {
		return su
	}
	token := getSessionToken(r)
	if token == "" {
		return nil
	}
	me, err := apiMe(r.Context(), cfg.DansalURL, token)
	if err != nil || me.Role != "admin" {
		return nil
	}
	su := &SessionUser{ID: me.ID, Email: me.Email, DisplayName: me.DisplayName, Role: me.Role}
	expiresAt, _ := time.Parse(time.RFC3339, me.TokenExpiresAt)
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(24 * time.Hour)
	}
	setSession(w, token, *su, expiresAt)
	return su
}

func setSession(w http.ResponseWriter, token string, user SessionUser, expiresAt time.Time) {
	sessionCookies.Set(w, token, user, expiresAt)
}

func clearSession(w http.ResponseWriter) {
	sessionCookies.Clear(w)
}

func requireLogin(cfg *Config, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Cert auth: trust nginx-forwarded mTLS headers only when the direct
		// peer is loopback — webmin is designed to sit behind a local nginx
		// that terminates mTLS and sets these headers itself (#994).
		if isLoopback(r.RemoteAddr) && r.Header.Get("X-Client-Verified") == "SUCCESS" {
			cn := extractCN(r.Header.Get("X-Client-DN"))
			if cn != "" {
				if su := certAuthUser(cfg, cn); su != nil {
					// Refresh session cookie and inject user into context for this request
					setSession(w, "", *su, time.Now().Add(24*time.Hour))
					next(w, r.WithContext(context.WithValue(r.Context(), ctxSessionUser, su)))
					return
				}
				// Cert present but CN is not an admin user. Fall through to
				// cookie-session auth instead of hard-403ing here — a stale
				// or forged header with an unrecognised CN must not be able
				// to kick out an already-authenticated admin (#994).
			}
		}

		if cfg.CertOnly {
			http.Error(w, "Forbidden: certificate authentication required", http.StatusForbidden)
			return
		}

		if su := refreshedSessionUser(cfg, w, r); su != nil {
			next(w, r.WithContext(context.WithValue(r.Context(), ctxSessionUser, su)))
			return
		}

		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	}
}

type loginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	User      struct {
		ID          int    `json:"id"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	} `json:"user"`
}

func apiLogin(ctx *http.Request, dansalURL, email, password string) (*loginResponse, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequestWithContext(ctx.Context(), http.MethodPost, dansalURL+"/api/v1/login", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if ip := ctx.Header.Get("X-Forwarded-For"); ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	req.Header.Set("User-Agent", ctx.UserAgent())

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("invalid credentials")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("login failed: HTTP %d", resp.StatusCode)
	}
	var lr loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, err
	}
	return &lr, nil
}

func loginPageHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Auto-login via cert if verified (loopback-only, see requireLogin, #994)
		if isLoopback(r.RemoteAddr) && r.Header.Get("X-Client-Verified") == "SUCCESS" {
			cn := extractCN(r.Header.Get("X-Client-DN"))
			if cn != "" {
				if su := certAuthUser(cfg, cn); su != nil {
					setSession(w, "", *su, time.Now().Add(24*time.Hour))
					http.Redirect(w, r, "/", http.StatusSeeOther)
					return
				}
			}
		}
		if getSessionUser(r) != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		renderTemplate(w, tmpls.login, tmplData(r, cfg, "Login", map[string]any{
			"Next":     safeNextPath(r.URL.Query().Get("next")),
			"CertOnly": cfg.CertOnly,
		}))
	}
}

func loginPostHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.CertOnly {
			http.Error(w, "Forbidden: certificate authentication required", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		email := r.FormValue("email")
		password := r.FormValue("password")
		next := safeNextPath(r.FormValue("next"))

		lr, err := apiLogin(r, cfg.DansalURL, email, password)
		if err != nil {
			log.Printf("webmin login failed for %q: %v", email, err)
			renderTemplate(w, tmpls.login, tmplData(r, cfg, "Login", map[string]string{
				"Error": "Invalid email or password.",
				"Email": email,
				"Next":  next,
			}))
			return
		}

		if lr.User.Role != "admin" {
			log.Printf("webmin login rejected: %q has role %q", email, lr.User.Role)
			renderTemplate(w, tmpls.login, tmplData(r, cfg, "Login", map[string]string{
				"Error": "Admin access required.",
				"Email": email,
				"Next":  next,
			}))
			return
		}

		expiresAt, _ := time.Parse(time.RFC3339, lr.ExpiresAt)
		if expiresAt.IsZero() {
			expiresAt = time.Now().Add(24 * time.Hour)
		}
		setSession(w, lr.Token, SessionUser{
			ID:          lr.User.ID,
			Email:       lr.User.Email,
			DisplayName: lr.User.DisplayName,
			Role:        lr.User.Role,
		}, expiresAt)
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}

func logoutHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := getSessionToken(r)
		if token != "" {
			// best-effort revoke on the dansal API
			req, err := http.NewRequestWithContext(r.Context(), http.MethodDelete, cfg.DansalURL+"/api/v1/login", nil)
			if err == nil {
				req.Header.Set("Authorization", "Bearer "+token)
				resp, err := httpClient.Do(req)
				if err == nil {
					resp.Body.Close()
				}
			}
		}
		clearSession(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}
