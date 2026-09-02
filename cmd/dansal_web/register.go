package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const pendingRegCookie = "pending_reg"

// loadRegisterFormData best-effort loads the org list and service info for
// re-rendering the register form after a duplicate/error submission. Failures
// are logged — the render's primary purpose is showing the error, and a 502
// would hide it.
func loadRegisterFormData(ctx context.Context, client *DansalClient) ([]Organization, DansalInfo) {
	orgs, err := client.GetOrganizations(ctx)
	if err != nil {
		log.Printf("register: could not load organizations: %v", err)
	}
	info, err := client.GetServiceInfo(ctx)
	if err != nil {
		log.Printf("register: could not load service info: %v", err)
	}
	return orgs, info
}

type RegisterPageData struct {
	Orgs                 []Organization
	Error                string
	FormToken            string
	PendingID            int    // set when cookie found and record still active
	PendingVerified      bool   // true = verified, awaiting admin review
	PendingApproved      bool   // true = admin approved; InviteURL holds the link
	PendingToken         string // verification token for resend
	PendingHasPasskey    bool   // true = passkey already bound to this pending registration
	PendingHasAuthMethod bool   // true = onboarding complete (password set, or passkey bound) -- ready for admin review (#1223)
	InviteURL            string // set when approved without contact info
	TelegramAvailable    bool   // true = telegram channel is configured on the API
}

type RegisterDoneData struct {
	Channel       string // "email" or "telegram"
	TelegramToken string
	BotName       string
}

type AdminRegistrationsData struct {
	Regs           []PendingRegistration
	PendingInvites []PendingInvite
	Flash          string
}

func registerPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !registrationEnabled(cfg) {
			http.NotFound(w, r)
			return
		}

		// Check for pending registration cookie.
		if c, err := r.Cookie(pendingRegCookie); err == nil {
			parts := strings.SplitN(c.Value, ":", 2)
			if len(parts) == 2 {
				if id, err := strconv.Atoi(parts[0]); err == nil {
					pendingToken := parts[1]
					status, err := client.GetRegistrationStatus(r.Context(), id, pendingToken)
					if err == nil && status != nil && !status.Expired {
						title := i18n.T(r, "register_title")
						renderTemplate(w, tmpls.register, tmplData(r, cfg, i18n, title, RegisterPageData{
							PendingID:            id,
							PendingVerified:      status.Verified,
							PendingApproved:      status.Approved,
							PendingToken:         pendingToken,
							PendingHasPasskey:    status.HasPasskey,
							PendingHasAuthMethod: status.HasAuthMethod,
							InviteURL:            status.InviteURL,
						}))
						return
					}
					// Expired or not found — clear cookie.
					http.SetCookie(w, &http.Cookie{Name: pendingRegCookie, MaxAge: -1, Path: "/", Secure: true})
				}
			}
		}

		ip := getClientIP(r)
		if tokenThrottle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/register", tokenBlock, ip)
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
		orgs, err := client.GetOrganizations(r.Context())
		info, err2 := client.GetServiceInfo(r.Context())
		if err != nil || err2 != nil {
			logHTTPError(w, r, "could not load register data", http.StatusBadGateway)
			return
		}
		title := i18n.T(r, "register_title")
		renderTemplate(w, tmpls.register, tmplData(r, cfg, i18n, title, RegisterPageData{
			Orgs:              orgs,
			FormToken:         tok,
			TelegramAvailable: info.TelegramChannelAvailable,
		}))
	}
}

func registerSubmitHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !registrationEnabled(cfg) {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		if authThrottle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/register", authBlock, ip)
			title := i18n.T(r, "register_title")
			renderTemplate(w, tmpls.register, tmplData(r, cfg, i18n, title, RegisterPageData{
				Error:     "login_error_throttled",
				FormToken: issueFormToken(ip),
			}))
			return
		}
		switch guardFormSubmit(w, r, cfg, ip) {
		case formGuardParseError:
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		case formGuardHoneypot, formGuardBadToken:
			http.Redirect(w, r, "/register/done?ch=email", http.StatusSeeOther)
			return
		}

		regType := r.FormValue("reg_type")
		email := strings.TrimSpace(r.FormValue("email"))
		description := strings.TrimSpace(r.FormValue("description"))
		channel := r.FormValue("channel")
		telegram := strings.TrimSpace(r.FormValue("telegram"))
		orgName := strings.TrimSpace(r.FormValue("org_name"))
		orgActorName := strings.TrimSpace(r.FormValue("org_actor_name"))
		orgDesc := strings.TrimSpace(r.FormValue("org_description"))
		orgWebsite := strings.TrimSpace(r.FormValue("org_website"))
		orgContactEmail := strings.TrimSpace(r.FormValue("org_contact_email"))
		phone2 := r.FormValue(honeypotField)
		if hasPendingSubmission(ip, r.UserAgent()) {
			orgs, info := loadRegisterFormData(r.Context(), client)
			title := i18n.T(r, "register_title")
			renderTemplate(w, tmpls.register, tmplData(r, cfg, i18n, title, RegisterPageData{
				Orgs:              orgs,
				Error:             "register_error_pending",
				TelegramAvailable: info.TelegramChannelAvailable,
				FormToken:         issueFormToken(ip),
			}))
			return
		}

		var orgID *int
		if regType == "join_org" {
			if s := r.FormValue("org_id"); s != "" {
				if id, err := strconv.Atoi(s); err == nil {
					orgID = &id
				}
			}
		}

		req := RegisterReq{
			Email:           email,
			Description:     description,
			RegType:         regType,
			OrgID:           orgID,
			OrgName:         orgName,
			OrgActorName:    orgActorName,
			OrgDescription:  orgDesc,
			OrgWebsite:      orgWebsite,
			OrgContactEmail: orgContactEmail,
			Channel:         channel,
			Telegram:        telegram,
			Phone2:          phone2,
		}

		authThrottle.record(ip)
		setPendingSubmission(ip, r.UserAgent(), stdFormMaxAge(cfg))
		globalEmailSendRate.record()
		result, err := client.Register(r.Context(), req, cfg.publicBaseURL(), getBoardSessionToken(r))
		if err != nil {
			clearPendingSubmission(ip, r.UserAgent())
			log.Printf("dansal-web: register submit failed ip_hash=%s err=%v", hashIP(ip), err)
			errKey := "register_error_other"
			switch classifyAPIError(err) {
			case errConflict:
				errKey = "register_error_conflict"
			case errRateLimited:
				errKey = "register_error_rate"
			default:
				if strings.Contains(apiErrUserMessage(err), "org_id") {
					errKey = "register_error_no_org"
				}
			}
			orgs, info := loadRegisterFormData(r.Context(), client)
			title := i18n.T(r, "register_title")
			renderTemplate(w, tmpls.register, tmplData(r, cfg, i18n, title, RegisterPageData{
				Orgs:              orgs,
				Error:             errKey,
				TelegramAvailable: info.TelegramChannelAvailable,
				FormToken:         issueFormToken(ip),
			}))
			return
		}

		log.Printf("register: new registration for %s (status=%s)", email, result["status"])

		// Set a persistent cookie so the user can resume verification if the tab is closed.
		if pid := result["pending_id"]; pid != "" {
			tok := result["verification_token"]
			http.SetCookie(w, &http.Cookie{
				Name:     pendingRegCookie,
				Value:    pid + ":" + tok,
				Path:     "/",
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteLaxMode,
				Expires:  time.Now().Add(7 * 24 * time.Hour),
			})
		}

		redirectURL := "/register/done?ch=email"
		if result["status"] == "pending_approval" {
			redirectURL = "/register/done?ch=none"
		} else if result["status"] == "telegram_verification_required" {
			redirectURL = "/register/done?ch=telegram&tok=" + result["telegram_token"] + "&bot=" + result["bot_name"]
		}
		http.Redirect(w, r, redirectURL, http.StatusSeeOther)
	}
}

func registerDoneHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channel := r.URL.Query().Get("ch")
		token := r.URL.Query().Get("tok")
		bot := r.URL.Query().Get("bot")
		title := i18n.T(r, "register_done_title")
		renderTemplate(w, tmpls.registerDone, tmplData(r, cfg, i18n, title, RegisterDoneData{
			Channel:       channel,
			TelegramToken: token,
			BotName:       bot,
		}))
	}
}

func registerVerifyHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		pendingID, err := client.VerifyRegistrationEmail(r.Context(), token)
		if err == nil {
			// Land the user straight in onboarding (#1223) -- set the
			// pending_reg cookie here rather than relying on one already
			// set by the original /register submission, since the verify
			// link is very often opened on a different device/browser than
			// the one that submitted the form.
			http.SetCookie(w, &http.Cookie{
				Name:     pendingRegCookie,
				Value:    strconv.Itoa(pendingID) + ":" + token,
				Path:     "/",
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteLaxMode,
				Expires:  time.Now().Add(7 * 24 * time.Hour),
			})
			http.Redirect(w, r, "/register", http.StatusSeeOther)
			return
		}
		title := i18n.T(r, "register_verified_title")
		renderTemplate(w, tmpls.registerVerified, tmplData(r, cfg, i18n, title, false))
	}
}

// registerPasswordSubmitHandler handles POST /register/password: the
// password track of onboarding (#1223), counterpart to the client-side
// WebAuthn ceremony used for the passkey track. Unlike invitePasswordHandler,
// it does not log the user in -- the account stays disabled until an admin
// approves the pending registration.
func registerPasswordSubmitHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(pendingRegCookie)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "no pending registration"})
			return
		}
		parts := strings.SplitN(c.Value, ":", 2)
		if len(parts) != 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "no pending registration"})
			return
		}
		pendingID, err := strconv.Atoi(parts[0])
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "no pending registration"})
			return
		}
		token := parts[1]

		var req struct {
			DisplayName string `json:"display_name"`
			Password    string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid request"})
			return
		}

		if err := client.RegisterSetPassword(r.Context(), pendingID, token, req.DisplayName, req.Password); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": apiErrUserMessage(err)})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "password_set"})
	}
}

func adminRegistrationsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su := getSessionUser(r)
		if su == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		token := getSessionToken(r)
		regs, err := client.ListPendingRegistrations(r.Context(), token)
		if err != nil {
			log.Printf("admin registrations list: %v", err)
			regs = nil
		}
		pendingInvites, ierr := client.ListPendingInvites(r.Context(), token)
		if ierr != nil {
			log.Printf("admin registrations: could not load pending invites: %v", ierr)
		}
		title := i18n.T(r, "admin_registrations_title")
		renderTemplate(w, tmpls.adminRegistrations, tmplData(r, cfg, i18n, title, AdminRegistrationsData{
			Regs:           regs,
			PendingInvites: pendingInvites,
			Flash:          r.URL.Query().Get("flash"),
		}))
	}
}

func adminRegistrationApproveHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su := getSessionUser(r)
		if su == nil || (su.Role != "admin" && su.Role != "user") {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		r.ParseForm()
		role := r.FormValue("role")
		if role == "" {
			role = "user"
		}
		token := getSessionToken(r)
		if err := client.ApproveRegistration(r.Context(), token, id, role); err != nil {
			log.Printf("approve registration %d: %v", id, err)
		}
		http.Redirect(w, r, "/admin/registrations", http.StatusSeeOther)
	}
}

func adminRegistrationResendInviteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su := getSessionUser(r)
		if su == nil || (su.Role != "admin" && su.Role != "user") {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		if err := client.ResendInvite(r.Context(), token, id, cfg.publicBaseURL()); err != nil {
			log.Printf("resend invite %d: %v", id, err)
		}
		http.Redirect(w, r, "/admin/registrations", http.StatusSeeOther)
	}
}

func adminRegistrationRejectHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su := getSessionUser(r)
		if su == nil || (su.Role != "admin" && su.Role != "user") {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		r.ParseForm()
		reason := strings.TrimSpace(r.FormValue("reason"))
		token := getSessionToken(r)
		if err := client.RejectRegistration(r.Context(), token, id, reason); err != nil {
			log.Printf("reject registration %d: %v", id, err)
		}
		http.Redirect(w, r, "/admin/registrations", http.StatusSeeOther)
	}
}

func registerResendHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(pendingRegCookie)
		if err != nil {
			http.Redirect(w, r, "/register", http.StatusSeeOther)
			return
		}
		parts := strings.SplitN(c.Value, ":", 2)
		if len(parts) != 2 {
			http.SetCookie(w, &http.Cookie{Name: pendingRegCookie, MaxAge: -1, Path: "/", Secure: true})
			http.Redirect(w, r, "/register", http.StatusSeeOther)
			return
		}
		tok := parts[1]
		if err := client.ResendRegistration(r.Context(), tok); err != nil {
			log.Printf("register resend: %v", err)
		}
		http.Redirect(w, r, "/register", http.StatusSeeOther)
	}
}

func registerCancelHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(pendingRegCookie)
		if err != nil {
			http.Redirect(w, r, "/register", http.StatusSeeOther)
			return
		}
		parts := strings.SplitN(c.Value, ":", 2)
		if len(parts) == 2 {
			tok := parts[1]
			if err := client.CancelRegistration(r.Context(), tok); err != nil {
				log.Printf("register cancel: %v", err)
			}
		}
		http.SetCookie(w, &http.Cookie{Name: pendingRegCookie, MaxAge: -1, Path: "/", Secure: true})
		http.Redirect(w, r, "/register", http.StatusSeeOther)
	}
}
