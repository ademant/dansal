package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
)

// ── Users & Invites ───────────────────────────────────────────────────────────

type AdminUsersData struct {
	IsAdmin          bool
	Users            []UserInfo
	Orgs             []Organization
	OrgMap           map[int]Organization
	UserOrgs         map[int][]int
	MyOrgs           []Organization // orgs the current user belongs to (non-admins: invite target choices)
	MyOrgIDs         map[int]bool   // set form of MyOrgs, for permission checks in templates
	Invites          []InviteLink
	APIKeys          map[int]APIKey // userID → their API key (for publishers)
	BaseURL          string
	NewInviteToken   string
	PreselectedOrgID int // first org in sorted MyOrgs for non-admins; 0 for admins
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

		orgIDs := make([]int, len(orgs))
		for i, o := range orgs {
			orgIDs[i] = o.ID
		}
		bulkMembers, _ := client.GetOrganizationMembersBulk(r.Context(), orgIDs, token)

		userOrgs := make(map[int][]int)
		for orgID, members := range bulkMembers {
			for _, m := range members {
				userOrgs[m.UserID] = append(userOrgs[m.UserID], orgID)
			}
		}

		var users []UserInfo
		if isAdmin {
			users, _ = client.GetAllUsers(r.Context(), token)
		} else {
			seen := make(map[int]bool)
			for _, orgID := range userOrgs[su.ID] {
				for _, m := range bulkMembers[orgID] {
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

		// Build my-orgs list for the invite form, sorted by actor_name (fallback to name).
		orgSortKey := func(o Organization) string {
			if o.ActorName != "" {
				return o.ActorName
			}
			return o.Name
		}
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

		sort.Slice(myOrgs, func(i, j int) bool {
			return orgSortKey(myOrgs[i]) < orgSortKey(myOrgs[j])
		})
		preselectedOrgID := 0
		if !isAdmin && len(myOrgs) > 0 {
			preselectedOrgID = myOrgs[0].ID
		}
		myOrgIDs := make(map[int]bool, len(myOrgs))
		for _, o := range myOrgs {
			myOrgIDs[o.ID] = true
		}

		// Build userID→APIKey map for publisher rows.
		apiKeys := make(map[int]APIKey)
		if allKeys, err := client.ListAPIKeys(r.Context(), token); err == nil {
			for _, k := range allKeys {
				apiKeys[k.UserID] = k
			}
		}

		title := i18n.T(r, "admin_users_title")
		renderTemplate(w, tmpls.adminUsers, tmplData(r, cfg, i18n, title, AdminUsersData{
			IsAdmin:          isAdmin,
			Users:            users,
			Orgs:             orgs,
			OrgMap:           orgMap,
			UserOrgs:         userOrgs,
			MyOrgs:           myOrgs,
			MyOrgIDs:         myOrgIDs,
			Invites:          active,
			APIKeys:          apiKeys,
			BaseURL:          cfg.publicBaseURL(),
			NewInviteToken:   r.URL.Query().Get("new_invite"),
			PreselectedOrgID: preselectedOrgID,
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
		// Non-admins may only add/remove members of orgs they themselves belong to.
		if su.Role != "admin" && !orgIDSet(getUserOrgIDs(r.Context(), client, su.ID, token))[orgID] {
			http.Error(w, "Forbidden", http.StatusForbidden)
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

func adminUserTelegramMessageHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		message := r.FormValue("message")
		if message == "" {
			http.Error(w, "message required", http.StatusBadRequest)
			return
		}
		if err := client.SendTelegramMessageToUser(r.Context(), id, message, getSessionToken(r)); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func adminInviteCreateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		var req struct {
			InviteType string `json:"type"`
			OrgID      *int   `json:"org_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		if req.InviteType != "qr" && req.InviteType != "link" {
			req.InviteType = "link"
		}
		token := getSessionToken(r)
		if su.Role != "admin" {
			if req.OrgID == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": "organisation required"})
				return
			}
			if !orgIDSet(getUserOrgIDs(r.Context(), client, su.ID, token))[*req.OrgID] {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": "forbidden"})
				return
			}
		}
		link, err := client.CreateInvite(r.Context(), req.InviteType, req.OrgID, token)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      link.Token,
			"type":       link.InviteType,
			"expires_at": link.ExpiresAt,
			"url":        cfg.publicBaseURL() + "/invites/" + link.Token,
		})
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

// adminInviteResendHandler regenerates an active invite: it revokes the old
// token and issues a fresh one with the same type and org, since these
// link/QR invites aren't tied to an email address to actually resend to.
func adminInviteResendHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		invToken := r.PathValue("token")

		invites, err := client.ListInvites(r.Context(), token)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		var old *InviteLink
		for i := range invites {
			if invites[i].Token == invToken {
				old = &invites[i]
				break
			}
		}
		if old == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "invite not found"})
			return
		}
		if su.Role != "admin" && (old.OrgID == nil || !orgIDSet(getUserOrgIDs(r.Context(), client, su.ID, token))[*old.OrgID]) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "forbidden"})
			return
		}

		inviteType := old.InviteType
		if inviteType != "qr" && inviteType != "link" {
			inviteType = "link"
		}
		link, err := client.CreateInvite(r.Context(), inviteType, old.OrgID, token)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = client.RevokeInvite(r.Context(), invToken, token)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      link.Token,
			"type":       link.InviteType,
			"expires_at": link.ExpiresAt,
			"url":        cfg.publicBaseURL() + "/invites/" + link.Token,
		})
	}
}

func adminPublisherCreateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		var req struct {
			Name  string `json:"name"`
			OrgID *int   `json:"org_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		if su.Role != "admin" && req.OrgID == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "organisation required"})
			return
		}
		pub, err := client.CreatePublisher(r.Context(), req.Name, req.OrgID, token)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pub)
	}
}

func adminPublisherRegenerateKeyHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		newKey, keyID, err := client.RegeneratePublisherKey(r.Context(), id, getSessionToken(r))
		if err != nil {
			log.Printf("regenerate publisher key %d: %v", id, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"api_key": newKey, "key_id": keyID})
	}
}

func adminPublisherInviteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		var req struct {
			OrgID *int `json:"org_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OrgID == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "org_id required"})
			return
		}
		token := getSessionToken(r)
		if su.Role != "admin" {
			if !orgIDSet(getUserOrgIDs(r.Context(), client, su.ID, token))[*req.OrgID] {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": "forbidden"})
				return
			}
		}
		link, err := client.CreatePublisherInvite(r.Context(), *req.OrgID, token)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		redeemURL := cfg.publicBaseURL() + "/api/v1/invites/" + link.Token + "/publisher"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      link.Token,
			"redeem_url": redeemURL,
			"expires_at": link.ExpiresAt,
		})
	}
}

func adminPublisherDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		if err := client.DeletePublisher(r.Context(), id, getSessionToken(r)); err != nil {
			log.Printf("delete publisher %d: %v", id, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
