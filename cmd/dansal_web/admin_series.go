package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// ── Data structs ──────────────────────────────────────────────────────────────

type AdminSeriesData struct {
	Series  []EventSeries
	IsAdmin bool
}

type AdminSeriesEditData struct {
	Series    EventSeries
	Locations []Location
	Orgs      []Organization
	IsAdmin   bool
	BaseURL   string
	ErrorKey  string
}

type AdminSeriesNewData struct {
	Locations []Location
	Orgs      []Organization
	IsAdmin   bool
	ErrorKey  string
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func adminSeriesListHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		series, err := client.GetSeriesList(r.Context(), token)
		if err != nil {
			http.Error(w, "could not load series: "+err.Error(), http.StatusBadGateway)
			return
		}
		title := i18n.T(r, "series_title")
		renderTemplate(w, tmpls.adminSeries, tmplData(r, cfg, i18n, title, AdminSeriesData{
			Series:  series,
			IsAdmin: user.Role == "admin",
		}))
	}
}

func adminSeriesNewPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		locs, _ := client.GetLocations(r.Context())
		orgs, _ := client.GetOrganizations(r.Context())
		title := i18n.T(r, "series_new")
		renderTemplate(w, tmpls.adminSeriesNew, tmplData(r, cfg, i18n, title, AdminSeriesNewData{
			Locations: locs,
			Orgs:      orgs,
			IsAdmin:   user.Role == "admin",
		}))
	}
}

func adminSeriesCreateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)

		body := map[string]any{
			"title":               strings.TrimSpace(r.FormValue("title")),
			"description":         strings.TrimSpace(r.FormValue("description")),
			"default_start_time":  r.FormValue("default_start_time"),
			"default_end_time":    r.FormValue("default_end_time"),
			"start_date":          r.FormValue("start_date"),
			"recurrence":          r.FormValue("recurrence"),
			"end_date":            r.FormValue("end_date"),
		}

		if v := r.FormValue("organization_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				body["organization_id"] = n
			}
		}
		if v := r.FormValue("default_location_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				body["default_location_id"] = n
			}
		}
		if v := r.FormValue("occurrences"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				body["occurrences"] = n
			}
		}
		_ = user

		created, err := client.CreateSeries(r.Context(), body, token)
		if err != nil {
			http.Error(w, "failed to create series: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/series/%d", created.ID), http.StatusSeeOther)
	}
}

func adminSeriesEditPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		series, err := client.GetSeriesByID(r.Context(), id, token)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		locs, _ := client.GetLocations(r.Context())
		orgs, _ := client.GetOrganizations(r.Context())
		title := i18n.T(r, "series_edit")
		renderTemplate(w, tmpls.adminSeriesEdit, tmplData(r, cfg, i18n, title, AdminSeriesEditData{
			Series:    series,
			Locations: locs,
			Orgs:      orgs,
			IsAdmin:   user.Role == "admin",
			BaseURL:   cfg.publicBaseURL(),
		}))
	}
}

func adminSeriesSaveHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		body := map[string]any{
			"title":              strings.TrimSpace(r.FormValue("title")),
			"description":        strings.TrimSpace(r.FormValue("description")),
			"default_start_time": r.FormValue("default_start_time"),
			"default_end_time":   r.FormValue("default_end_time"),
		}
		if v := r.FormValue("default_location_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				body["default_location_id"] = n
			}
		}
		if v := r.FormValue("organization_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				body["organization_id"] = n
			}
		}
		if err := client.UpdateSeries(r.Context(), id, body, token); err != nil {
			http.Error(w, "failed to save: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/series/%d", id), http.StatusSeeOther)
	}
}

func adminSeriesDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		token := getSessionToken(r)
		_ = client.DeleteSeries(r.Context(), id, token)
		http.Redirect(w, r, "/admin/series", http.StatusSeeOther)
	}
}

func adminSeriesAddDateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		body := map[string]any{
			"date": r.FormValue("date"),
		}
		if v := r.FormValue("start_time"); v != "" {
			body["start_time"] = v
		}
		if v := r.FormValue("end_time"); v != "" {
			body["end_time"] = v
		}
		if err := client.AddSeriesDate(r.Context(), id, body, token); err != nil {
			http.Error(w, "failed to add date: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/series/%d", id), http.StatusSeeOther)
	}
}

func adminSeriesRegenerateTokenHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		token := getSessionToken(r)
		_, _ = client.RegenerateSeriesToken(r.Context(), id, token)
		http.Redirect(w, r, fmt.Sprintf("/admin/series/%d", id), http.StatusSeeOther)
	}
}

func adminSeriesRevokeTokenHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		token := getSessionToken(r)
		_ = client.RevokeSeriesToken(r.Context(), id, token)
		http.Redirect(w, r, fmt.Sprintf("/admin/series/%d", id), http.StatusSeeOther)
	}
}

// ── Public token view ─────────────────────────────────────────────────────────

func seriesTokenPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.PathValue("token")
		series, err := client.GetSeriesByInviteToken(r.Context(), tok)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		title := i18n.T(r, "series_token_view")
		renderTemplate(w, tmpls.seriesToken, tmplData(r, cfg, i18n, title, map[string]any{
			"Series": series,
			"Token":  tok,
		}))
	}
}

func seriesTokenSaveDescriptionHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.PathValue("token")
		eventIDStr := r.PathValue("eventID")
		eventID, err := strconv.Atoi(eventIDStr)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		desc := r.FormValue("description")
		if err := client.PatchSeriesEventDescription(r.Context(), tok, eventID, desc); err != nil {
			http.Error(w, "failed to save: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/series_token/%s", tok), http.StatusSeeOther)
	}
}
