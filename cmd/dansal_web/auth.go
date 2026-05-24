package main

import (
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func getClientIP(r *http.Request) string {
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

type LoginPageData struct {
	ErrorKey  string
	Username  string
	MagicSent string // "email" or "telegram" when a link was just sent
	Next      string
	FormToken string
}

// safeNext returns next only when it is a local path with no host or scheme,
// preventing open redirects.
func safeNext(next string) string {
	if next == "" {
		return "/"
	}

	// Some clients/browsers may treat backslashes as path separators.
	normalized := strings.ReplaceAll(next, "\\", "/")

	u, err := url.Parse(normalized)
	if err != nil {
		return "/"
	}
	if u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	if !strings.HasPrefix(u.Path, "/") {
		return "/"
	}

	// Return canonical parsed form, not raw user input.
	return u.String()
}

func loginPageHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if getSessionUser(r) != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		title := i18n.T(r, "login_title")
		renderTemplate(w, tmpls.login, tmplData(r, cfg, i18n, title, LoginPageData{
			MagicSent: r.URL.Query().Get("magic_sent"),
			Next:      r.URL.Query().Get("next"),
			FormToken: newFormToken(),
		}))
	}
}

func loginHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	throttle := newLoginThrottle(
		cfg.LoginMaxFailures,
		time.Duration(cfg.LoginWindowMins)*time.Minute,
	)
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		username := r.FormValue("username")
		password := r.FormValue("password")
		next := safeNext(r.FormValue("next"))
		ip := getClientIP(r)

		if r.FormValue("phone2") != "" || !validFormToken(r.FormValue("_form_ts"), cfg.MinSubmitSecs) {
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		}

		if throttle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/login", authBlock, ip)
			title := i18n.T(r, "login_title")
			renderTemplate(w, tmpls.login, tmplData(r, cfg, i18n, title, LoginPageData{
				ErrorKey: "login_error_throttled",
				Username: username,
				Next:     r.FormValue("next"),
			}))
			return
		}

		lr, err := client.Login(r.Context(), username, password)
		if err != nil {
			delay := throttle.recordFailure(ip)
			log.Printf("login failed from %s: invalid credentials for %q", ip, username)
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				return
			}
			errorKey := "login_error_invalid"
			if throttle.isBlocked(ip) {
				errorKey = "login_error_throttled"
			}
			title := i18n.T(r, "login_title")
			renderTemplate(w, tmpls.login, tmplData(r, cfg, i18n, title, LoginPageData{
				ErrorKey: errorKey,
				Username: username,
				Next:     r.FormValue("next"),
			}))
			return
		}

		throttle.reset(ip)
		expiresAt, err := time.Parse(time.RFC3339, lr.ExpiresAt)
		if err != nil {
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

func logoutHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token := getSessionToken(r); token != "" {
			_ = client.Logout(r.Context(), token)
		}
		clearSession(w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func magicRequestHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if authThrottle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/magic", authBlock, ip)
			title := i18n.T(r, "login_title")
			renderTemplate(w, tmpls.login, tmplData(r, cfg, i18n, title, LoginPageData{
				ErrorKey: "login_error_throttled",
			}))
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		channel := r.FormValue("channel")
		if channel == "" {
			channel = "email"
		}
		if r.FormValue("phone2") != "" || !validFormToken(r.FormValue("_form_ts"), cfg.MinSubmitSecs) {
			http.Redirect(w, r, "/login?magic_sent="+channel, http.StatusSeeOther)
			return
		}
		identifier := r.FormValue("identifier")
		if identifier != "" {
			authThrottle.record(ip)
			_ = client.RequestMagicLogin(r.Context(), identifier, channel, cfg.publicBaseURL())
		}
		http.Redirect(w, r, "/login?magic_sent="+channel, http.StatusSeeOther)
	}
}

func magicLoginHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		lr, err := client.UseMagicLogin(r.Context(), token)
		if err != nil {
			title := i18n.T(r, "login_title")
			renderTemplate(w, tmpls.login, tmplData(r, cfg, i18n, title, LoginPageData{
				ErrorKey: "login_magic_error",
			}))
			return
		}

		expiresAt, err := time.Parse(time.RFC3339, lr.ExpiresAt)
		if err != nil {
			expiresAt = time.Now().Add(24 * time.Hour)
		}
		setSession(w, lr.Token, SessionUser{
			ID:       lr.User.ID,
			Username: lr.User.Username,
			Role:     lr.User.Role,
		}, expiresAt)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}
