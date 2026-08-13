package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
)

type PasskeyInfo struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type SettingsData struct {
	User             UserInfo
	ErrorKey         string
	Saved            bool
	TOTPNudge        bool   // true after first-time password set — prompts TOTP setup
	VerifySent       string // channel name: "email", "telegram", "matrix"
	VerifiedChannel  string // channel name after token consumption
	TelegramDeepLink string
	APIKeys          []APIKey
	NewAPIKey        *APIKey
	Sessions         []SessionInfo
	Passkeys         []PasskeyInfo
	TOTPSetupURI     string         // non-empty during setup flow
	TOTPSetupSecret  string         // shown as manual-entry fallback during setup
	OIDCProviders    []OIDCProvider // instance-wide providers offered as "Link" buttons (#1096)
	OIDCIdentities   []UserIdentity // already-linked identities
	Linked           bool           // true right after a successful link
	LinkError        bool
	UnlinkError      bool
}

// settingsThrottleCheck redirects to /settings when the client's IP is
// auth-throttled. Returns true when the caller should abort. Shared by the
// verify, verify-telegram, and verify-matrix handlers.
func settingsThrottleCheck(w http.ResponseWriter, r *http.Request, ip, path string) bool {
	if authThrottle.isBlocked(ip) {
		log.Printf("%s ip=%s path=%s", authBlock, ip, path)
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return true
	}
	return false
}

func settingsPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		u, err := client.GetUser(r.Context(), su.ID, token)
		if err != nil {
			http.Error(w, "could not load user", http.StatusBadGateway)
			return
		}
		keys, _ := client.ListAPIKeys(r.Context(), token)
		sessions, _ := client.GetSessions(r.Context(), token)
		passkeys, _ := client.ListPasskeys(r.Context(), token)
		oidcProviders, _ := client.GetOIDCProviders(r.Context(), nil)
		oidcIdentities, _ := client.ListOIDCIdentities(r.Context(), token)
		title := i18n.T(r, "settings_title")
		renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, SettingsData{
			User:            u,
			Saved:           r.URL.Query().Get("saved") == "1",
			TOTPNudge:       r.URL.Query().Get("totp_nudge") == "1",
			VerifySent:      r.URL.Query().Get("verify_sent"),
			VerifiedChannel: r.URL.Query().Get("verified"),
			APIKeys:         keys,
			Sessions:        sessions,
			Passkeys:        passkeys,
			OIDCProviders:   oidcProviders,
			OIDCIdentities:  oidcIdentities,
			Linked:          r.URL.Query().Get("linked") == "1",
			LinkError:       r.URL.Query().Get("linkerr") == "1",
			UnlinkError:     r.URL.Query().Get("unlinkerr") == "1",
		}))
	}
}

func settingsUpdateHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		fields := map[string]string{
			"email":        r.FormValue("email"),
			"display_name": r.FormValue("display_name"),
			"description":  r.FormValue("description"),
			"telegram":     r.FormValue("telegram"),
			"matrix":       r.FormValue("matrix"),
			"mastodon":     r.FormValue("mastodon"),
			"website":      r.FormValue("website"),
		}

		if err := client.UpdateUser(r.Context(), su.ID, fields, token); err != nil {
			u, _ := client.GetUser(r.Context(), su.ID, token)
			title := i18n.T(r, "settings_title")
			renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, SettingsData{
				User:     u,
				ErrorKey: "settings_save_error",
			}))
			return
		}
		http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
	}
}

func settingsSendVerifyHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		ip := getClientIP(r)
		if settingsThrottleCheck(w, r, ip, "/settings/verify") {
			return
		}
		authThrottle.record(ip)
		token := getSessionToken(r)
		baseURL := cfg.publicBaseURL()

		if err := client.SendEmailVerification(r.Context(), su.ID, baseURL, token); err != nil {
			u, _ := client.GetUser(r.Context(), su.ID, token)
			title := i18n.T(r, "settings_title")
			renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, SettingsData{
				User:     u,
				ErrorKey: "settings_verify_error",
			}))
			return
		}
		http.Redirect(w, r, "/settings?verify_sent=email", http.StatusSeeOther)
	}
}

func settingsTelegramVerifyHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		ip := getClientIP(r)
		if settingsThrottleCheck(w, r, ip, "/settings/verify-telegram") {
			return
		}
		authThrottle.record(ip)
		token := getSessionToken(r)
		baseURL := cfg.publicBaseURL()

		deepLink, err := client.GetTelegramVerifyLink(r.Context(), su.ID, baseURL, token)
		u, _ := client.GetUser(r.Context(), su.ID, token)
		title := i18n.T(r, "settings_title")
		if err != nil {
			renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, SettingsData{
				User:     u,
				ErrorKey: "settings_verify_error",
			}))
			return
		}
		renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, SettingsData{
			User:             u,
			TelegramDeepLink: deepLink,
		}))
	}
}

func settingsMatrixVerifyHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		ip := getClientIP(r)
		if settingsThrottleCheck(w, r, ip, "/settings/verify-matrix") {
			return
		}
		authThrottle.record(ip)
		token := getSessionToken(r)
		baseURL := cfg.publicBaseURL()

		if err := client.SendMatrixVerification(r.Context(), su.ID, baseURL, token); err != nil {
			u, _ := client.GetUser(r.Context(), su.ID, token)
			title := i18n.T(r, "settings_title")
			renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, SettingsData{
				User:     u,
				ErrorKey: "settings_verify_error",
			}))
			return
		}
		http.Redirect(w, r, "/settings?verify_sent=matrix", http.StatusSeeOther)
	}
}

func settingsCreateAPIKeyHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		name := r.FormValue("name")
		expiresAt := r.FormValue("expires_at")
		newKey, err := client.CreateAPIKey(r.Context(), token, name, expiresAt)
		u, _ := client.GetUser(r.Context(), su.ID, token)
		keys, _ := client.ListAPIKeys(r.Context(), token)
		title := i18n.T(r, "settings_title")
		data := SettingsData{User: u, APIKeys: keys}
		if err != nil {
			data.ErrorKey = "settings_save_error"
		} else {
			data.NewAPIKey = newKey
		}
		renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, data))
	}
}

func settingsSessionRevokeHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		_ = client.RevokeSession(r.Context(), id, token)
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}

func settingsDeleteAPIKeyHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		_ = client.DeleteAPIKey(r.Context(), token, id)
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}

// POST /settings/magic-link — generate a self-service magic login link for
// the current user (e.g. to scan on a second device and add a passkey there).
// Proxies POST /api/v1/users/me/magic-link and forwards the JSON response.
func settingsMagicLinkHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		var result map[string]string
		w.Header().Set("Content-Type", "application/json")
		if err := client.do(r.Context(), "POST", "/api/v1/users/me/magic-link", token, nil, &result, http.StatusOK); err != nil {
			http.Error(w, `{"error":"failed to generate link"}`, http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(result)
	}
}

func settingsDeleteAccountHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		u, err := client.GetUser(r.Context(), su.ID, token)
		if err != nil {
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
			return
		}
		if su.Role == "admin" {
			title := i18n.T(r, "settings_title")
			keys, _ := client.ListAPIKeys(r.Context(), token)
			sessions, _ := client.GetSessions(r.Context(), token)
			renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, SettingsData{
				User:     u,
				ErrorKey: "settings_delete_admin_error",
				APIKeys:  keys,
				Sessions: sessions,
			}))
			return
		}
		confirmEmail := r.FormValue("confirm_email")
		if confirmEmail != u.Email {
			title := i18n.T(r, "settings_title")
			keys, _ := client.ListAPIKeys(r.Context(), token)
			sessions, _ := client.GetSessions(r.Context(), token)
			renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, SettingsData{
				User:     u,
				ErrorKey: "settings_delete_confirm_error",
				APIKeys:  keys,
				Sessions: sessions,
			}))
			return
		}
		if err := client.DeleteOwnAccount(r.Context(), token); err != nil {
			log.Printf("delete account user %d: %v", su.ID, err)
			title := i18n.T(r, "settings_title")
			keys, _ := client.ListAPIKeys(r.Context(), token)
			sessions, _ := client.GetSessions(r.Context(), token)
			renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, SettingsData{
				User:     u,
				ErrorKey: "settings_save_error",
				APIKeys:  keys,
				Sessions: sessions,
			}))
			return
		}
		clearSession(w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

type VerifyData struct {
	Success              bool
	BoardVerify          bool
	ContactRequestVerify bool
	BoardDelete          bool
	ErrorKey             string
	BoardManageUpdated   bool
	BoardManageDeleted   bool
}

func verifyEmailHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		channel, err := client.ConsumeVerification(r.Context(), token)
		if err == nil && getSessionUser(r) != nil {
			if channel == "" {
				channel = "email"
			}
			http.Redirect(w, r, "/settings?verified="+channel, http.StatusSeeOther)
			return
		}
		title := i18n.T(r, "verify_title")
		var data VerifyData
		if err == nil {
			data = VerifyData{Success: true}
		} else if errors.Is(err, errExpired) {
			data = VerifyData{ErrorKey: "verify_error_expired"}
		} else {
			data = VerifyData{ErrorKey: "verify_error_invalid"}
		}
		renderTemplate(w, tmpls.verify, tmplData(r, cfg, i18n, title, data))
	}
}

func settingsChangePasswordHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		oldPw := r.FormValue("old_password")
		newPw := r.FormValue("new_password")
		firstPassword := oldPw == "" // no old password field means first-time set
		if err := client.ChangePassword(r.Context(), oldPw, newPw, token); err != nil {
			u, _ := client.GetUser(r.Context(), su.ID, token)
			keys, _ := client.ListAPIKeys(r.Context(), token)
			sessions, _ := client.GetSessions(r.Context(), token)
			passkeys, _ := client.ListPasskeys(r.Context(), token)
			title := i18n.T(r, "settings_title")
			renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, SettingsData{
				User:     u,
				ErrorKey: "settings_password_error",
				APIKeys:  keys,
				Sessions: sessions,
				Passkeys: passkeys,
			}))
			return
		}
		dest := "/settings?saved=1"
		if firstPassword {
			dest += "&totp_nudge=1"
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
	}
}

func settingsPasskeyRegisterBeginHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		webauthnAuthedProxyDo(cfg, client, "/api/v1/user/webauthn/register/begin", token, w, r)
	}
}

func settingsPasskeyRegisterFinishHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		webauthnAuthedProxyDo(cfg, client, "/api/v1/user/webauthn/register/finish", token, w, r)
	}
}

func settingsPasskeyDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		token := getSessionToken(r)
		resp, err := client.authed(r.Context(), "DELETE", "/api/v1/user/webauthn/credentials/"+id, token, nil)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}

// settingsOIDCIdentityDeleteHandler unlinks an OIDC identity (#1096). The
// API's self-lockout guard (hasOtherLoginMethod) rejects it with 409 when
// it's the caller's last login method — surfaced back to /settings as
// unlinkerr=1 rather than silently dropped.
func settingsOIDCIdentityDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		token := getSessionToken(r)
		resp, err := client.authed(r.Context(), "DELETE", "/api/v1/user/oidc-identities/"+id, token, nil)
		if err != nil {
			http.Redirect(w, r, "/settings?unlinkerr=1", http.StatusSeeOther)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			http.Redirect(w, r, "/settings?unlinkerr=1", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}

// GET /settings/totp/setup — generate a new TOTP secret and show the QR code.
func settingsTOTPSetupHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		u, _ := client.GetUser(r.Context(), su.ID, token)
		if !u.HasPassword {
			// TOTP only protects password logins; redirect silently when no password is set.
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
			return
		}
		info, err := client.TOTPSetup(r.Context(), token)
		if err != nil {
			log.Printf("totp setup: %v", err)
			http.Redirect(w, r, "/settings?error=totp_setup_failed", http.StatusSeeOther)
			return
		}
		keys, _ := client.ListAPIKeys(r.Context(), token)
		sessions, _ := client.GetSessions(r.Context(), token)
		passkeys, _ := client.ListPasskeys(r.Context(), token)
		title := i18n.T(r, "settings_title")
		renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, SettingsData{
			User:            u,
			APIKeys:         keys,
			Sessions:        sessions,
			Passkeys:        passkeys,
			TOTPSetupURI:    info.URI,
			TOTPSetupSecret: info.Secret,
		}))
	}
}

// POST /settings/totp/confirm — verify the code and activate TOTP.
func settingsTOTPConfirmHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		code := r.FormValue("code")
		if err := client.TOTPConfirm(r.Context(), token, code); err != nil {
			http.Redirect(w, r, "/settings/totp/setup?error=invalid_code", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
	}
}

// POST /settings/totp/disable — disable TOTP; requires current TOTP code.
func settingsTOTPDisableHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		code := r.FormValue("code")
		if err := client.TOTPDisable(r.Context(), token, code); err != nil {
			http.Redirect(w, r, "/settings?error=totp_invalid_code", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
	}
}
