package main

import (
	"context"
	"net/http"
	"time"
)

type InvitePageData struct {
	Token          string
	PresetUsername string
	PresetEmail    string
}

func invitePageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		data := InvitePageData{Token: token}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if info, err := client.GetInviteInfo(ctx, token); err == nil {
			data.PresetUsername = info.PresetUsername
			data.PresetEmail = info.PresetEmail
		}

		title := i18n.T(r, "invite_page_title")
		renderTemplate(w, tmpls.invite, tmplData(r, cfg, i18n, title, data))
	}
}
