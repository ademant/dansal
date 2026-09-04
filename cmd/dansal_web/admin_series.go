package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ── Data structs ──────────────────────────────────────────────────────────────

type AdminSeriesData struct {
	Series   []EventSeries
	IsAdmin  bool
	OrgNames map[int]string
}

// seriesDefaults mirrors cmd/dansal's seriesTemplateData JSON shape — the
// rich per-series default set (everything a template can carry, minus
// title/location/times/org, which the series already models via its own
// dedicated fields). Kept as a plain struct (not reusing EventTemplate's
// templateEventData) since series defaults intentionally exclude org_id/
// loc_id/start_time/end_time, which templateEventData carries but series
// already owns through other fields.
type seriesDefaults struct {
	HasBall            bool             `json:"has_ball"`
	HasWorkshop        bool             `json:"has_workshop"`
	HasFestival        bool             `json:"has_festival"`
	WorkshopDifficulty string           `json:"workshop_difficulty"`
	URL                string           `json:"url"`
	BookingURL         string           `json:"booking_url"`
	Pricing            *Pricing         `json:"pricing,omitempty"`
	Tags               []string         `json:"tags"`
	DanceIDs           []int            `json:"dance_ids"`
	Food               string           `json:"food"`
	Drink              string           `json:"drink"`
	FloorCondition     string           `json:"floor_condition"`
	ContactName        string           `json:"contact_name,omitempty"`
	ContactEmail       string           `json:"contact_email,omitempty"`
	TicketsTotal       int              `json:"tickets_total"`
	BookingEnabled     bool             `json:"booking_enabled"`
	Timetable          []TimetableEntry `json:"timetable"`
}

func parseSeriesDefaults(raw json.RawMessage) seriesDefaults {
	var d seriesDefaults
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &d); err != nil {
			log.Printf("could not parse series defaults: %v", err)
		}
	}
	return d
}

type AdminSeriesEditData struct {
	Series      EventSeries
	Locations   []Location
	Orgs        []Organization
	Musicians   []Musician
	Instructors []Instructor
	IsAdmin     bool
	BaseURL     string
	ErrorKey    string
	TplDefaults seriesDefaults
	Dances      []Dance
	DanceIDSet  map[int]bool
	Applied     bool // set when redirected back after a successful apply-to-events
}

type PrefillDate struct {
	Date  string
	Start string
	End   string
}

type AdminSeriesNewData struct {
	Locations           []Location
	Orgs                []Organization
	Musicians           []Musician
	Instructors         []Instructor
	Series              []EventSeries
	IsAdmin             bool
	ErrorKey            string
	PrefillTitle        string
	PrefillLocID        int
	PrefillStart        string
	PrefillEnd          string
	PrefillOrgID        int
	PrefillMusicianID   int
	PrefillInstructorID int
	PrefillEventIDs     []int
	PrefillDates        []PrefillDate
	PrefillDateCount    int // number of pre-filled rows; handler skips these when calling AddSeriesDate
}

// parseTimeOnly extracts HH:MM from an RFC3339 timestamp, preserving the
// timezone offset embedded in the string (Berlin time from epochToLocal).
func parseTimeOnly(ts string) (string, error) {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "", err
	}
	return t.Format("15:04"), nil
}

// parseDateOnly extracts YYYY-MM-DD from an RFC3339 timestamp, preserving
// the timezone offset embedded in the string.
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
		orgs, err := client.GetOrganizations(r.Context())
		orgNames := make(map[int]string)
		if err == nil {
			for _, o := range orgs {
				orgNames[o.ID] = o.Name
			}
		}
		title := i18n.T(r, "series_title")
		renderTemplate(w, tmpls.adminSeries, tmplData(r, cfg, i18n, title, AdminSeriesData{
			Series:   series,
			IsAdmin:  user.Role == "admin",
			OrgNames: orgNames,
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
		musicians, _ := client.GetMusicians(r.Context())
		instructors, _ := client.GetInstructors(r.Context())

		data := AdminSeriesNewData{
			Locations:   locs,
			Orgs:        orgs,
			Musicians:   musicians,
			Instructors: instructors,
			IsAdmin:     user.Role == "admin",
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
		if musicianIDStr := r.URL.Query().Get("musician_id"); musicianIDStr != "" {
			data.PrefillMusicianID, _ = strconv.Atoi(musicianIDStr)
		}
		if instructorIDStr := r.URL.Query().Get("instructor_id"); instructorIDStr != "" {
			data.PrefillInstructorID, _ = strconv.Atoi(instructorIDStr)
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

		// Fetch dates for all selected event IDs and pre-fill the date table.
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
		data.PrefillDateCount = len(data.PrefillDates)

		title := i18n.T(r, "series_new")
		renderTemplate(w, tmpls.adminSeriesNew, tmplData(r, cfg, i18n, title, data))
	}
}

func adminSeriesCreateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
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
		if v := r.FormValue("musician_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				body["musician_id"] = n
			}
		}
		if v := r.FormValue("instructor_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				body["instructor_id"] = n
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
		created, err := client.CreateSeries(r.Context(), body, token)
		if err != nil {
			http.Error(w, "failed to create series: "+err.Error(), http.StatusBadGateway)
			return
		}
		// Add manually entered dates from the growing date table.
		// Skip the first prefill_count rows — those correspond to existing events
		// being assigned via assign_event_ids and must not generate new events.
		prefillCount, _ := strconv.Atoi(r.FormValue("prefill_count"))
		dates := r.Form["series_date"]
		starts := r.Form["series_start"]
		ends := r.Form["series_end"]
		for i, d := range dates {
			if i < prefillCount {
				continue
			}
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
			if err := client.AddSeriesDate(r.Context(), created.ID, map[string]any{
				"date":       d,
				"start_time": st,
				"end_time":   et,
			}, token); err != nil {
				log.Printf("add series date %s to series %d: %v", d, created.ID, err)
			}
		}

		// Assign any events that were passed from the bulk-assign flow.
		var assignIDs []int
		for _, s := range r.Form["assign_event_ids"] {
			if id, err := strconv.Atoi(s); err == nil && id > 0 {
				assignIDs = append(assignIDs, id)
			}
		}
		if len(assignIDs) > 0 {
			if err := client.AssignEventsToSeries(r.Context(), created.ID, assignIDs, token); err != nil {
				log.Printf("assign %d events to series %d: %v", len(assignIDs), created.ID, err)
			}
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
		musicians, _ := client.GetMusicians(r.Context())
		instructors, _ := client.GetInstructors(r.Context())
		dances, _ := client.GetDances(r.Context())
		td := parseSeriesDefaults(series.TemplateData)
		danceIDSet := make(map[int]bool, len(td.DanceIDs))
		for _, id := range td.DanceIDs {
			danceIDSet[id] = true
		}
		title := i18n.T(r, "series_edit")
		renderTemplate(w, tmpls.adminSeriesEdit, tmplData(r, cfg, i18n, title, AdminSeriesEditData{
			Series:      series,
			Locations:   locs,
			Orgs:        orgs,
			Musicians:   musicians,
			Instructors: instructors,
			IsAdmin:     user.Role == "admin",
			BaseURL:     cfg.publicBaseURL(),
			TplDefaults: td,
			Dances:      dances,
			DanceIDSet:  danceIDSet,
			Applied:     r.URL.Query().Get("applied") == "1",
		}))
	}
}

// seriesDefaultsFromForm builds the template_data JSON blob from the
// "Series Defaults" fieldset in admin_series_edit.html. Mirrors how
// createEvent derives has_ball/workshop/festival from the submitted tags.
// existingTimetable is carried through unchanged since there is no UI yet
// for editing a series' default timetable — without this, saving the header
// form would silently wipe out a timetable set via the API.
func seriesDefaultsFromForm(r *http.Request, existingTimetable []TimetableEntry) []byte {
	tags := r.Form["tags"]
	var danceIDs []int
	for _, v := range r.Form["dance_ids"] {
		if n, err := strconv.Atoi(v); err == nil {
			danceIDs = append(danceIDs, n)
		}
	}
	d := seriesDefaults{
		HasBall:            sliceContains(tags, "bal-folk"),
		HasWorkshop:        sliceContains(tags, "dance-workshop") || sliceContains(tags, "musician-workshop"),
		HasFestival:        sliceContains(tags, "festival"),
		WorkshopDifficulty: r.FormValue("workshop_difficulty"),
		URL:                strings.TrimSpace(r.FormValue("url")),
		BookingURL:         strings.TrimSpace(r.FormValue("booking_url")),
		Tags:               tags,
		DanceIDs:           danceIDs,
		Food:               r.FormValue("food"),
		Drink:              r.FormValue("drink"),
		FloorCondition:     r.FormValue("floor_condition"),
		ContactName:        strings.TrimSpace(r.FormValue("contact_name")),
		ContactEmail:       strings.TrimSpace(r.FormValue("contact_email")),
		BookingEnabled:     r.FormValue("booking_enabled") != "",
	}
	if n, err := strconv.Atoi(r.FormValue("tickets_total")); err == nil {
		d.TicketsTotal = n
	}
	switch r.FormValue("pricing_type") {
	case "free":
		d.Pricing = &Pricing{Type: "free"}
	case "donation", "single":
		amount, _ := strconv.ParseFloat(r.FormValue("pricing_amount"), 64)
		d.Pricing = &Pricing{Type: r.FormValue("pricing_type"), Amount: amount, Currency: strings.TrimSpace(r.FormValue("pricing_currency"))}
	}
	d.Timetable = existingTimetable
	b, _ := json.Marshal(d)
	return b
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
		existing, err := client.GetSeriesByID(r.Context(), id, token)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		body := map[string]any{
			"title":              strings.TrimSpace(r.FormValue("title")),
			"description":        strings.TrimSpace(r.FormValue("description")),
			"default_start_time": r.FormValue("default_start_time"),
			"default_end_time":   r.FormValue("default_end_time"),
			"cadence":            strings.TrimSpace(r.FormValue("cadence")),
		}
		if v := r.FormValue("default_location_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				body["default_location_id"] = n
			}
		}
		if vals, ok := r.Form["organization_id"]; ok {
			n, _ := strconv.Atoi(vals[0])
			if n > 0 {
				body["organization_id"] = n
			} else {
				body["organization_id"] = 0
			}
		}
		if vals, ok := r.Form["musician_id"]; ok {
			n, _ := strconv.Atoi(vals[0])
			if n > 0 {
				body["musician_id"] = n
			} else {
				body["musician_id"] = 0
			}
		}
		if vals, ok := r.Form["instructor_id"]; ok {
			n, _ := strconv.Atoi(vals[0])
			if n > 0 {
				body["instructor_id"] = n
			} else {
				body["instructor_id"] = 0
			}
		}
		body["template_data"] = json.RawMessage(seriesDefaultsFromForm(r, parseSeriesDefaults(existing.TemplateData).Timetable))
		body["image_ai_generated"] = r.FormValue("image_ai_generated") == "1"
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
		if err := client.DeleteSeries(r.Context(), id, token); err != nil {
			log.Printf("delete series %d: %v", id, err)
		}
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
			if err := client.UpdateSeriesDescriptions(r.Context(), id, updates, token); err != nil {
				log.Printf("update %d series descriptions: %v", len(updates), err)
			}
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
		if _, err := client.RegenerateSeriesToken(r.Context(), id, token); err != nil {
			log.Printf("regenerate token for series %d: %v", id, err)
		}
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
		if err := client.RevokeSeriesToken(r.Context(), id, token); err != nil {
			log.Printf("revoke token for series %d: %v", id, err)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/series/%d", id), http.StatusSeeOther)
	}
}

// ── Series image upload/delete ────────────────────────────────────────────────

// adminSeriesImageUploadHandler handles POST /admin/series/{id}/image —
// uploads a banner image for the series.
func adminSeriesImageUploadHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		if file, header, ferr := r.FormFile("image"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadSeriesImage(r.Context(), id, data, header.Filename, token); uerr != nil {
				log.Printf("upload series image %d: %v", id, uerr)
			}
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/series/%d", id), http.StatusSeeOther)
	}
}

// adminSeriesImageDeleteHandler handles POST /admin/series/{id}/image/delete —
// removes the series banner image.
func adminSeriesImageDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		if err := client.DeleteSeriesImage(r.Context(), id, getSessionToken(r)); err != nil {
			log.Printf("delete series image %d: %v", id, err)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/series/%d", id), http.StatusSeeOther)
	}
}

// adminSeriesApplyToEventsHandler handles POST /admin/series/{id}/apply-to-events —
// propagates series org/location/musician/instructor and template defaults to
// all already-attached events, then redirects back with ?applied=1.
func adminSeriesApplyToEventsHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		if err := client.ApplySeriesToEvents(r.Context(), id, token); err != nil {
			log.Printf("apply series %d to events: %v", id, err)
			http.Error(w, "failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/series/%d?applied=1", id), http.StatusSeeOther)
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
