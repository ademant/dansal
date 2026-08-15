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

		orgs, oErr := client.GetOrganizations(r.Context())
		if oErr != nil {
			log.Printf("admin users: could not load organizations: %v", oErr)
		}
		orgMap := buildOrgMap(orgs)

		orgIDs := make([]int, len(orgs))
		for i, o := range orgs {
			orgIDs[i] = o.ID
		}
		bulkMembers, bErr := client.GetOrganizationMembersBulk(r.Context(), orgIDs, token)
		if bErr != nil {
			log.Printf("admin users: could not load memberships: %v", bErr)
		}

		userOrgs := make(map[int][]int)
		for orgID, members := range bulkMembers {
			for _, m := range members {
				userOrgs[m.UserID] = append(userOrgs[m.UserID], orgID)
			}
		}

		var users []UserInfo
		if isAdmin {
			var uErr error
			users, uErr = client.GetAllUsers(r.Context(), token)
			if uErr != nil {
				logHTTPError(w, r, "could not load users", http.StatusBadGateway)
				return
			}
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

		invites, iErr := client.ListInvites(r.Context(), token)
		if iErr != nil {
			log.Printf("admin users: could not load invites: %v", iErr)
		}
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
			forbidden(w, r)
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
			forbidden(w, r)
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
		if err := client.UpdateUser(r.Context(), id, map[string]string{"role": r.FormValue("role")}, getSessionToken(r)); err != nil {
			log.Printf("set role of user %d: %v", id, err)
		}
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
		if su.Role != "admin" && !memberOrgSet(r, client, su)[orgID] {
			forbidden(w, r)
			return
		}
		if action == "remove" {
			if err := client.RemoveOrgMember(r.Context(), orgID, userID, token); err != nil {
				log.Printf("remove user %d from org %d: %v", userID, orgID, err)
			}
		} else {
			if err := client.AddOrgMember(r.Context(), orgID, userID, token); err != nil {
				log.Printf("add user %d to org %d: %v", userID, orgID, err)
			}
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
			forbidden(w, r)
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
					if err := client.AddOrgMember(r.Context(), *orgID, id, token); err != nil {
						log.Printf("bulk add user %d to org %d: %v", id, *orgID, err)
					}
				}
			case "role":
				if role := r.FormValue("role"); role != "" {
					if err := client.UpdateUser(r.Context(), id, map[string]string{"role": role}, token); err != nil {
						log.Printf("bulk set role of user %d: %v", id, err)
					}
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
			forbidden(w, r)
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
		if err := client.SetUserDisabled(r.Context(), id, disabled, getSessionToken(r)); err != nil {
			log.Printf("set disabled=%t for user %d: %v", disabled, id, err)
		}
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
			forbidden(w, r)
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
			writeJSONError(w, r, http.StatusBadRequest, "bad request")
			return
		}
		if req.InviteType != "qr" && req.InviteType != "link" {
			req.InviteType = "link"
		}
		token := getSessionToken(r)
		if su.Role != "admin" {
			if req.OrgID == nil {
				writeJSONError(w, r, http.StatusForbidden, "organisation required")
				return
			}
			if !memberOrgSet(r, client, su)[*req.OrgID] {
				forbidden(w, r)
				return
			}
		}
		link, err := client.CreateInvite(r.Context(), req.InviteType, req.OrgID, token)
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, err.Error())
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
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		invToken := r.PathValue("token")
		// Same org check as adminInviteCreateHandler/adminInviteResendHandler
		// applied to the invite being revoked (#1110).
		if su.Role != "admin" {
			invites, err := client.ListInvites(r.Context(), token)
			if err != nil {
				writeJSONError(w, r, http.StatusBadGateway, err.Error())
				return
			}
			var target *InviteLink
			for i := range invites {
				if invites[i].Token == invToken {
					target = &invites[i]
					break
				}
			}
			if target == nil || target.OrgID == nil || !memberOrgSet(r, client, su)[*target.OrgID] {
				forbidden(w, r)
				return
			}
		}
		if err := client.RevokeInvite(r.Context(), invToken, token); err != nil {
			log.Printf("revoke invite %s: %v", invToken, err)
		}
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
			writeJSONError(w, r, http.StatusBadGateway, err.Error())
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
			writeJSONError(w, r, http.StatusNotFound, "invite not found")
			return
		}
		if su.Role != "admin" && (old.OrgID == nil || !memberOrgSet(r, client, su)[*old.OrgID]) {
			forbidden(w, r)
			return
		}

		inviteType := old.InviteType
		if inviteType != "qr" && inviteType != "link" {
			inviteType = "link"
		}
		link, err := client.CreateInvite(r.Context(), inviteType, old.OrgID, token)
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, err.Error())
			return
		}
		if err := client.RevokeInvite(r.Context(), invToken, token); err != nil {
			log.Printf("revoke invite %s: %v", invToken, err)
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
			writeJSONError(w, r, http.StatusBadRequest, "bad request")
			return
		}
		token := getSessionToken(r)
		if su.Role != "admin" && req.OrgID == nil {
			writeJSONError(w, r, http.StatusForbidden, "organisation required")
			return
		}
		pub, err := client.CreatePublisher(r.Context(), req.Name, req.OrgID, token)
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pub)
	}
}

// publisherInCallerOrgs reports whether publisherID (a user ID) shares an org
// with the caller — used by non-admins acting on a publisher (#1110).
func publisherInCallerOrgs(r *http.Request, client *DansalClient, su *SessionUser, publisherID int, token string) bool {
	if su.Role == "admin" {
		return true
	}
	pubOrgIDs, err := client.GetUserOrganizationIDs(r.Context(), publisherID, token)
	if err != nil {
		return false
	}
	memberSet := memberOrgSet(r, client, su)
	for _, oid := range pubOrgIDs {
		if memberSet[oid] {
			return true
		}
	}
	return false
}

func adminPublisherRegenerateKeyHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, r, http.StatusBadRequest, "invalid id")
			return
		}
		token := getSessionToken(r)
		if !publisherInCallerOrgs(r, client, su, id, token) {
			forbidden(w, r)
			return
		}
		newKey, keyID, err := client.RegeneratePublisherKey(r.Context(), id, token)
		if err != nil {
			log.Printf("regenerate publisher key %d: %v", id, err)
			writeJSONError(w, r, http.StatusBadGateway, err.Error())
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
			writeJSONError(w, r, http.StatusBadRequest, "org_id required")
			return
		}
		token := getSessionToken(r)
		if su.Role != "admin" && !memberOrgSet(r, client, su)[*req.OrgID] {
			forbidden(w, r)
			return
		}
		link, err := client.CreatePublisherInvite(r.Context(), *req.OrgID, token)
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, err.Error())
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
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, r, http.StatusBadRequest, "invalid id")
			return
		}
		token := getSessionToken(r)
		if !publisherInCallerOrgs(r, client, su, id, token) {
			forbidden(w, r)
			return
		}
		if err := client.DeletePublisher(r.Context(), id, token); err != nil {
			log.Printf("delete publisher %d: %v", id, err)
			writeJSONError(w, r, http.StatusBadGateway, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
