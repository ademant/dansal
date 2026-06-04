package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
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

type PrefillDate struct {
	Date  string
	Start string
	End   string
}

type AdminSeriesNewData struct {
	Locations       []Location
	Orgs            []Organization
	Series          []EventSeries
	IsAdmin         bool
	ErrorKey        string
	PrefillTitle    string
	PrefillLocID    int
	PrefillStart    string
	PrefillEnd      string
	PrefillOrgID    int
	PrefillEventIDs []int
	PrefillDates    []PrefillDate
}

// parseTimeOnly extracts HH:MM from an RFC3339 timestamp string.
func parseTimeOnly(ts string) (string, error) {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "", err
	}
	return t.Format("15:04"), nil
}

// parseDateOnly extracts YYYY-MM-DD from an RFC3339 timestamp string.
func parseDateOnly(ts string) (string, error) {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "", err
	}
	return t.Format("2006-01-02"), nil
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
		token := getSessionToken(r)
		locs, _ := client.GetLocations(r.Context())
		orgs, _ := client.GetOrganizations(r.Context())

		data := AdminSeriesNewData{
			Locations: locs,
			Orgs:      orgs,
			IsAdmin:   user.Role == "admin",
		}

		// Pre-fill from event IDs passed by bulk-assign-series
		if idsParam := r.URL.Query().Get("ids"); idsParam != "" {
			for _, s := range strings.Split(idsParam, ",") {
				if id, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && id > 0 {
					data.PrefillEventIDs = append(data.PrefillEventIDs, id)
				}
			}
		}
		if orgIDStr := r.URL.Query().Get("org_id"); orgIDStr != "" {
			data.PrefillOrgID, _ = strconv.Atoi(orgIDStr)
		}
		if prefillID, err := strconv.Atoi(r.URL.Query().Get("prefill_event_id")); err == nil && prefillID > 0 {
			if ev, err := client.GetEventAuthed(r.Context(), prefillID, token); err == nil {
				data.PrefillTitle = ev.Title
				if ev.LocationID != nil {
					data.PrefillLocID = *ev.LocationID
				}
				if t, err := parseTimeOnly(ev.StartTime); err == nil {
					data.PrefillStart = t
				}
				if t, err := parseTimeOnly(ev.EndTime); err == nil {
					data.PrefillEnd = t
				}
			}
		}

		// Fetch dates for all selected event IDs.
		for _, id := range data.PrefillEventIDs {
			ev, err := client.GetEventAuthed(r.Context(), id, token)
			if err != nil {
				continue
			}
			d, err := parseDateOnly(ev.StartTime)
			if err != nil {
				continue
			}
			pd := PrefillDate{Date: d}
			if t, err := parseTimeOnly(ev.StartTime); err == nil {
				pd.Start = t
			}
			if t, err := parseTimeOnly(ev.EndTime); err == nil {
				pd.End = t
			}
			data.PrefillDates = append(data.PrefillDates, pd)
		}

		title := i18n.T(r, "series_new")
		renderTemplate(w, tmpls.adminSeriesNew, tmplData(r, cfg, i18n, title, data))
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
			"title":              strings.TrimSpace(r.FormValue("title")),
			"description":        strings.TrimSpace(r.FormValue("description")),
			"default_start_time": r.FormValue("default_start_time"),
			"default_end_time":   r.FormValue("default_end_time"),
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
		// Add manually entered dates from the growing date table.
		dates := r.Form["series_date"]
		starts := r.Form["series_start"]
		ends := r.Form["series_end"]
		for i, d := range dates {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			st := r.FormValue("default_start_time")
			et := r.FormValue("default_end_time")
			if i < len(starts) && strings.TrimSpace(starts[i]) != "" {
				st = starts[i]
			}
			if i < len(ends) && strings.TrimSpace(ends[i]) != "" {
				et = ends[i]
			}
			_ = client.AddSeriesDate(r.Context(), created.ID, map[string]any{
				"date":       d,
				"start_time": st,
				"end_time":   et,
			}, token)
		}

		// Assign any events that were passed from the bulk-assign flow.
		var assignIDs []int
		for _, s := range r.Form["assign_event_ids"] {
			if id, err := strconv.Atoi(s); err == nil && id > 0 {
				assignIDs = append(assignIDs, id)
			}
		}
		if len(assignIDs) > 0 {
			_ = client.AssignEventsToSeries(r.Context(), created.ID, assignIDs, token)
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

func adminSeriesSaveDescriptionsHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		var updates []map[string]any
		for key, vals := range r.Form {
			if !strings.HasPrefix(key, "desc_") {
				continue
			}
			eventIDStr := strings.TrimPrefix(key, "desc_")
			eventID, err := strconv.Atoi(eventIDStr)
			if err != nil || len(vals) == 0 {
				continue
			}
			updates = append(updates, map[string]any{
				"event_id":    eventID,
				"description": vals[0],
			})
		}
		if len(updates) > 0 {
			_ = client.UpdateSeriesDescriptions(r.Context(), id, updates, token)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/series/%d", id), http.StatusSeeOther)
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
