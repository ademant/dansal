package main

import (
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
	VerifySent       string // channel name: "email", "telegram", "matrix"
	VerifiedChannel  string // channel name after token consumption
	TelegramDeepLink string
	APIKeys          []APIKey
	NewAPIKey        *APIKey
	Sessions         []SessionInfo
	Passkeys         []PasskeyInfo
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
		title := i18n.T(r, "settings_title")
		renderTemplate(w, tmpls.settings, tmplData(r, cfg, i18n, title, SettingsData{
			User:            u,
			Saved:           r.URL.Query().Get("saved") == "1",
			VerifySent:      r.URL.Query().Get("verify_sent"),
			VerifiedChannel: r.URL.Query().Get("verified"),
			APIKeys:         keys,
			Sessions:        sessions,
			Passkeys:        passkeys,
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
		if authThrottle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/settings/verify", authBlock, ip)
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
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
		if authThrottle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/settings/verify-telegram", authBlock, ip)
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
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
		if authThrottle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/settings/verify-matrix", authBlock, ip)
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
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
		} else if err.Error() == "expired" {
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
		http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
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
