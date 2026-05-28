package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
)

type adminUser struct {
	ID        int    `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Disabled  bool   `json:"disabled"`
	CreatedAt string `json:"created_at"`
}

type adminSession struct {
	ID         int    `json:"id"`
	UserAgent  string `json:"user_agent"`
	IP         string `json:"ip"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
}

func listAdminUsers(socketPath string) ([]adminUser, error) {
	resp, err := sendSocket(socketPath, socketRequest{Cmd: "list-users"})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	var users []adminUser
	json.Unmarshal(resp.Data, &users)
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
		d := tmplData(cfg, "Admin Users", map[string]any{
			"Users": users,
			"Error": errMsg,
			"Flash": r.URL.Query().Get("flash"),
		})
		d.User = getSessionUser(r)
		renderTemplate(w, tmpls.users, d)
	}
}

func userCreateHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		username := r.FormValue("username")
		email := r.FormValue("email")
		password := r.FormValue("password")
		if username == "" || email == "" || password == "" {
			http.Redirect(w, r, "/users?flash=missing+fields", http.StatusSeeOther)
			return
		}
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{
			Cmd:      "create-user",
			Username: username,
			Email:    email,
			Password: password,
			Role:     "admin",
		})
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			log.Printf("create admin user %q: %v / %s", username, err, msg)
			http.Redirect(w, r, "/users?flash="+url.QueryEscape("Error: "+msg), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/users?flash="+url.QueryEscape("Created "+username), http.StatusSeeOther)
	}
}

func userResetPasswordHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		username := r.PathValue("username")
		password := r.FormValue("password")
		if username == "" || password == "" {
			http.Redirect(w, r, "/users?flash=missing+fields", http.StatusSeeOther)
			return
		}
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{
			Cmd:      "set-password",
			Username: username,
			Password: password,
		})
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			log.Printf("reset password %q: %v / %s", username, err, msg)
			http.Redirect(w, r, "/users?flash="+url.QueryEscape("Error: "+msg), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/users?flash="+url.QueryEscape("Password reset for "+username), http.StatusSeeOther)
	}
}

func userSessionsPageHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.PathValue("username")
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "list-sessions", Username: username})
		errMsg := ""
		var sessions []adminSession
		if err != nil {
			errMsg = err.Error()
		} else if !resp.OK {
			errMsg = resp.Error
		} else {
			json.Unmarshal(resp.Data, &sessions)
		}
		d := tmplData(cfg, "Sessions: "+username, map[string]any{
			"Username": username,
			"Sessions": sessions,
			"Error":    errMsg,
			"Flash":    r.URL.Query().Get("flash"),
		})
		d.User = getSessionUser(r)
		renderTemplate(w, tmpls.sessions, d)
	}
}

func userRevokeSessionHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.PathValue("username")
		sessionID := 0
		fmt.Sscan(r.PathValue("id"), &sessionID)
		if sessionID == 0 {
			http.Redirect(w, r, "/users/"+username+"/sessions?flash=invalid+id", http.StatusSeeOther)
			return
		}
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "revoke-session", SessionID: sessionID})
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			http.Redirect(w, r, "/users/"+username+"/sessions?flash="+url.QueryEscape("Error: "+msg), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/users/"+username+"/sessions?flash=Session+revoked", http.StatusSeeOther)
	}
}

func userMagicLinkHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.PathValue("username")
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "magic-link", Username: username})
		w.Header().Set("Content-Type", "application/json")
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": msg})
			return
		}
		w.Write(resp.Data)
	}
}

func userDeleteHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.PathValue("username")
		// safety: don't allow deleting yourself
		self := getSessionUser(r)
		if self != nil && self.Username == username {
			http.Redirect(w, r, "/users?flash="+url.QueryEscape("Cannot delete your own account"), http.StatusSeeOther)
			return
		}
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "delete-user", Username: username})
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			log.Printf("delete user %q: %v / %s", username, err, msg)
			http.Redirect(w, r, "/users?flash="+url.QueryEscape("Error: "+msg), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/users?flash="+url.QueryEscape("Deleted "+username), http.StatusSeeOther)
	}
}
