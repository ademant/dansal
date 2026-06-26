package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type InvitePageData struct {
	Token       string
	PresetEmail string
	Expired     bool // true when invite is already used or expired
}

func invitePageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		data := InvitePageData{Token: token}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if info, err := client.GetInviteInfo(ctx, token); err == nil {
			data.PresetEmail = info.PresetEmail
			data.Expired = info.Expired
		} else {
			// Token not found or API error — treat as invalid.
			data.Expired = true
		}

		title := i18n.T(r, "invite_page_title")
		renderTemplate(w, tmpls.invite, tmplData(r, cfg, i18n, title, data))
	}
}

// invitePasswordHandler handles POST /invites/{token}/password.
// It creates the account via the password track and immediately logs the user
// in, so they land on the dashboard rather than being redirected to /login.
func invitePasswordHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		var req struct {
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
			Password    string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid request"})
			return
		}

		ctx := r.Context()
		if err := client.UseInvitePassword(ctx, token, req.Email, req.DisplayName, req.Password); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		redirect := "/login"
		if req.Email != "" && req.Password != "" {
			ip := getClientIP(r)
			if lr, err := client.Login(ctx, req.Email, req.Password, "", ip, r.UserAgent()); err == nil {
				expAt, parseErr := time.Parse(time.RFC3339, lr.ExpiresAt)
				if parseErr != nil {
					expAt = time.Now().Add(24 * time.Hour)
				}
				setSession(w, lr.Token, SessionUser{
					ID:          lr.User.ID,
					Email:       lr.User.Email,
					DisplayName: lr.User.DisplayName,
					Role:        lr.User.Role,
				}, expAt)
				redirect = "/dashboard"
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"redirect": redirect})
	}
}
