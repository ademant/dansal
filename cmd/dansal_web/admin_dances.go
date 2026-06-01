package main

import (
	"net/http"
	"strconv"
	"strings"
)

// ── Admin Dances ──────────────────────────────────────────────────────────────

type AdminDancesData struct {
	Dances   []Dance
	ErrorMsg string
}

func adminDancesHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		dances, _ := client.GetDances(r.Context())
		title := i18n.T(r, "admin_dances_title")
		renderTemplate(w, tmpls.adminDances, tmplData(r, cfg, i18n, title, AdminDancesData{Dances: dances}))
	}
}

func adminDanceCreateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if name != "" {
			_, _ = client.CreateDance(r.Context(), name, getSessionToken(r))
		}
		http.Redirect(w, r, "/admin/dances", http.StatusSeeOther)
	}
}

func adminDanceDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = client.DeleteDance(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, "/admin/dances", http.StatusSeeOther)
	}
}
