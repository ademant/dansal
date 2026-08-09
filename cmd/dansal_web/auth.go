package main

import (
	"crypto/rand"
	"encoding/base64"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type LoginPageData struct {
	ErrorKey     string
	Email        string
	MagicSent    string // "email" or "telegram" when a link was just sent
	Next         string
	FormToken    string
	TotpRequired bool
	PendingToken string // opaque server-side handle replacing the password hidden field
}

// pendingTOTPAuth holds credentials across the TOTP second step so the
// password is never written into the HTML form.
type pendingTOTPAuth struct {
	email     string
	password  string
	expiresAt time.Time
}

var (
	totpPendingMu  sync.Mutex
	totpPendingMap = map[string]pendingTOTPAuth{}
)

// issueTOTPPending stores (email, password) under a random key for 5 minutes
// and returns the key. The key goes into a hidden form field; the password
// never touches the page.
func issueTOTPPending(email, password string) string {
	b := make([]byte, 24)
	rand.Read(b)
	token := base64.URLEncoding.EncodeToString(b)
	totpPendingMu.Lock()
	totpPendingMap[token] = pendingTOTPAuth{email: email, password: password, expiresAt: time.Now().Add(5 * time.Minute)}
	totpPendingMu.Unlock()
	return token
}

// consumeTOTPPending retrieves and deletes the pending auth for token.
// Returns (auth, true) when found and not expired.
func consumeTOTPPending(token string) (pendingTOTPAuth, bool) {
	totpPendingMu.Lock()
	defer totpPendingMu.Unlock()
	p, ok := totpPendingMap[token]
	if !ok {
		return pendingTOTPAuth{}, false
	}
	delete(totpPendingMap, token)
	if time.Now().After(p.expiresAt) {
		return pendingTOTPAuth{}, false
	}
	return p, true
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
				Next:      r.FormValue("next"),
				FormToken: tok,
			}))
			return
		}

		// TOTP second step: pending_token replaces the password hidden field.
		var email, password string
		if pt := r.FormValue("pending_token"); pt != "" {
			pending, ok := consumeTOTPPending(pt)
			if !ok {
				tok := issueFormToken(ip)
				title := i18n.T(r, "login_title")
				renderTemplate(w, tmpls.login, tmplData(r, cfg, i18n, title, LoginPageData{
					ErrorKey:  "login_error_invalid",
					Next:      r.FormValue("next"),
					FormToken: tok,
				}))
				return
			}
			email, password = pending.email, pending.password
		} else {
			email = r.FormValue("email")
			password = r.FormValue("password")
		}

		lr, err := client.Login(r.Context(), email, password, totpCode, ip, r.UserAgent())
		if err == ErrTOTPRequired {
			pending := issueTOTPPending(email, password)
			tok := issueFormToken(ip)
			title := i18n.T(r, "login_title")
			renderTemplate(w, tmpls.login, tmplData(r, cfg, i18n, title, LoginPageData{
				TotpRequired: true,
				Email:        email,
				PendingToken: pending,
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
		establishSession(w, lr)

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

		establishSession(w, lr)
		next := "/dashboard"
		if p := safeReturnPath(r.URL.Query().Get("next")); p != "" {
			next = p
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}
