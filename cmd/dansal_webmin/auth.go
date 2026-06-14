package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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

func setSession(w http.ResponseWriter, token string, user SessionUser, expiresAt time.Time) {
	sessionCookies.Set(w, token, user, expiresAt)
}

func clearSession(w http.ResponseWriter) {
	sessionCookies.Clear(w)
}

func requireLogin(cfg *Config, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Cert auth: check nginx-forwarded mTLS headers (trusted because webmin only binds localhost)
		if r.Header.Get("X-Client-Verified") == "SUCCESS" {
			cn := extractCN(r.Header.Get("X-Client-DN"))
			if cn != "" {
				if su := certAuthUser(cfg, cn); su != nil {
					// Refresh session cookie and inject user into context for this request
					setSession(w, "", *su, time.Now().Add(24*time.Hour))
					next(w, r.WithContext(context.WithValue(r.Context(), ctxSessionUser, su)))
					return
				}
				// Cert present but CN is not an admin user
				http.Error(w, "Forbidden: certificate CN does not match an admin user", http.StatusForbidden)
				return
			}
		}

		if getSessionUser(r) == nil {
			http.Redirect(w, r, "/login?next="+r.URL.RequestURI(), http.StatusSeeOther)
			return
		}
		next(w, r)
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
		// Auto-login via cert if verified
		if r.Header.Get("X-Client-Verified") == "SUCCESS" {
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
		renderTemplate(w, tmpls.login, tmplData(r, cfg, "Login", map[string]string{
			"Next": r.URL.Query().Get("next"),
		}))
	}
}

func loginPostHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		email := r.FormValue("email")
		password := r.FormValue("password")
		next := r.FormValue("next")
		if next == "" {
			next = "/"
		}

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
