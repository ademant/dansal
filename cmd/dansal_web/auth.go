package main

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type LoginPageData struct {
	ErrorKey     string
	Email        string
	MagicSent    string // "email" or "telegram" when a link was just sent
	Next         string
	FormToken    string
	TotpRequired bool   // password verified; waiting for TOTP code
	Password     string // held briefly for TOTP second step (POST body only, never in URL)
}

// safeNext returns next only when it is a local path with no host or scheme,
// preventing open redirects.
func safeNext(next string) string {
	if next == "" {
		return "/dashboard"
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

// loginMinAge / loginMaxAge are the per-class bounds for the login form token.
// These are intentionally tight: login forms need only seconds to fill.
const loginMinAge = 100 * time.Millisecond

func loginFormMaxAge(cfg *Config) time.Duration {
	return time.Duration(cfg.FormTokenLoginMaxAgeMins) * time.Minute
}

func stdFormMaxAge(cfg *Config) time.Duration {
	return time.Duration(cfg.FormTokenMaxAgeMins) * time.Minute
}

func loginPageHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if getSessionUser(r) != nil {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}
		ip := getClientIP(r)
		if tokenThrottle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/login", tokenBlock, ip)
			http.Error(w, i18n.T(r, "form_token_cap_error"), http.StatusTooManyRequests)
			return
		}
		applyEmailBackpressure(r.Context(), globalEmailSendRate, w)
		if r.Context().Err() != nil {
			return
		}
		tok := issueFormToken(ip)
		if tok == "" {
			http.Error(w, i18n.T(r, "form_token_cap_error"), http.StatusServiceUnavailable)
			return
		}
		tokenThrottle.record(ip)
		title := i18n.T(r, "login_title")
		renderTemplate(w, tmpls.login, tmplData(r, cfg, i18n, title, LoginPageData{
			MagicSent: r.URL.Query().Get("magic_sent"),
			Next:      r.URL.Query().Get("next"),
			FormToken: tok,
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
		email := r.FormValue("email")
		password := r.FormValue("password")
		totpCode := r.FormValue("totp_code")
		next := safeNext(r.FormValue("next"))
		ip := getClientIP(r)

		if r.FormValue("phone2") != "" || !consumeFormToken(r.FormValue("_form_token"), ip, loginMinAge, loginFormMaxAge(cfg), cfg.FormTokenBindIP) {
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		}

		if throttle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/login", authBlock, ip)
			tok := issueFormToken(ip)
			title := i18n.T(r, "login_title")
			renderTemplate(w, tmpls.login, tmplData(r, cfg, i18n, title, LoginPageData{
				ErrorKey:  "login_error_throttled",
				Email:     email,
				Next:      r.FormValue("next"),
				FormToken: tok,
			}))
			return
		}

		lr, err := client.Login(r.Context(), email, password, totpCode, ip, r.UserAgent())
		if err == ErrTOTPRequired {
			tok := issueFormToken(ip)
			title := i18n.T(r, "login_title")
			renderTemplate(w, tmpls.login, tmplData(r, cfg, i18n, title, LoginPageData{
				TotpRequired: true,
				Email:        email,
				Password:     password,
				Next:         r.FormValue("next"),
				FormToken:    tok,
			}))
			return
		}
		if err != nil {
			delay := throttle.recordFailure(ip, email, password)
			log.Printf("%s ip=%s path=/login", authFail, ip)
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
			tok := issueFormToken(ip)
			title := i18n.T(r, "login_title")
			renderTemplate(w, tmpls.login, tmplData(r, cfg, i18n, title, LoginPageData{
				ErrorKey:  errorKey,
				Email:     email,
				Next:      r.FormValue("next"),
				FormToken: tok,
			}))
			return
		}

		throttle.reset(ip)
		expiresAt, err := time.Parse(time.RFC3339, lr.ExpiresAt)
		if err != nil {
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

func logoutHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token := getSessionToken(r); token != "" {
			_ = client.Logout(r.Context(), token)
		}
		clearSession(w)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	}
}

func magicRequestHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if authThrottle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/magic", authBlock, ip)
			tok := issueFormToken(ip)
			title := i18n.T(r, "login_title")
			renderTemplate(w, tmpls.login, tmplData(r, cfg, i18n, title, LoginPageData{
				ErrorKey:  "login_error_throttled",
				FormToken: tok,
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
		if r.FormValue("phone2") != "" || !consumeFormToken(r.FormValue("_form_token"), ip, loginMinAge, loginFormMaxAge(cfg), cfg.FormTokenBindIP) {
			http.Redirect(w, r, "/login?magic_sent="+channel, http.StatusSeeOther)
			return
		}
		identifier := r.FormValue("identifier")
		if identifier != "" {
			authThrottle.record(ip)
			globalEmailSendRate.record()
			_ = client.RequestMagicLogin(r.Context(), identifier, channel, cfg.publicBaseURL())
		}
		http.Redirect(w, r, "/login?magic_sent="+channel, http.StatusSeeOther)
	}
}

func magicLoginHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		lr, err := client.UseMagicLogin(r.Context(), token, getClientIP(r), r.UserAgent())
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
			ID:          lr.User.ID,
			Email:       lr.User.Email,
			DisplayName: lr.User.DisplayName,
			Role:        lr.User.Role,
		}, expiresAt)
		next := "/dashboard"
		if p := safeReturnPath(r.URL.Query().Get("next")); p != "" {
			next = p
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}
