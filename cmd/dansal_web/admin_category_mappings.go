package main

import (
	"log"
	"net/http"
	"strings"
)

// ── Admin Category Mappings ─────────────────────────────────────────────────
//
// Maps a raw feed category string (iCal CATEGORIES, RSS/Atom <category>, ...)
// to a known tag slug, so feeds whose taxonomy doesn't already match dansal's
// slugs verbatim (e.g. "Bal Folk Termine" -> bal-folk) still get auto-tagged
// on every future fetch. Most mappings are created inline on the
// /admin/events/import preview page; this page is for reviewing/editing the
// full list without re-running an import. Same access as fetch source
// creation: admins and regular users, not publishers (see fetchURL() in
// cmd/dansal/fetchurl.go).

type AdminCategoryMappingsData struct {
	Aliases []CategoryAlias
	Tags    []Tag
}

func adminCategoryMappingsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role == "publisher" {
			forbidden(w, r)
			return
		}
		aliases, err := client.GetCategoryAliases(r.Context())
		if err != nil {
			log.Printf("admin category mappings: %v", err)
		}
		tags, err := client.GetTags(r.Context())
		if err != nil {
			log.Printf("admin category mappings: could not load tags: %v", err)
		}
		title := i18n.T(r, "admin_category_mappings_title")
		renderTemplate(w, tmpls.adminCategoryMappings, tmplData(r, cfg, i18n, title, AdminCategoryMappingsData{
			Aliases: aliases,
			Tags:    tags,
		}))
	}
}

func adminCategoryMappingCreateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role == "publisher" {
			forbidden(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		category := strings.TrimSpace(r.FormValue("category"))
		tagSlug := strings.TrimSpace(r.FormValue("tag_slug"))
		if category != "" && tagSlug != "" {
			if err := client.CreateCategoryAlias(r.Context(), category, tagSlug, getSessionToken(r)); err != nil {
				log.Printf("create category alias %q -> %q: %v", category, tagSlug, err)
			}
		}
		http.Redirect(w, r, "/admin/category-mappings", http.StatusSeeOther)
	}
}

func adminCategoryMappingDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role == "publisher" {
			forbidden(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		category := r.FormValue("category")
		if category == "" {
			http.NotFound(w, r)
			return
		}
		if err := client.DeleteCategoryAlias(r.Context(), category, getSessionToken(r)); err != nil {
			log.Printf("delete category alias %q: %v", category, err)
		}
		http.Redirect(w, r, "/admin/category-mappings", http.StatusSeeOther)
	}
}
