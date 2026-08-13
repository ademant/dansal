package main

import (
	"log"
	"net/http"
	"strconv"
	"strings"
)

// ── Admin OIDC Providers ─────────────────────────────────────────────────────
//
// Registered external identity providers (#1095) — instance-wide (org left
// blank) or scoped to a single organization's own IdP (e.g. their
// WordPress site). Admin-only, same pattern as /admin/dances and
// /admin/category-mappings.

type AdminOIDCProvidersData struct {
	Providers []OIDCProvider
	Orgs      []Organization
}

func adminOIDCProvidersHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			forbidden(w, r)
			return
		}
		orgs, _ := client.GetOrganizations(r.Context())
		providers, err := client.GetOIDCProvidersAuthed(r.Context(), getSessionToken(r))
		if err != nil {
			log.Printf("admin oidc providers: %v", err)
		}

		title := i18n.T(r, "admin_oidc_providers_title")
		renderTemplate(w, tmpls.adminOIDCProviders, tmplData(r, cfg, i18n, title, AdminOIDCProvidersData{
			Providers: providers,
			Orgs:      orgs,
		}))
	}
}

func adminOIDCProviderCreateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			forbidden(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		kind := strings.TrimSpace(r.FormValue("kind"))
		if kind == "" {
			kind = "oidc"
		}
		issuerURL := strings.TrimSpace(r.FormValue("issuer_url"))
		clientID := strings.TrimSpace(r.FormValue("client_id"))
		clientSecret := strings.TrimSpace(r.FormValue("client_secret"))
		displayName := strings.TrimSpace(r.FormValue("display_name"))
		var orgID *int
		if s := r.FormValue("org_id"); s != "" {
			if v, err := strconv.Atoi(s); err == nil && v > 0 {
				orgID = &v
			}
		}
		if issuerURL != "" && clientID != "" && clientSecret != "" && displayName != "" {
			if _, err := client.CreateOIDCProvider(r.Context(), orgID, kind, issuerURL, clientID, clientSecret, displayName, getSessionToken(r)); err != nil {
				log.Printf("create oidc provider %q: %v", issuerURL, err)
			}
		}
		http.Redirect(w, r, "/admin/oidc-providers", http.StatusSeeOther)
	}
}

func adminOIDCProviderDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			forbidden(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		id, err := strconv.Atoi(r.FormValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := client.DeleteOIDCProvider(r.Context(), id, getSessionToken(r)); err != nil {
			log.Printf("delete oidc provider %d: %v", id, err)
		}
		http.Redirect(w, r, "/admin/oidc-providers", http.StatusSeeOther)
	}
}
