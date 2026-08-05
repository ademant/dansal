package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// embedSiteName returns the display name for the host instance, used in
// "Powered by …" attribution links inside embedded widgets.
func embedSiteName(cfg *Config) string {
	if n := siteCfg.SiteName(); n != "" {
		return n
	}
	if cfg.SiteName != "" {
		return cfg.SiteName
	}
	return cfg.Domain
}

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

// resolveLocationIDs maps a list of ?location= query params to a set of
// location IDs. Returns nil map (= all locations) if none were requested.
func resolveLocationIDs(values []string) map[int]bool {
	if len(values) == 0 {
		return nil
	}
	m := make(map[int]bool)
	for _, v := range values {
		if id, err := strconv.Atoi(v); err == nil {
			m[id] = true
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// embedEventsHandler serves GET /embed/events — filterable upcoming event list.
func embedEventsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := embedLang(r, i18n)
		orgSlugs := r.URL.Query()["org"]
		locIDs := r.URL.Query()["location"]
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
		locFilter := resolveLocationIDs(locIDs)

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
		if locFilter != nil {
			var filtered []Event
			for _, e := range events {
				if e.LocationID != nil && locFilter[*e.LocationID] {
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
		renderEmbed(w, tmpls.embedEvents, map[string]any{
			"Lang":     lang,
			"Mode":     mode,
			"Events":   events,
			"OrgNames": orgNames,
			"Strings":  strs,
			"BaseURL":  cfg.BaseURL,
				"SiteName": embedSiteName(cfg),
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
		renderEmbed(w, tmpls.embedEvent, map[string]any{
			"Lang":     lang,
			"Event":    event,
			"OrgName":  orgName,
			"Strings":  strs,
			"BaseURL":  cfg.BaseURL,
			"SiteName": embedSiteName(cfg),
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
		renderEmbed(w, tmpls.embedOrg, map[string]any{
			"Lang":     lang,
			"Org":      org,
			"Events":   events,
			"Strings":  strs,
			"BaseURL":  cfg.BaseURL,
			"SiteName": embedSiteName(cfg),
		})
	}
}

// embedNextHandler serves GET /embed/next — minimal upcoming events ticker.
func embedNextHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := embedLang(r, i18n)
		orgSlugs := r.URL.Query()["org"]
		locIDs := r.URL.Query()["location"]

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
		locFilter := resolveLocationIDs(locIDs)

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
		if locFilter != nil {
			var filtered []Event
			for _, e := range events {
				if e.LocationID != nil && locFilter[*e.LocationID] {
					filtered = append(filtered, e)
				}
			}
			events = filtered
		}
		if count < len(events) {
			events = events[:count]
		}

		strs := i18n.Strings(lang)
		renderEmbed(w, tmpls.embedNext, map[string]any{
			"Lang":     lang,
			"Events":   events,
			"Strings":  strs,
			"BaseURL":  cfg.BaseURL,
			"SiteName": embedSiteName(cfg),
		})
	}
}

// locMarker is the compact per-location shape sent to the /embed/locations
// client for map markers.
type locMarker struct {
	ID     int     `json:"id"`
	Name   string  `json:"name"`
	Town   string  `json:"town,omitempty"`
	Lat    float64 `json:"lat"`
	Lng    float64 `json:"lng"`
	Future int     `json:"future"`
	Past   int     `json:"past"`
}

// embedLocationsHandler serves GET /embed/locations — a map of all locations
// with future/past event counts, linking to each location's public page.
// Optional ?org= filters to locations associated with the given org slug(s).
func embedLocationsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := embedLang(r, i18n)
		q := r.URL.Query()

		allLocations, err := client.GetLocationsWithEventCounts(r.Context())
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		allOrgs, _ := client.GetOrganizations(r.Context())
		orgFilter := resolveOrgSlugs(allOrgs, q["org"])

		markers := make([]locMarker, 0, len(allLocations))
		for _, l := range allLocations {
			if l.Latitude == nil || l.Longitude == nil {
				continue
			}
			if orgFilter != nil {
				match := false
				for _, oid := range l.OrganizationIDs {
					if orgFilter[oid] {
						match = true
						break
					}
				}
				if !match {
					continue
				}
			}
			markers = append(markers, locMarker{
				ID: l.ID, Name: l.Location, Town: l.Town,
				Lat: *l.Latitude, Lng: *l.Longitude,
				Future: l.FutureEventCount, Past: l.PastEventCount,
			})
		}
		locJSON, _ := json.Marshal(markers)

		strs := i18n.Strings(lang)
		renderEmbed(w, tmpls.embedLocations, map[string]any{
			"Lang":     lang,
			"LocData":  template.JS(locJSON),
			"Strings":  strs,
			"BaseURL":  cfg.BaseURL,
			"SiteName": embedSiteName(cfg),
		})
	}
}

// calEvent is the compact per-event shape sent to the /embed/calendar client
// for map markers and client-side date/tag filtering.
type calEvent struct {
	ID        int      `json:"id"`
	Title     string   `json:"t"`
	Start     string   `json:"s"`
	End       string   `json:"e,omitempty"`
	Location  string   `json:"loc,omitempty"`
	Town      string   `json:"town,omitempty"`
	Lat       float64  `json:"lat,omitempty"`
	Lng       float64  `json:"lng,omitempty"`
	URL       string   `json:"url,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Org       string   `json:"org,omitempty"`
	Cancelled bool     `json:"x,omitempty"`
}

// embedCalendarHandler serves GET /embed/calendar — map + filterable event list
// with a client-side date-range and event-type (tag) filter.
func embedCalendarHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := embedLang(r, i18n)
		q := r.URL.Query()

		now := time.Now().UTC()
		from := now.Truncate(24 * time.Hour)
		to := from.AddDate(0, 0, 14)
		if v, err := time.Parse("2006-01-02", q.Get("from")); err == nil {
			from = v
		}
		if v, err := time.Parse("2006-01-02", q.Get("to")); err == nil {
			to = v
		}
		if !to.After(from) || to.Sub(from) > 366*24*time.Hour {
			to = from.AddDate(0, 0, 180)
		}

		params := url.Values{}
		params.Set("is_published", "true")
		params.Set("limit", "1000")
		params.Set("start_time_after", strconv.FormatInt(from.Add(-time.Second).Unix(), 10))
		params.Set("start_time_before", strconv.FormatInt(to.AddDate(0, 0, 1).Unix(), 10))
		if !from.After(now) {
			params.Set("include_past", "true")
		}

		allEvents, err := client.GetEventsFiltered(r.Context(), params)
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		sort.Slice(allEvents, func(i, j int) bool { return allEvents[i].StartTime < allEvents[j].StartTime })

		allOrgs, _ := client.GetOrganizations(r.Context())
		orgFilter := resolveOrgSlugs(allOrgs, q["org"])
		locFilter := resolveLocationIDs(q["location"])
		orgNames := make(map[int]string, len(allOrgs))
		for _, o := range allOrgs {
			orgNames[o.ID] = o.Name
		}

		events := make([]Event, 0, len(allEvents))
		for _, e := range allEvents {
			if orgFilter != nil && (e.OrganizationID == nil || !orgFilter[*e.OrganizationID]) {
				continue
			}
			if locFilter != nil && (e.LocationID == nil || !locFilter[*e.LocationID]) {
				continue
			}
			events = append(events, e)
		}

		cal := make([]calEvent, 0, len(events))
		for _, e := range events {
			ce := calEvent{
				ID: e.ID, Title: e.Title, Start: e.StartTime, End: e.EndTime,
				URL: e.URL, Tags: e.Tags, Cancelled: e.IsCancelled,
			}
			if e.Location != nil {
				ce.Location = e.Location.Location
				ce.Town = e.Location.Town
				if e.Location.Latitude != nil && e.Location.Longitude != nil {
					ce.Lat = *e.Location.Latitude
					ce.Lng = *e.Location.Longitude
				}
			}
			if e.OrganizationID != nil {
				ce.Org = orgNames[*e.OrganizationID]
			}
			cal = append(cal, ce)
		}
		calJSON, _ := json.Marshal(cal)

		allTags, _ := client.GetTags(r.Context())

		fpLocales := map[string]string{
			"de": "de", "fr": "fr", "es": "es", "it": "it",
			"nl": "nl", "uk": "uk", "ca": "cat", "pt": "pt",
			"pl": "pl", "cs": "cs", "br": "fr",
		}

		strs := i18n.Strings(lang)
		renderEmbed(w, tmpls.embedCalendar, map[string]any{
			"Lang":        lang,
			"Events":      events,
			"CalData":     template.JS(calJSON),
			"Tags":        allTags,
			"OrgNames":    orgNames,
			"From":        from.Format("2006-01-02"),
			"To":          to.Format("2006-01-02"),
			"SelectedTag": q.Get("tag"),
			"FPLocale":    fpLocales[lang],
			"Strings":     strs,
			"BaseURL":     cfg.BaseURL,
			"SiteName":    embedSiteName(cfg),
		})
	}
}

// embedParam describes one query parameter accepted by an embed widget, for
// the machine-readable manifest served at /embed/manifest.json.
type embedParam struct {
	Type        string `json:"type"`
	Repeatable  bool   `json:"repeatable,omitempty"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description"`
}

// embedWidget describes one /embed/* route for the manifest.
type embedWidget struct {
	Path        string                `json:"path"`
	Description string                `json:"description"`
	Params      map[string]embedParam `json:"params"`
}

// embedManifest is the static, hand-maintained source of truth for
// /embed/manifest.json — kept next to the handlers it documents so a change
// to a handler's query params is a visible diff against this struct.
var embedManifest = map[string]embedWidget{
	"events": {
		Path:        "/embed/events",
		Description: "Filterable upcoming event list",
		Params: map[string]embedParam{
			"org":      {Type: "string", Repeatable: true, Description: "org slug filter; repeatable, e.g. ?org=a&org=b"},
			"location": {Type: "number", Repeatable: true, Description: "location ID filter; repeatable"},
			"mode":     {Type: "string", Default: "agenda", Description: "display mode"},
			"lang":     {Type: "string", Description: "override display language (2-letter code)"},
		},
	},
	"event": {
		Path:        "/embed/event/{id}",
		Description: "Single event card",
		Params: map[string]embedParam{
			"lang": {Type: "string", Description: "override display language (2-letter code)"},
		},
	},
	"org": {
		Path:        "/embed/org/{slug}",
		Description: "Organization profile card with upcoming events",
		Params: map[string]embedParam{
			"events": {Type: "number", Default: "3", Description: "number of upcoming events to show (0 to hide the list)"},
			"lang":   {Type: "string", Description: "override display language (2-letter code)"},
		},
	},
	"next": {
		Path:        "/embed/next",
		Description: "Minimal upcoming events ticker",
		Params: map[string]embedParam{
			"org":      {Type: "string", Repeatable: true, Description: "org slug filter; repeatable"},
			"location": {Type: "number", Repeatable: true, Description: "location ID filter; repeatable"},
			"count":    {Type: "number", Default: "5", Description: "max number of events to show"},
			"lang":     {Type: "string", Description: "override display language (2-letter code)"},
		},
	},
	"locations": {
		Path:        "/embed/locations",
		Description: "Map of all locations with future/past event counts",
		Params: map[string]embedParam{
			"org":  {Type: "string", Repeatable: true, Description: "org slug filter; repeatable"},
			"lang": {Type: "string", Description: "override display language (2-letter code)"},
		},
	},
	"calendar": {
		Path:        "/embed/calendar",
		Description: "Combined map + filterable event list with client-side date-range and tag filter",
		Params: map[string]embedParam{
			"org":      {Type: "string", Repeatable: true, Description: "org slug filter; repeatable"},
			"location": {Type: "number", Repeatable: true, Description: "location ID filter; repeatable"},
			"from":     {Type: "string", Description: "start date, YYYY-MM-DD (default: today)"},
			"to":       {Type: "string", Description: "end date, YYYY-MM-DD (default: 14 days after from)"},
			"tag":      {Type: "string", Description: "pre-select a tag slug in the client-side filter"},
			"lang":     {Type: "string", Description: "override display language (2-letter code)"},
		},
	},
}

// embedManifestHandler serves GET /embed/manifest.json — a machine-readable
// description of every /embed/* widget and its query params, so integrators
// (e.g. the wp-dansal WordPress plugin) can introspect embed options at
// runtime instead of hardcoding them.
func embedManifestHandler(cfg *Config) http.HandlerFunc {
	data, _ := json.MarshalIndent(map[string]any{"widgets": embedManifest}, "", "  ")
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
	}
}
