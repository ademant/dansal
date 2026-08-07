package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
)

type adminUser struct {
	ID          int    `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	CreatedAt   string `json:"created_at"`
}

type adminSession struct {
	ID         int    `json:"id"`
	UserAgent  string `json:"user_agent"`
	IP         string `json:"ip"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
}

func listAdminUsers(socketPath string) ([]adminUser, error) {
	var users []adminUser
	if err := getSocketData(socketPath, "list-users", &users); err != nil {
		return nil, err
	}
	// filter to admin role only
	var admins []adminUser
	for _, u := range users {
		if u.Role == "admin" {
			admins = append(admins, u)
		}
	}
	return admins, nil
}

func usersPageHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := listAdminUsers(cfg.AdminSocket)
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
			log.Printf("list admin users: %v", err)
		}
		d := tmplData(r, cfg, "Admin Users", map[string]any{
			"Users": users,
			"Error": errMsg,
			"Flash": r.URL.Query().Get("flash"),
		})
		renderTemplate(w, tmpls.users, d)
	}
}

func userSessionsPageHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := r.PathValue("email")
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "list-sessions", Email: email})
		errMsg := ""
		var sessions []adminSession
		if err != nil {
			errMsg = err.Error()
		} else if !resp.OK {
			errMsg = resp.Error
		} else {
			json.Unmarshal(resp.Data, &sessions)
		}
		d := tmplData(r, cfg, "Sessions: "+email, map[string]any{
			"Email":    email,
			"Sessions": sessions,
			"Error":    errMsg,
			"Flash":    r.URL.Query().Get("flash"),
		})
		renderTemplate(w, tmpls.sessions, d)
	}
}

func userRevokeSessionHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := r.PathValue("email")
		sessionID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || sessionID == 0 {
			http.Redirect(w, r, "/users/"+url.PathEscape(email)+"/sessions?flash=invalid+id", http.StatusSeeOther)
			return
		}
		basePath := "/users/" + url.PathEscape(email) + "/sessions"
		if _, ok := socketFlashRedirect(w, r, cfg, basePath, "Error", socketRequest{Cmd: "revoke-session", SessionID: sessionID}); !ok {
			return
		}
		http.Redirect(w, r, basePath+"?flash=Session+revoked", http.StatusSeeOther)
	}
}

// userMagicLinkHandler generates a magic login link, restricted to
// role=admin targets — webmin's only user-management capability besides
// session listing/revocation, per the dansal-webmin sysadmin/business-admin
// boundary (#614).
func userMagicLinkHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := r.PathValue("email")
		w.Header().Set("Content-Type", "application/json")

		// Only admin accounts may receive magic links via webmin.
		su, err := lookupAdminUser(cfg, email)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": "could not verify user role"})
			return
		}
		if su == nil {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "magic links via webmin are only available for admin accounts"})
			return
		}

		resp, err := doSocket(cfg, socketRequest{Cmd: "magic-link", Email: email})
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Write(resp.Data)
	}
}

// userInviteAdminHandler creates an invite link with role=admin, attributed
// to the admin whose row the button was clicked on. Only reachable from
// dansal-webmin (mTLS-gated) — the public /admin/users UI in dansal_web can
// only ever create role=user invites.
func userInviteAdminHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := r.PathValue("email")
		resp, err := doSocket(cfg, socketRequest{Cmd: "invite-admin", Email: email})
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Write(resp.Data)
	}
}
