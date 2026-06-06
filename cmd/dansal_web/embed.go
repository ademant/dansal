package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// embedLang returns the display language for embed pages: prefer the ?lang=
// query param, fall back to cookie/default.
func embedLang(r *http.Request, i18n *I18n) string {
	if l := r.URL.Query().Get("lang"); l != "" {
		if i18n.HasLang(l) {
			return l
		}
	}
	return i18n.detectLang(r)
}

// upcomingEvents filters events to those starting from now, sorted ascending.
func upcomingEvents(events []Event) []Event {
	now := time.Now().UTC()
	var out []Event
	for _, e := range events {
		t, ok := parseTime(e.StartTime)
		if !ok || t.Before(now) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartTime < out[j].StartTime
	})
	return out
}

// resolveOrgSlugs maps a list of org slug query params to a set of org IDs.
// Returns nil map (= all orgs) if no slugs were requested.
func resolveOrgSlugs(orgs []Organization, slugs []string) map[int]bool {
	if len(slugs) == 0 {
		return nil
	}
	m := make(map[int]bool)
	for _, slug := range slugs {
		for _, o := range orgs {
			if strings.EqualFold(effectiveSlug(o), slug) {
				m[o.ID] = true
			}
		}
	}
	return m
}

// embedEventsHandler serves GET /embed/events — filterable upcoming event list.
func embedEventsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := embedLang(r, i18n)
		orgSlugs := r.URL.Query()["org"]
		mode := r.URL.Query().Get("mode")
		if mode == "" {
			mode = "agenda"
		}

		allEvents, err := client.GetEvents(r.Context(), "")
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		allOrgs, _ := client.GetOrganizations(r.Context())
		orgFilter := resolveOrgSlugs(allOrgs, orgSlugs)

		events := upcomingEvents(allEvents)
		if orgFilter != nil {
			var filtered []Event
			for _, e := range events {
				if e.OrganizationID != nil && orgFilter[*e.OrganizationID] {
					filtered = append(filtered, e)
				}
			}
			events = filtered
		}

		// Build org name lookup for display.
		orgNames := make(map[int]string, len(allOrgs))
		for _, o := range allOrgs {
			orgNames[o.ID] = o.Name
		}

		strs := i18n.Strings(lang)
		renderTemplate(w, tmpls.embedEvents, map[string]any{
			"Lang":     lang,
			"Mode":     mode,
			"Events":   events,
			"OrgNames": orgNames,
			"Strings":  strs,
			"BaseURL":  cfg.BaseURL,
		})
	}
}

// embedEventHandler serves GET /embed/event/{id} — single event card.
func embedEventHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		event, err := client.GetEvent(r.Context(), id)
		if err != nil || !event.IsPublished {
			http.NotFound(w, r)
			return
		}

		lang := embedLang(r, i18n)
		var orgName string
		if event.OrganizationID != nil {
			if o, err := client.GetOrganization(r.Context(), *event.OrganizationID); err == nil {
				orgName = o.Name
			}
		}

		strs := i18n.Strings(lang)
		renderTemplate(w, tmpls.embedEvent, map[string]any{
			"Lang":    lang,
			"Event":   event,
			"OrgName": orgName,
			"Strings": strs,
			"BaseURL": cfg.BaseURL,
		})
	}
}

// embedOrgHandler serves GET /embed/org/{slug} — org profile card with upcoming events.
func embedOrgHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		lang := embedLang(r, i18n)

		count := 3
		if n, err := strconv.Atoi(r.URL.Query().Get("events")); err == nil && n >= 0 {
			count = n
		}

		allOrgs, err := client.GetOrganizations(r.Context())
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		var org *Organization
		for _, o := range allOrgs {
			if strings.EqualFold(effectiveSlug(o), slug) {
				org = &o
				break
			}
		}
		if org == nil {
			http.NotFound(w, r)
			return
		}

		var events []Event
		if count > 0 {
			all, err := client.GetEventsByOrg(r.Context(), org.ID)
			if err == nil {
				upcoming := upcomingEvents(all)
				if count < len(upcoming) {
					upcoming = upcoming[:count]
				}
				events = upcoming
			}
		}

		strs := i18n.Strings(lang)
		renderTemplate(w, tmpls.embedOrg, map[string]any{
			"Lang":    lang,
			"Org":     org,
			"Events":  events,
			"Strings": strs,
			"BaseURL": cfg.BaseURL,
		})
	}
}

// embedNextHandler serves GET /embed/next — minimal upcoming events ticker.
func embedNextHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := embedLang(r, i18n)
		orgSlugs := r.URL.Query()["org"]

		count := 5
		if n, err := strconv.Atoi(r.URL.Query().Get("count")); err == nil && n > 0 {
			count = n
		}

		allEvents, err := client.GetEvents(r.Context(), "")
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		allOrgs, _ := client.GetOrganizations(r.Context())
		orgFilter := resolveOrgSlugs(allOrgs, orgSlugs)

		events := upcomingEvents(allEvents)
		if orgFilter != nil {
			var filtered []Event
			for _, e := range events {
				if e.OrganizationID != nil && orgFilter[*e.OrganizationID] {
					filtered = append(filtered, e)
				}
			}
			events = filtered
		}
		if count < len(events) {
			events = events[:count]
		}

		strs := i18n.Strings(lang)
		renderTemplate(w, tmpls.embedNext, map[string]any{
			"Lang":    lang,
			"Events":  events,
			"Strings": strs,
			"BaseURL": cfg.BaseURL,
		})
	}
}
