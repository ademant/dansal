package main

import (
	"log"
	"net/http"
	"strconv"
	"strings"
)

type RegisterPageData struct {
	Orgs      []Organization
	Error     string
	FormToken string
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
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		orgs, _ := client.GetOrganizations(r.Context())
		title := i18n.T(r, "register_title")
		renderTemplate(w, tmpls.register, tmplData(r, cfg, i18n, title, RegisterPageData{Orgs: orgs, FormToken: newFormToken()}))
	}
}

func registerSubmitHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !suggestAvailable(cfg) {
			http.NotFound(w, r)
			return
		}
		ip := getClientIP(r)
		if authThrottle.isBlocked(ip) {
			log.Printf("%s ip=%s path=/register", authBlock, ip)
			title := i18n.T(r, "register_title")
			renderTemplate(w, tmpls.register, tmplData(r, cfg, i18n, title, RegisterPageData{
				Error: "login_error_throttled",
			}))
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		regType := r.FormValue("reg_type")
		email := strings.TrimSpace(r.FormValue("email"))
		description := strings.TrimSpace(r.FormValue("description"))
		channel := r.FormValue("channel")
		telegram := strings.TrimSpace(r.FormValue("telegram"))
		orgName := strings.TrimSpace(r.FormValue("org_name"))
		orgDesc := strings.TrimSpace(r.FormValue("org_description"))
		orgWebsite := strings.TrimSpace(r.FormValue("org_website"))
		orgContactEmail := strings.TrimSpace(r.FormValue("org_contact_email"))
		phone2 := r.FormValue("phone2")

		if phone2 != "" || !validFormToken(r.FormValue("_form_ts"), cfg.MinSubmitSecs) {
			http.Redirect(w, r, "/register/done?ch=email", http.StatusSeeOther)
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
			OrgDescription:  orgDesc,
			OrgWebsite:      orgWebsite,
			OrgContactEmail: orgContactEmail,
			Channel:         channel,
			Telegram:        telegram,
			Phone2:          phone2,
		}

		authThrottle.record(ip)
		result, err := client.Register(r.Context(), req, cfg.publicBaseURL())
		if err != nil {
			errKey := "register_error_other"
			msg := err.Error()
			if strings.Contains(msg, " 409") || strings.Contains(msg, "already") {
				errKey = "register_error_conflict"
			} else if strings.Contains(msg, " 429") || strings.Contains(msg, "Rate limit") {
				errKey = "register_error_rate"
			}
			orgs, _ := client.GetOrganizations(r.Context())
			title := i18n.T(r, "register_title")
			renderTemplate(w, tmpls.register, tmplData(r, cfg, i18n, title, RegisterPageData{
				Orgs:  orgs,
				Error: errKey,
			}))
			return
		}

		log.Printf("register: new registration for %s (status=%s)", email, result["status"])

		redirectURL := "/register/done?ch=email"
		if result["status"] == "telegram_verification_required" {
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
		err := client.VerifyRegistrationEmail(r.Context(), token)
		success := err == nil
		title := i18n.T(r, "register_verified_title")
		renderTemplate(w, tmpls.registerVerified, tmplData(r, cfg, i18n, title, success))
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
		pendingInvites, _ := client.ListPendingInvites(r.Context(), token)
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
		if su == nil || (su.Role != "admin" && su.Role != "publisher") {
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
		if su == nil || su.Role != "admin" {
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
		if su == nil || (su.Role != "admin" && su.Role != "publisher") {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		if err := client.RejectRegistration(r.Context(), token, id); err != nil {
			log.Printf("reject registration %d: %v", id, err)
		}
		http.Redirect(w, r, "/admin/registrations", http.StatusSeeOther)
	}
}
