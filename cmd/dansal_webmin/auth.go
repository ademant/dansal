package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type contextKey int

const ctxSessionUser contextKey = 1

const (
	cookieToken = "dwm_token"
	cookieUser  = "dwm_user"
)

type SessionUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	CertAuth bool   `json:"cert_auth,omitempty"`
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
		if u.Username == cn && u.Role == "admin" {
			return &SessionUser{ID: u.ID, Username: u.Username, Role: u.Role, CertAuth: true}
		}
	}
	return nil
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
			Name:    name,
			Value:   "",
			Path:    "/",
			MaxAge:  -1,
			Expires: time.Unix(0, 0),
		})
	}
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
		ID       int    `json:"id"`
		Username string `json:"username"`
		Role     string `json:"role"`
	} `json:"user"`
}

func apiLogin(ctx *http.Request, dansalURL, username, password string) (*loginResponse, error) {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequestWithContext(ctx.Context(), http.MethodPost, dansalURL+"/api/v1/login", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if ip := ctx.Header.Get("X-Forwarded-For"); ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	req.Header.Set("User-Agent", ctx.UserAgent())

	resp, err := http.DefaultClient.Do(req)
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
		renderTemplate(w, tmpls.login, tmplData(cfg, "Login", map[string]string{
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
		username := r.FormValue("username")
		password := r.FormValue("password")
		next := r.FormValue("next")
		if next == "" {
			next = "/"
		}

		lr, err := apiLogin(r, cfg.DansalURL, username, password)
		if err != nil {
			log.Printf("webmin login failed for %q: %v", username, err)
			renderTemplate(w, tmpls.login, tmplData(cfg, "Login", map[string]string{
				"Error":    "Invalid username or password.",
				"Username": username,
				"Next":     next,
			}))
			return
		}

		if lr.User.Role != "admin" {
			log.Printf("webmin login rejected: %q has role %q", username, lr.User.Role)
			renderTemplate(w, tmpls.login, tmplData(cfg, "Login", map[string]string{
				"Error":    "Admin access required.",
				"Username": username,
				"Next":     next,
			}))
			return
		}

		expiresAt, _ := time.Parse(time.RFC3339, lr.ExpiresAt)
		if expiresAt.IsZero() {
			expiresAt = time.Now().Add(24 * time.Hour)
		}
		setSession(w, lr.Token, SessionUser{
			ID:       lr.User.ID,
			Username: lr.User.Username,
			Role:     lr.User.Role,
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
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					resp.Body.Close()
				}
			}
		}
		clearSession(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}
