package main

import "net/http"

type InvitePageData struct {
	Token string
}

func invitePageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := i18n.T(r, "invite_page_title")
		renderTemplate(w, tmpls.invite, tmplData(r, cfg, i18n, title, InvitePageData{
			Token: r.PathValue("token"),
		}))
	}
}
