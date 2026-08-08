package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ── Fetch sources ─────────────────────────────────────────────────────────────

type AdminFetchurlsData struct {
	Sources []FetchSource
	OrgMap  map[int]Organization
	Orgs    []Organization
	IsAdmin bool
}

type AdminFetchurlEditData struct {
	Source             FetchSource
	Orgs               []Organization
	OrgMap             map[int]Organization
	Dances             []Dance
	SelectedDanceNames map[string]bool
	Templates          []EventTemplate
	ErrorKey           string
	KuferKeywords      string
	KuferSearchURL     string
	KuferSearchMethod  string
}

// kuferEditFields extracts the display fields for the edit form from a
// source's stored kufer_config JSON.
func kuferEditFields(src FetchSource) (keywords, searchURL, searchMethod string) {
	if src.KuferConfig == "" {
		return "", "", ""
	}
	var cfg KuferConfig
	if json.Unmarshal([]byte(src.KuferConfig), &cfg) != nil {
		return "", "", ""
	}
	return strings.Join(cfg.Keywords, ", "), cfg.SearchURL, cfg.SearchMethod
}

func adminFetchurlsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		sources, err := client.GetFetchSources(r.Context(), token)
		if err != nil {
			http.Error(w, "could not load feed sources", http.StatusBadGateway)
			return
		}
		orgs, _ := client.GetOrganizations(r.Context())
		orgMap := buildOrgMap(orgs)
		isAdmin := su.Role == "admin"
		if !isAdmin {
			sources = filterByUserOrgs(sources, func(s FetchSource) []int {
				if s.OrganizationID != nil {
					return []int{*s.OrganizationID}
				}
				return nil
			}, memberOrgSet(r, client, su))
		}
		title := i18n.T(r, "admin_fetchurls_title")
		renderTemplate(w, tmpls.adminFetchurls, tmplData(r, cfg, i18n, title, AdminFetchurlsData{
			Sources: sources,
			OrgMap:  orgMap,
			Orgs:    orgs,
			IsAdmin: isAdmin,
		}))
	}
}

type AdminFetchurlNewData struct {
	Orgs              []Organization
	Templates         []EventTemplate
	ErrorKey          string
	URL               string
	Type              string
	OrgID             int
	TemplateID        int
	TemplateMode      string
	FromImport        bool
	CreatedAfter      string
	KuferKeywords     string
	KuferSearchURL    string
	KuferSearchMethod string
}

// kuferConfigFromForm builds the JSON stored in fetch_sources.kufer_config
// from the admin form's keyword textfield + optional manual override fields.
func kuferConfigFromForm(r *http.Request) string {
	rawKeywords := strings.TrimSpace(r.FormValue("kufer_keywords"))
	if rawKeywords == "" {
		return ""
	}
	var keywords []string
	for _, kw := range strings.Split(rawKeywords, ",") {
		if kw = strings.TrimSpace(kw); kw != "" {
			keywords = append(keywords, kw)
		}
	}
	cfg := KuferConfig{
		Keywords:     keywords,
		SearchURL:    strings.TrimSpace(r.FormValue("kufer_search_url")),
		SearchMethod: r.FormValue("kufer_search_method"),
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		log.Printf("could not marshal kufer config: %v", err)
		return ""
	}
	return string(b)
}

func adminFetchurlNewPageHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		orgs, err := client.GetOrganizations(r.Context())
		if err != nil {
			log.Printf("admin fetchurl: could not load organizations: %v", err)
		}
		if su.Role != "admin" {
			userOrgSet := memberOrgSet(r, client, su)
			var filtered []Organization
			for _, o := range orgs {
				if userOrgSet[o.ID] {
					filtered = append(filtered, o)
				}
			}
			orgs = filtered
		}
		orgID, _ := strconv.Atoi(r.URL.Query().Get("org_id"))
		orgIDs := orgIDsForTemplates(r, client, su)
		templates, err := listTemplates(db, su.ID, orgIDs)
		if err != nil {
			log.Printf("admin fetchurl: could not load templates: %v", err)
		}
		title := i18n.T(r, "fetch_new_title")
		renderTemplate(w, tmpls.adminFetchurlNew, tmplData(r, cfg, i18n, title, AdminFetchurlNewData{
			Orgs:         orgs,
			Templates:    templates,
			URL:          r.URL.Query().Get("url"),
			Type:         r.URL.Query().Get("type"),
			OrgID:        orgID,
			FromImport:   r.URL.Query().Get("from_import") != "" || r.URL.Query().Get("created_after") != "",
			CreatedAfter: r.URL.Query().Get("created_after"),
		}))
	}
}

func adminFetchurlNewPostHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		rawURL := strings.TrimSpace(r.FormValue("url"))
		typ := r.FormValue("type")
		rawTags := strings.TrimSpace(r.FormValue("tags"))
		var tags []string
		for _, t := range strings.FieldsFunc(rawTags, func(r rune) bool { return r == ',' || r == ' ' }) {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		var orgID *int
		if v := r.FormValue("organization_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				orgID = &n
			}
		}
		if su.Role != "admin" {
			if orgID == nil {
				forbidden(w, r)
				return
			}
			if !memberOrgSet(r, client, su)[*orgID] {
				forbidden(w, r)
				return
			}
		}
		var templateID *int
		templateMode := r.FormValue("template_mode")
		if v := r.FormValue("template_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				templateID = &n
			}
		}
		if templateID == nil {
			templateMode = ""
		}
		createdAfter := r.FormValue("created_after")
		kuferConfig := kuferConfigFromForm(r)
		token := getSessionToken(r)
		if _, err := client.CreateFetchSource(r.Context(), rawURL, typ, tags, orgID, templateID, templateMode, fetchTemplateData(db, templateID), kuferConfig, token); err != nil {
			orgs, _ := client.GetOrganizations(r.Context())
			orgIDInt := 0
			if orgID != nil {
				orgIDInt = *orgID
			}
			templates, _ := listTemplates(db, su.ID, orgIDsForTemplates(r, client, su))
			tplIDInt := 0
			if templateID != nil {
				tplIDInt = *templateID
			}
			title := i18n.T(r, "fetch_new_title")
			renderTemplate(w, tmpls.adminFetchurlNew, tmplData(r, cfg, i18n, title, AdminFetchurlNewData{
				Orgs:              orgs,
				Templates:         templates,
				ErrorKey:          "fetch_add_error",
				URL:               rawURL,
				Type:              typ,
				OrgID:             orgIDInt,
				TemplateID:        tplIDInt,
				TemplateMode:      templateMode,
				FromImport:        createdAfter != "",
				CreatedAfter:      createdAfter,
				KuferKeywords:     r.FormValue("kufer_keywords"),
				KuferSearchURL:    r.FormValue("kufer_search_url"),
				KuferSearchMethod: r.FormValue("kufer_search_method"),
			}))
			return
		}
		if createdAfter != "" {
			q := url.Values{}
			q.Set("created_after", createdAfter)
			q.Set("include_past", "1")
			http.Redirect(w, r, "/admin/events?"+q.Encode(), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/fetchurls", http.StatusSeeOther)
	}
}

func adminFetchurlEditPageHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		src, err := client.GetFetchSource(r.Context(), id, token)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		orgs, _ := client.GetOrganizations(r.Context())
		orgMap := buildOrgMap(orgs)
		if su.Role != "admin" {
			userOrgSet := memberOrgSet(r, client, su)
			if src.OrganizationID == nil || !userOrgSet[*src.OrganizationID] {
				forbidden(w, r)
				return
			}
		}
		orgIDs := orgIDsForTemplates(r, client, su)
		dances, _ := client.GetDances(r.Context())
		selected := buildSelectedDanceNamesFromIDs(src.DanceIDs, dances)
		templates, _ := listTemplates(db, su.ID, orgIDs)
		title := i18n.T(r, "admin_edit")
		kuferKw, kuferURL, kuferMethod := kuferEditFields(src)
		renderTemplate(w, tmpls.adminFetchurlEdit, tmplData(r, cfg, i18n, title, AdminFetchurlEditData{
			Source:             src,
			Orgs:               orgs,
			OrgMap:             orgMap,
			Dances:             dances,
			SelectedDanceNames: selected,
			Templates:          templates,
			KuferKeywords:      kuferKw,
			KuferSearchURL:     kuferURL,
			KuferSearchMethod:  kuferMethod,
		}))
	}
}

func adminFetchurlSaveHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
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

		typ := r.FormValue("type")
		rawTags := strings.TrimSpace(r.FormValue("tags"))
		var tags []string
		for _, t := range strings.FieldsFunc(rawTags, func(r rune) bool { return r == ',' || r == ' ' }) {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		var orgID *int
		if v := r.FormValue("organization_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				orgID = &n
			}
		}

		token := getSessionToken(r)
		if su.Role != "admin" {
			userOrgSet := memberOrgSet(r, client, su)
			// Verify they own the source's current org
			existing, ferr := client.GetFetchSource(r.Context(), id, token)
			if ferr != nil || existing.OrganizationID == nil || !userOrgSet[*existing.OrganizationID] {
				forbidden(w, r)
				return
			}
			// Also verify the new org (if changed) is still theirs
			if orgID == nil || !userOrgSet[*orgID] {
				forbidden(w, r)
				return
			}
		}

		var danceIDs []int
		for _, v := range r.Form["dance_ids"] {
			if n, err2 := strconv.Atoi(v); err2 == nil {
				danceIDs = append(danceIDs, n)
			}
		}

		var templateID *int
		if v := r.FormValue("template_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				templateID = &n
			}
		}
		templateMode := r.FormValue("template_mode")
		kuferConfig := kuferConfigFromForm(r)

		if err := client.UpdateFetchSource(r.Context(), id, typ, tags, danceIDs, orgID, templateID, templateMode, fetchTemplateData(db, templateID), kuferConfig, token); err != nil {
			src, _ := client.GetFetchSource(r.Context(), id, token)
			orgs, _ := client.GetOrganizations(r.Context())
			orgMap := buildOrgMap(orgs)
			dances, _ := client.GetDances(r.Context())
			selected := buildSelectedDanceNamesFromIDs(danceIDs, dances)
			templates, _ := listTemplates(db, su.ID, orgIDsForTemplates(r, client, su))
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, tmpls.adminFetchurlEdit, tmplData(r, cfg, i18n, title, AdminFetchurlEditData{
				Source:             src,
				Orgs:               orgs,
				OrgMap:             orgMap,
				Dances:             dances,
				SelectedDanceNames: selected,
				Templates:          templates,
				ErrorKey:           "admin_save_error",
				KuferKeywords:      r.FormValue("kufer_keywords"),
				KuferSearchURL:     r.FormValue("kufer_search_url"),
				KuferSearchMethod:  r.FormValue("kufer_search_method"),
			}))
			return
		}
		http.Redirect(w, r, "/admin/fetchurls", http.StatusSeeOther)
	}
}

func adminFetchurlDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		if su.Role != "admin" {
			src, ferr := client.GetFetchSource(r.Context(), id, token)
			if ferr != nil || src.OrganizationID == nil || !memberOrgSet(r, client, su)[*src.OrganizationID] {
				forbidden(w, r)
				return
			}
		}
		if err := client.DeleteFetchSource(r.Context(), id, token); err != nil {
			log.Printf("delete fetch source %d: %v", id, err)
		}
		http.Redirect(w, r, "/admin/fetchurls", http.StatusSeeOther)
	}
}

func adminFetchurlRunHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		result, runErr := client.RunFetchSource(r.Context(), id, getSessionToken(r))
		if r.Header.Get("Accept") == "application/json" {
			w.Header().Set("Content-Type", "application/json")
			if runErr != nil {
				writeJSONError(w, r, http.StatusBadGateway, runErr.Error())
				return
			}
			json.NewEncoder(w).Encode(map[string]int{
				"count":     result.Count,
				"new":       result.New,
				"updated":   result.Updated,
				"unchanged": result.Unchanged,
			})
			return
		}
		http.Redirect(w, r, "/admin/fetchurls", http.StatusSeeOther)
	}
}

func adminFetchurlBulkHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ids := parseFormIDs(r.Form, "src_ids")
		if len(ids) == 0 {
			http.Redirect(w, r, "/admin/fetchurls", http.StatusSeeOther)
			return
		}
		token := getSessionToken(r)
		action := r.FormValue("bulk_action")
		switch action {
		case "delete":
			if user.Role != "admin" {
				forbidden(w, r)
				return
			}
			if err := client.BulkDeleteFetchSources(r.Context(), ids, token); err != nil {
				log.Printf("bulk delete fetch sources: %v", err)
			}
		case "run":
			if err := client.BulkRunFetchSources(r.Context(), ids, token); err != nil {
				log.Printf("bulk run fetch sources: %v", err)
			}
		case "assign-org":
			if user.Role != "admin" {
				forbidden(w, r)
				return
			}
			if err := client.BulkAssignFetchSourceOrg(r.Context(), ids, parseFormOptionalInt(r.Form, "organization_id"), token); err != nil {
				log.Printf("bulk assign fetch source org: %v", err)
			}
		case "add-tag":
			newTag := strings.TrimSpace(r.FormValue("new_tag"))
			if newTag != "" {
				idSet := make(map[int]bool, len(ids))
				for _, id := range ids {
					idSet[id] = true
				}
				sources, err := client.GetFetchSources(r.Context(), token)
				if err == nil {
					for _, src := range sources {
						if !idSet[src.ID] {
							continue
						}
						hasTag := false
						for _, t := range src.Tags {
							if t == newTag {
								hasTag = true
								break
							}
						}
						if !hasTag {
							newTags := append(src.Tags, newTag)
							if err := client.UpdateFetchSource(r.Context(), src.ID, src.Type, newTags, src.DanceIDs, src.OrganizationID, src.TemplateID, src.TemplateMode, src.TemplateData, src.KuferConfig, token); err != nil {
								log.Printf("bulk add tag %q to fetch source %d: %v", newTag, src.ID, err)
							}
						}
					}
				}
			}
		}
		http.Redirect(w, r, "/admin/fetchurls", http.StatusSeeOther)
	}
}
