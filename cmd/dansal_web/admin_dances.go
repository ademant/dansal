package main

import (
	"log"
	"net/http"
	"strings"
)

// ── Admin Dances ──────────────────────────────────────────────────────────────

type AdminDancesData struct {
	Dances   []Dance
	ErrorMsg string
}

func adminDancesHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireAdmin(w, r)
		if !ok {
			return
		}
		dances, _ := client.GetDances(r.Context())
		title := i18n.T(r, "admin_dances_title")
		renderTemplate(w, tmpls.adminDances, tmplData(r, cfg, i18n, title, AdminDancesData{Dances: dances}))
	}
}

func adminDanceCreateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireAdmin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if name != "" {
			if _, err := client.CreateDance(r.Context(), name, getSessionToken(r)); err != nil {
				log.Printf("create dance %q: %v", name, err)
			}
		}
		http.Redirect(w, r, "/admin/dances", http.StatusSeeOther)
	}
}

// adminDanceEditHandler handles POST /admin/dances/{id}/edit — saves a
// dance's name+description together (#1290; the API's PUT is a full
// replace, so the form always submits both, even though only description
// is editable in the UI today).
func adminDanceEditHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireAdmin(w, r)
		if !ok {
			return
		}
		id, ok := intPathValueOr404(w, r, "id")
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		description := strings.TrimSpace(r.FormValue("description"))
		if name != "" {
			if _, err := client.UpdateDance(r.Context(), id, name, description, getSessionToken(r)); err != nil {
				log.Printf("update dance %d: %v", id, err)
			}
		}
		http.Redirect(w, r, "/admin/dances", http.StatusSeeOther)
	}
}

func adminDanceDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireAdmin(w, r)
		if !ok {
			return
		}
		id, ok := intPathValueOr404(w, r, "id")
		if !ok {
			return
		}
		if err := client.DeleteDance(r.Context(), id, getSessionToken(r)); err != nil {
			log.Printf("delete dance %d: %v", id, err)
		}
		http.Redirect(w, r, "/admin/dances", http.StatusSeeOther)
	}
}
