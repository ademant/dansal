package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
)

// ── Users & Invites ───────────────────────────────────────────────────────────

type AdminUsersData struct {
	IsAdmin        bool
	Users          []UserInfo
	Orgs           []Organization
	OrgMap         map[int]Organization
	UserOrgs       map[int][]int
	MyOrgs         []Organization // orgs the current user belongs to (non-admins: invite target choices)
	Invites        []InviteLink
	BaseURL        string
	NewInviteToken string
}

func adminUsersHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		isAdmin := su.Role == "admin"

		orgs, _ := client.GetOrganizations(r.Context())
		orgMap := make(map[int]Organization, len(orgs))
		for _, o := range orgs {
			orgMap[o.ID] = o
		}

		userOrgs := make(map[int][]int)
		for _, o := range orgs {
			members, err := client.GetOrganizationMembers(r.Context(), o.ID, token)
			if err != nil {
				continue
			}
			for _, m := range members {
				userOrgs[m.UserID] = append(userOrgs[m.UserID], o.ID)
			}
		}

		var users []UserInfo
		if isAdmin {
			users, _ = client.GetAllUsers(r.Context(), token)
		} else {
			seen := make(map[int]bool)
			for _, orgID := range userOrgs[su.ID] {
				members, _ := client.GetOrganizationMembers(r.Context(), orgID, token)
				for _, m := range members {
					if !seen[m.UserID] {
						seen[m.UserID] = true
						users = append(users, UserInfo{ID: m.UserID, Email: m.Email, DisplayName: m.DisplayName})
					}
				}
			}
		}

		invites, _ := client.ListInvites(r.Context(), token)
		active := make([]InviteLink, 0, len(invites))
		for _, inv := range invites {
			if inv.UsedAt == "" {
				active = append(active, inv)
			}
		}

		// Build my-orgs list for the invite form (non-admins can only invite to their own orgs)
		var myOrgs []Organization
		if isAdmin {
			myOrgs = orgs
		} else {
			myOrgSet := orgIDSet(userOrgs[su.ID])
			for _, o := range orgs {
				if myOrgSet[o.ID] {
					myOrgs = append(myOrgs, o)
				}
			}
		}

		title := i18n.T(r, "admin_users_title")
		renderTemplate(w, tmpls.adminUsers, tmplData(r, cfg, i18n, title, AdminUsersData{
			IsAdmin:        isAdmin,
			Users:          users,
			Orgs:           orgs,
			OrgMap:         orgMap,
			UserOrgs:       userOrgs,
			MyOrgs:         myOrgs,
			Invites:        active,
			BaseURL:        cfg.publicBaseURL(),
			NewInviteToken: r.URL.Query().Get("new_invite"),
		}))
	}
}

func adminGenerateMagicLinkHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		link, err := client.GenerateMagicLink(r.Context(), id, getSessionToken(r), cfg.publicBaseURL())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"url": link})
	}
}

func adminUserDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := client.DeleteUser(r.Context(), id, getSessionToken(r)); err != nil {
			log.Printf("admin delete user %d: %v", id, err)
			http.Error(w, "delete failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func adminUserRoleHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = client.UpdateUser(r.Context(), id, map[string]string{"role": r.FormValue("role")}, getSessionToken(r))
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func adminUserOrgHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		userID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		action := r.FormValue("action")
		orgID, err := strconv.Atoi(r.FormValue("org_id"))
		if err != nil {
			http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
			return
		}
		if action == "remove" {
			_ = client.RemoveOrgMember(r.Context(), orgID, userID, token)
		} else {
			_ = client.AddOrgMember(r.Context(), orgID, userID, token)
		}
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func adminUsersBulkHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		action := r.FormValue("action")
		orgID := parseFormOptionalInt(r.Form, "org_id")
		for _, id := range parseFormIDs(r.Form, "user_ids") {
			switch action {
			case "delete":
				_ = client.DeleteUser(r.Context(), id, token)
			case "org":
				if orgID != nil {
					_ = client.AddOrgMember(r.Context(), *orgID, id, token)
				}
			case "role":
				if role := r.FormValue("role"); role != "" {
					_ = client.UpdateUser(r.Context(), id, map[string]string{"role": role}, token)
				}
			}
		}
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func adminUserDisableHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		disabled := r.FormValue("disabled") == "1"
		_ = client.SetUserDisabled(r.Context(), id, disabled, getSessionToken(r))
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func adminUserCreateDirectHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		email := r.FormValue("email")
		password := r.FormValue("password")
		role := r.FormValue("role")
		if role == "" {
			role = "user"
		}
		_, _ = client.CreateUserDirect(r.Context(), email, password, role, getSessionToken(r))
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func adminUserPasswordResetHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		password := r.FormValue("password")
		if password != "" {
			_ = client.SetUserPassword(r.Context(), id, password, getSessionToken(r))
		}
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func adminInviteCreateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		role := r.FormValue("role")
		if role == "" {
			role = "user"
		}
		var orgID *int
		if s := r.FormValue("org_id"); s != "" {
			if id, err := strconv.Atoi(s); err == nil {
				orgID = &id
			}
		}
		if su.Role != "admin" {
			// Non-admin org members may only invite with role "user" to their own org
			role = "user"
			if orgID == nil {
				http.Error(w, "Forbidden: organisation required", http.StatusForbidden)
				return
			}
			if !orgIDSet(getUserOrgIDs(r.Context(), client, su.ID, token))[*orgID] {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}
		link, err := client.CreateInvite(r.Context(), role, orgID, token)
		if err != nil {
			http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/users?new_invite="+link.Token, http.StatusSeeOther)
	}
}

func adminInviteRevokeHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		invToken := r.PathValue("token")
		_ = client.RevokeInvite(r.Context(), invToken, getSessionToken(r))
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}
