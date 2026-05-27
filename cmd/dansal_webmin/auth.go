package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

const (
	cookieToken = "dwm_token"
	cookieUser  = "dwm_user"
)

type SessionUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func getSessionUser(r *http.Request) *SessionUser {
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
