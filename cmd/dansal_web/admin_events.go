package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func parseChangedAt(changedAt string) int64 {
	if changedAt == "" {
		return 0
	}
	if n, err := strconv.ParseInt(changedAt, 10, 64); err == nil {
		return n
	}
	// If it's not a Unix timestamp, try to parse as ISO timestamp
	if t, err := time.Parse(time.RFC3339, changedAt); err == nil {
		return t.Unix()
	}
	return 0
}

// ── Events ────────────────────────────────────────────────────────────────────

type AdminEventsData struct {
	Events             []Event
	Organizations      []Organization
	OrgMap             map[int]string
	Locations          []Location
	Musicians          []Musician
	Dances             []Dance
	AllTags            []Tag
	Series             []EventSeries
	FilterIncludePast  bool
	FilterOrgID        int // -1 = no org assigned
	FilterOrgName      string
	FilterLocationID   int
	FilterLocationName string
	FilterCity         string
	FilterDateFrom     string
	FilterDateTo       string
	FilterMusicianID   int
	FilterType         string // "ball", "workshop", "festival"
	FilterDance        string
	FilterCreatedAfter string
	FilterSource       string
	FilterUnpublished  bool
	FilterFlagged      bool
	TotalCount         int
	PrevURL            string
	NextURL            string
}

type EventPrefill struct {
	Title, Description, URL, BookingURL string
	Date, EndDate                       string
	StartTime, EndTime                  string
	Location, Town, Country             string
	HasBall, HasWorkshop, HasFestival   bool
	WorkshopDifficulty                  string
	OrgID                               int
	LocID                               int
	PricingType                         string
	PricingAmount                       float64
	PricingCurrency                     string
	PricingLines                        []Price
	Tags                                []string
	DanceIDs                            []int
	Food                                string
	Drink                               string
	TicketsTotal                        int
	BookingEnabled                      bool
	Timetable                           []TimetableEntry
	CloneMode                           bool
	OriginalDate                        string // used in clone mode to enforce date change
}

type TagOption struct {
	Slug    string
	Name    string
	Checked bool
}

type TagGroup struct {
	Category string
	Tags     []TagOption
}

func sliceContains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func buildGroupedTags(tags []Tag, checked map[string]bool) []TagGroup {
	order := []string{"format", "type", "level"}
	byCategory := make(map[string][]TagOption)
	for _, t := range tags {
		byCategory[t.Category] = append(byCategory[t.Category], TagOption{
			Slug:    t.Slug,
			Name:    t.Name,
			Checked: checked[t.Slug],
		})
	}
	var groups []TagGroup
	for _, cat := range order {
		if opts, ok := byCategory[cat]; ok {
			groups = append(groups, TagGroup{Category: cat, Tags: opts})
		}
	}
	return groups
}

// AdminEventFormData backs the unified admin_event_form.html template used
// by both the new-event and edit-event pages. IsNew gates the handful of
// fields/sections that genuinely differ (Delete, series assign-vs-create,
// import source/UID, ticket fields that the create API doesn't support yet).
type AdminEventFormData struct {
	IsNew              bool
	Event              Event
	Org                *Organization
	Organizations      []Organization
	Locations          []Location
	LocOrgFirst        []Location
	LocOthers          []Location
	Musicians          []Musician
	Instructors        []Instructor
	Dances             []Dance
	SelectedDanceNames map[string]bool
	ErrorKey           string
	UserOrgs           []Organization
	Templates          []EventTemplate
	Series             []EventSeries
	CurrentSeries      *EventSeries
	Prefill            *EventPrefill // new-event only: clone/suggestion prefill metadata for JS
	CanDelete          bool          // whether the current user is allowed to hard-delete this event
	TimetableError     string        // raw message when the timetable failed to save (see #808 follow-up)
	IsTemplateMode     bool          // rendering /admin/templates/new: hide event-only fields, save as a template instead of an event
}

// eventFromPrefill synthesizes an Event from prefill data so the unified
// new/edit template can read form values uniformly via .Event.X regardless
// of mode. pf may be nil (a blank new-event form).
func eventFromPrefill(pf *EventPrefill) Event {
	ev := Event{}
	if pf == nil {
		return ev
	}
	ev.Title = pf.Title
	ev.Description = pf.Description
	ev.URL = pf.URL
	ev.BookingURL = pf.BookingURL
	ev.HasBall = pf.HasBall
	ev.HasWorkshop = pf.HasWorkshop
	ev.HasFestival = pf.HasFestival
	ev.WorkshopDifficulty = pf.WorkshopDifficulty
	if pf.Date != "" && pf.StartTime != "" {
		ev.StartTime = pf.Date + "T" + pf.StartTime + ":00"
	}
	endDate := pf.EndDate
	if endDate == "" {
		endDate = pf.Date
	}
	if endDate != "" && pf.EndTime != "" {
		ev.EndTime = endDate + "T" + pf.EndTime + ":00"
	}
	if pf.Location != "" {
		ev.Location = &Location{Location: pf.Location, Town: pf.Town, Country: pf.Country}
	}
	if pf.OrgID > 0 {
		oid := pf.OrgID
		ev.OrganizationID = &oid
	}
	if pf.LocID > 0 {
		lid := pf.LocID
		ev.LocationID = &lid
	}
	if pf.PricingType != "" && pf.PricingType != "none" {
		p := &Pricing{Type: pf.PricingType, Amount: pf.PricingAmount, Currency: pf.PricingCurrency, Prices: pf.PricingLines}
		ev.Pricing = p
	}
	ev.Tags = pf.Tags
	ev.Food = pf.Food
	ev.Drink = pf.Drink
	ev.TicketsTotal = pf.TicketsTotal
	ev.BookingEnabled = pf.BookingEnabled
	ev.Timetable = pf.Timetable
	return ev
}

func adminEventPublishHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		if err := client.PublishEvent(r.Context(), id, token); err != nil {
			http.Error(w, "publish failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, safeReferer(r, "/admin/events?unpublished=1"), http.StatusSeeOther)
	}
}

func adminEventCancelHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		if err := client.CancelEvent(r.Context(), id, token); err != nil {
			http.Error(w, "cancel failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, safeReferer(r, fmt.Sprintf("/events/%d", id)), http.StatusSeeOther)
	}
}

func adminEventDeleteHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
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
		event, fetchErr := client.GetEvent(r.Context(), id)
		_ = client.DeleteEvent(r.Context(), id, getSessionToken(r))
		if fetchErr == nil && event.OrganizationID != nil {
			go deliverDeleteToFollowers(cfg, db, id, *event.OrganizationID)
		}
		http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
	}
}

func adminEventBulkPublishHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		for _, s := range r.Form["event_ids"] {
			if id, err := strconv.Atoi(s); err == nil {
				_ = client.PublishEvent(r.Context(), id, token)
			}
		}
		http.Redirect(w, r, safeReferer(r, "/admin/events/maintenance"), http.StatusSeeOther)
	}
}

func adminEventBulkCancelHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		for _, s := range r.Form["event_ids"] {
			if id, err := strconv.Atoi(s); err == nil {
				_ = client.CancelEvent(r.Context(), id, token)
			}
		}
		http.Redirect(w, r, safeReferer(r, "/admin/events"), http.StatusSeeOther)
	}
}

func adminEventBulkDeleteHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
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
		token := getSessionToken(r)
		for _, s := range r.Form["event_ids"] {
			if id, err := strconv.Atoi(s); err == nil {
				event, fetchErr := client.GetEvent(r.Context(), id)
				_ = client.DeleteEvent(r.Context(), id, token)
				if fetchErr == nil && event.OrganizationID != nil {
					go deliverDeleteToFollowers(cfg, db, id, *event.OrganizationID)
				}
			}
		}
		http.Redirect(w, r, safeReferer(r, "/admin/events"), http.StatusSeeOther)
	}
}

func adminEventAssignSeriesHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		seriesID, _ := strconv.Atoi(r.FormValue("series_id"))
		token := getSessionToken(r)
		if seriesID > 0 {
			_ = client.AssignEventsToSeries(r.Context(), seriesID, []int{id}, token)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/events/%d/edit", id), http.StatusSeeOther)
	}
}

func adminEventRemoveFromSeriesHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		_ = client.RemoveEventFromSeries(r.Context(), id, token)
		http.Redirect(w, r, fmt.Sprintf("/admin/events/%d/edit", id), http.StatusSeeOther)
	}
}

func adminEventBulkAssignLocationHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		locationID, err := strconv.Atoi(r.FormValue("bulk_location_id"))
		if err != nil || locationID == 0 {
			http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
			return
		}
		var ids []int
		for _, s := range r.Form["event_ids"] {
			if id, err := strconv.Atoi(s); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			_ = client.BulkSetEventLocation(r.Context(), ids, locationID, getSessionToken(r))
		}
		http.Redirect(w, r, safeReferer(r, "/admin/events"), http.StatusSeeOther)
	}
}

func adminEventBulkSetTimeHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		startTime := r.FormValue("bulk_start_time")
		endTime := r.FormValue("bulk_end_time")
		if startTime == "" && endTime == "" {
			http.Redirect(w, r, safeReferer(r, "/admin/events"), http.StatusSeeOther)
			return
		}
		var ids []int
		for _, s := range r.Form["event_ids"] {
			if id, err := strconv.Atoi(s); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			_ = client.BulkSetEventTime(r.Context(), ids, startTime, endTime, getSessionToken(r))
		}
		http.Redirect(w, r, safeReferer(r, "/admin/events"), http.StatusSeeOther)
	}
}

func adminEventBulkAssignSeriesHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var ids []int
		for _, s := range r.Form["event_ids"] {
			if id, err := strconv.Atoi(s); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
			return
		}

		token := getSessionToken(r)
		seriesIDStr := r.FormValue("bulk_series_id")
		seriesID, _ := strconv.Atoi(seriesIDStr)

		if seriesID > 0 {
			// Path A: assign to existing series
			_ = client.AssignEventsToSeries(r.Context(), seriesID, ids, token)
			http.Redirect(w, r, safeReferer(r, "/admin/events"), http.StatusSeeOther)
			return
		}

		// Path B: new series — validate common org
		orgIDSet := map[int]bool{}
		for _, id := range ids {
			orgStr := r.FormValue(fmt.Sprintf("event_org_%d", id))
			if orgID, err := strconv.Atoi(orgStr); err == nil && orgID > 0 {
				orgIDSet[orgID] = true
			}
		}
		if len(orgIDSet) > 1 {
			// conflicting orgs — show error
			series, _ := client.GetSeriesList(r.Context(), token)
			orgs, _ := client.GetOrganizations(r.Context())
			locs, _ := client.GetLocations(r.Context())
			title := i18n.T(r, "series_new")
			renderTemplate(w, tmpls.adminSeriesNew, tmplData(r, cfg, i18n, title, AdminSeriesNewData{
				Locations: locs,
				Orgs:      orgs,
				IsAdmin:   user.Role == "admin",
				ErrorKey:  "evt_bulk_series_org_error",
				Series:    series,
			}))
			return
		}

		// Build redirect to series/new with prefill
		commonOrgID := 0
		for id := range orgIDSet {
			commonOrgID = id
		}
		firstID := ids[0]
		idStrs := make([]string, len(ids))
		for i, id := range ids {
			idStrs[i] = strconv.Itoa(id)
		}
		q := url.Values{}
		q.Set("ids", strings.Join(idStrs, ","))
		q.Set("prefill_event_id", strconv.Itoa(firstID))
		if commonOrgID > 0 {
			q.Set("org_id", strconv.Itoa(commonOrgID))
		}
		http.Redirect(w, r, "/admin/series/new?"+q.Encode(), http.StatusSeeOther)
	}
}

func adminEventBulkSetAttributesHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var ids []int
		for _, s := range r.Form["event_ids"] {
			if id, err := strconv.Atoi(s); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
			return
		}
		token := getSessionToken(r)
		// Location and series assignment are handled separately before the
		// generic attributes payload because they use dedicated API endpoints.
		if v := r.FormValue("location_id"); v != "" {
			if locID, err := strconv.Atoi(v); err == nil && locID > 0 {
				_ = client.BulkSetEventLocation(r.Context(), ids, locID, token)
			}
		}
		if v := r.FormValue("series_id"); v != "" {
			if seriesID, err := strconv.Atoi(v); err == nil && seriesID > 0 {
				_ = client.AssignEventsToSeries(r.Context(), seriesID, ids, token)
			}
		}
		payload := map[string]any{"ids": ids}
		if v := r.FormValue("org_id"); v != "" {
			id, _ := strconv.Atoi(v)
			payload["org_id"] = id
		}
		if tags := r.Form["add_tags"]; len(tags) > 0 {
			payload["add_tags"] = tags
		}
		if danceStrs := r.Form["add_dances"]; len(danceStrs) > 0 {
			var danceIDs []int
			for _, s := range danceStrs {
				if id, err := strconv.Atoi(s); err == nil {
					danceIDs = append(danceIDs, id)
				}
			}
			if len(danceIDs) > 0 {
				payload["add_dances"] = danceIDs
			}
		}
		if v := r.FormValue("food"); v != "" && v != "__skip__" {
			s := v
			if s == "__unset__" {
				s = ""
			}
			payload["food"] = s
		}
		if v := r.FormValue("drink"); v != "" && v != "__skip__" {
			s := v
			if s == "__unset__" {
				s = ""
			}
			payload["drink"] = s
		}
		for _, attr := range []string{"wheelchair", "bar", "kitchen"} {
			if v := r.FormValue("attr_" + attr); v == "1" || v == "0" {
				payload[attr] = v == "1"
			}
		}
		if v := r.FormValue("pricing_type"); v != "" && v != "__skip__" {
			payload["pricing_type"] = v
		}
		_ = client.BulkSetEventAttributes(r.Context(), payload, token)
		http.Redirect(w, r, safeReferer(r, "/admin/events"), http.StatusSeeOther)
	}
}

// adminEventMergeHandler merges two or more selected events into one.
// Among user-edited events the newest (by changed_at) is the base; otherwise
// the newest by created_at. Tags, musicians and dances are unioned; other empty
// fields are filled from non-base events. Non-base events are deleted.
func adminEventMergeHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
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
		ids := parseFormIDs(r.Form, "event_ids")
		mergeBack := safeReferer(r, "/admin/events")
		if len(ids) < 2 {
			http.Redirect(w, r, mergeBack, http.StatusSeeOther)
			return
		}
		// Remove duplicate IDs
		idMap := make(map[int]bool)
		uniqueIDs := make([]int, 0, len(ids))
		for _, id := range ids {
			if !idMap[id] {
				idMap[id] = true
				uniqueIDs = append(uniqueIDs, id)
			}
		}
		if len(uniqueIDs) < 2 {
			http.Redirect(w, r, mergeBack, http.StatusSeeOther)
			return
		}
		ids = uniqueIDs
		ctx := r.Context()
		token := getSessionToken(r)

		ph := make([]string, len(ids))
		qargs := make([]interface{}, len(ids))
		for i, id := range ids {
			ph[i] = "?"
			qargs[i] = id
		}
		inClause := "(" + strings.Join(ph, ",") + ")"

		// Determine base from API (newest user-edited, or newest by created_at).
		type evMeta struct {
			id        int
			changedAt int64
			changedBy string
			createdAt string
		}
		var evMetas []evMeta
		for _, id := range ids {
			ev, err := client.GetEventAuthed(ctx, id, token)
			if err != nil {
				log.Printf("Event merge: failed to get event %d: %v", id, err)
				continue
			}
			evMetas = append(evMetas, evMeta{
				id:        ev.ID,
				changedAt: parseChangedAt(ev.ChangedAt),
				changedBy: ev.ChangedBy,
				createdAt: ev.CreatedAt,
			})
		}
		if len(evMetas) < 2 {
			http.Redirect(w, r, mergeBack, http.StatusSeeOther)
			return
		}

		hasEdited := false
		for _, em := range evMetas {
			if em.changedBy != "" {
				hasEdited = true
				break
			}
		}
		if len(evMetas) == 0 {
			http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
			return
		}
		baseMeta := evMetas[0]
		for _, em := range evMetas[1:] {
			if hasEdited {
				baseEdited := baseMeta.changedBy != ""
				emEdited := em.changedBy != ""
				if !baseEdited && emEdited {
					baseMeta = em
					continue
				}
				if baseEdited && !emEdited {
					continue
				}
				if em.changedAt > baseMeta.changedAt {
					baseMeta = em
				}
			} else {
				if em.createdAt > baseMeta.createdAt {
					baseMeta = em
				}
			}
		}
		baseID := baseMeta.id

		base, err := client.GetEventAuthed(ctx, baseID, token)
		if err != nil {
			http.Redirect(w, r, mergeBack, http.StatusSeeOther)
			return
		}

		tagSet := make(map[string]bool)
		for _, t := range base.Tags {
			tagSet[t] = true
		}
		musicianSet := make(map[int]bool)
		for _, m := range base.Musicians {
			musicianSet[m.ID] = true
		}

		for _, id := range ids {
			if id == baseID {
				continue
			}
			ev, err := client.GetEventAuthed(ctx, id, token)
			if err != nil {
				continue
			}
			if base.Description == "" {
				base.Description = ev.Description
			}
			if base.URL == "" {
				base.URL = ev.URL
			}
			if base.BookingURL == "" {
				base.BookingURL = ev.BookingURL
			}
			if base.Availability == "" {
				base.Availability = ev.Availability
			}
			if base.Pricing == nil {
				base.Pricing = ev.Pricing
			}
			if base.Food == "" {
				base.Food = ev.Food
			}
			if base.Drink == "" {
				base.Drink = ev.Drink
			}
			if base.WorkshopDifficulty == "" {
				base.WorkshopDifficulty = ev.WorkshopDifficulty
			}
			base.HasBall = base.HasBall || ev.HasBall
			base.HasWorkshop = base.HasWorkshop || ev.HasWorkshop
			base.HasFestival = base.HasFestival || ev.HasFestival
			for _, t := range ev.Tags {
				tagSet[t] = true
			}
			for _, m := range ev.Musicians {
				musicianSet[m.ID] = true
			}
			// Prefer a richer location over a stub: coords beat no-coords;
			// a non-"Unknown" name beats "Unknown".
			if locationQuality(ev.Location) > locationQuality(base.Location) {
				base.LocationID = ev.LocationID
				base.Location = ev.Location
			}
		}

		tags := make([]string, 0, len(tagSet))
		for t := range tagSet {
			tags = append(tags, t)
		}
		mids := make([]int, 0, len(musicianSet))
		for id := range musicianSet {
			mids = append(mids, id)
		}

		drows, _ := db.QueryContext(ctx,
			"SELECT DISTINCT dance_id FROM event_dances WHERE event_id IN "+inClause,
			qargs...)
		var danceIDs []int
		if drows != nil {
			for drows.Next() {
				var did int
				drows.Scan(&did)
				danceIDs = append(danceIDs, did)
			}
			drows.Close()
		}

		req := EventUpdateReq{
			Title:              base.Title,
			Description:        base.Description,
			StartTime:          base.StartTime,
			EndTime:            base.EndTime,
			HasBall:            base.HasBall,
			HasWorkshop:        base.HasWorkshop,
			HasFestival:        base.HasFestival,
			WorkshopDifficulty: base.WorkshopDifficulty,
			IsCancelled:        base.IsCancelled,
			IsPublished:        base.IsPublished,
			BookingURL:         base.BookingURL,
			Availability:       base.Availability,
			TicketsTotal:       base.TicketsTotal,
			BookingEnabled:     base.BookingEnabled,
			Food:               base.Food,
			Drink:              base.Drink,
			Attributes:         base.Attributes,
			ContactName:        base.ContactName,
			ContactEmail:       base.ContactEmail,
			Tags:               tags,
			URL:                base.URL,
			OrganizationID:     base.OrganizationID,
			Pricing:            base.Pricing,
			LocationID:         base.LocationID,
			Musicians:          mids,
			Dances:             danceIDs,
		}
		_, _ = client.UpdateEvent(ctx, baseID, req, token)

		for _, id := range ids {
			if id != baseID {
				_ = client.DeleteEvent(ctx, id, token)
			}
		}

		client.invalidateEvents()
		// Preserve filter params. They arrive as POST body fields (the JS on the
		// listing page copies window.location.search into hidden inputs), so
		// r.URL.Query() is empty — read from r.Form instead.
		filterKeys := []string{
			"include_past", "org_id", "city", "location_id", "musician_id",
			"type", "dance", "date_from", "date_to", "source",
			"unpublished", "flagged", "created_after",
		}
		q := url.Values{}
		for _, k := range filterKeys {
			if v := r.FormValue(k); v != "" {
				q.Set(k, v)
			}
		}
		basePath := "/admin/events"
		if rp := safeReturnPath(r.FormValue("return_path")); rp != "" {
			if u, err := url.Parse(rp); err == nil {
				basePath = u.Path
			}
		}
		redirectURL := basePath
		if len(q) > 0 {
			redirectURL += "?" + q.Encode()
		}
		http.Redirect(w, r, redirectURL, http.StatusSeeOther)
	}
}

func adminEventImageDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		_ = client.DeleteEventImage(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, fmt.Sprintf("/admin/events/%d/edit", id), http.StatusSeeOther)
	}
}

// orgIDSet converts a slice of org IDs to a set for O(1) membership tests.
func orgIDSet(ids []int) map[int]bool {
	m := make(map[int]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// getUserOrgIDs returns the IDs of all organisations the given user belongs to.
func getUserOrgIDs(ctx context.Context, client *DansalClient, userID int, token string) []int {
	ids, _ := client.GetUserOrganizationIDs(ctx, userID, token)
	return ids
}

// prefillFromEvent builds an EventPrefill from an existing event for clone/template use.
// Date is intentionally omitted so the user must pick a new one; time-of-day is kept.
func prefillFromEvent(ev Event) *EventPrefill {
	pf := &EventPrefill{
		Title:              ev.Title,
		Description:        ev.Description,
		URL:                ev.URL,
		BookingURL:         ev.BookingURL,
		HasBall:            ev.HasBall,
		HasWorkshop:        ev.HasWorkshop,
		HasFestival:        ev.HasFestival,
		WorkshopDifficulty: ev.WorkshopDifficulty,
		StartTime:          isoTimeStr(ev.StartTime),
		EndTime:            isoTimeStr(ev.EndTime),
		Location: func() string {
			if ev.Location != nil {
				return ev.Location.Location
			}
			return ""
		}(),
		Town: func() string {
			if ev.Location != nil {
				return ev.Location.Town
			}
			return ""
		}(),
		Country: func() string {
			if ev.Location != nil {
				return ev.Location.Country
			}
			return ""
		}(),
		Tags:         ev.Tags,
		OriginalDate: isoDateStr(ev.StartTime),
	}
	if ev.OrganizationID != nil {
		pf.OrgID = *ev.OrganizationID
	}
	if ev.LocationID != nil {
		pf.LocID = *ev.LocationID
	}
	if p := ev.Pricing; p != nil {
		pf.PricingType = p.Type
		pf.PricingAmount = p.Amount
		pf.PricingCurrency = p.Currency
		pf.PricingLines = p.Prices
	}
	for _, dn := range ev.DanceNames {
		_ = dn // dance names need to be resolved to IDs separately
	}
	return pf
}

// templateAccessible mirrors listTemplates' access rule (owner, or org member
// for an org-scoped template; admins see everything) for the single-template
// lookup used by the ?tpl_id= prefill path, so a guessed/stale template ID
// belonging to another user's personal or a foreign org's template can't leak
// its contents.
func templateAccessible(tpl EventTemplate, userID int, isAdmin bool, orgIDs []int) bool {
	if isAdmin || tpl.UserID == userID {
		return true
	}
	if tpl.OrgID == nil {
		return false
	}
	for _, oid := range orgIDs {
		if oid == *tpl.OrgID {
			return true
		}
	}
	return false
}

// prefillFromTemplate builds an EventPrefill from a saved template — the
// counterpart to prefillFromEvent for the dashboard's "+ New event" preset
// shortcut (/admin/events/new?tpl_id=...). Title/description are
// deliberately left blank: templates don't carry them (see
// templateEventData), unlike clone_from which copies an existing event's
// title as a starting point.
func prefillFromTemplate(td templateEventData) *EventPrefill {
	pf := &EventPrefill{
		URL:                td.URL,
		BookingURL:         td.BookingURL,
		HasBall:            td.HasBall,
		HasWorkshop:        td.HasWorkshop,
		HasFestival:        td.HasFestival,
		WorkshopDifficulty: td.WorkshopDifficulty,
		StartTime:          isoTimeStr(td.StartTime),
		EndTime:            isoTimeStr(td.EndTime),
		OrgID:              td.OrgID,
		LocID:              td.LocID,
		PricingType:        td.PricingType,
		PricingAmount:      td.PricingAmount,
		PricingCurrency:    td.PricingCurrency,
		PricingLines:       td.PricingLines,
		Tags:               td.Tags,
		DanceIDs:           td.DanceIDs,
		Food:               td.Food,
		Drink:              td.Drink,
		TicketsTotal:       td.TicketsTotal,
		BookingEnabled:     td.BookingEnabled,
		Timetable:          td.Timetable,
	}
	return pf
}

// isoDateStr extracts "YYYY-MM-DD" from "YYYY-MM-DDTHH:MM:SS".
func isoDateStr(t string) string {
	if len(t) >= 10 {
		return t[:10]
	}
	return ""
}

// isoTimeStr extracts "HH:MM" from "YYYY-MM-DDTHH:MM:SS".
func isoTimeStr(t string) string {
	if len(t) >= 16 {
		return t[11:16]
	}
	return ""
}

// templateTimeString wraps a bare "HH:MM" (from a <input type=time>) in a
// placeholder-date RFC3339 string, since templateEventData.StartTime/EndTime
// (unlike Timetable entries, which store bare "HH:MM") is read back via
// isoTimeStr, which expects a full timestamp of at least 16 characters.
func templateTimeString(hhmm string) string {
	if hhmm == "" {
		return ""
	}
	return "2000-01-01T" + hhmm + ":00Z"
}

// mergeTemplateTime handles both HH:MM (old template format) and full RFC3339
// (new format). For HH:MM it replaces the time portion of eventTime while
// keeping its date and timezone suffix, avoiding a 400 from the API.
func mergeTemplateTime(eventTime, templateTime string) string {
	if len(templateTime) == 5 && templateTime[2] == ':' && len(eventTime) >= 19 {
		// HH:MM — graft onto the event's date, preserving any timezone suffix
		suffix := ""
		if len(eventTime) > 19 {
			suffix = eventTime[19:]
		}
		return eventTime[:11] + templateTime + ":00" + suffix
	}
	return templateTime
}

// fetchTemplateData looks up a template's JSON from web.db by ID.
// Returns "" when templateID is nil or the template isn't found.
func fetchTemplateData(db *sql.DB, templateID *int) string {
	if templateID == nil {
		return ""
	}
	tpl, err := getTemplate(db, *templateID)
	if err != nil {
		return ""
	}
	return tpl.Data
}

// applyTemplateFields copies selected fields from td into req.
// locations is used to resolve td.LocID; passing nil skips the location lookup.
func applyTemplateFields(req *EventUpdateReq, td *templateEventData, fields map[string]bool, locations []Location) {
	if fields["timing"] {
		req.StartTime = mergeTemplateTime(req.StartTime, td.StartTime)
		req.EndTime = mergeTemplateTime(req.EndTime, td.EndTime)
	}
	if fields["org"] && td.OrgID > 0 {
		oid := td.OrgID
		req.OrganizationID = &oid
	}
	if fields["loc"] && td.LocID > 0 {
		for _, l := range locations {
			if l.ID == td.LocID {
				req.Location = EventLocReq{
					Location:  l.Location,
					Address:   l.Address,
					Town:      l.Town,
					Country:   l.Country,
					Latitude:  l.Latitude,
					Longitude: l.Longitude,
				}
				break
			}
		}
	}
	if fields["type_flags"] {
		req.HasBall = td.HasBall
		req.HasWorkshop = td.HasWorkshop
		req.HasFestival = td.HasFestival
		req.WorkshopDifficulty = td.WorkshopDifficulty
	}
	if fields["url"] {
		req.URL = td.URL
		req.BookingURL = td.BookingURL
	}
	if fields["pricing"] {
		if td.PricingType != "" && td.PricingType != "none" {
			p := &Pricing{Type: td.PricingType}
			switch td.PricingType {
			case "single":
				p.Amount = td.PricingAmount
				p.Currency = td.PricingCurrency
			case "multiple":
				p.Prices = td.PricingLines
			}
			req.Pricing = p
		} else {
			req.Pricing = nil
		}
	}
	if fields["tags"] {
		req.Tags = td.Tags
	}
	if fields["dances"] {
		req.Dances = td.DanceIDs
	}
	if fields["food_drink"] {
		req.Food = td.Food
		req.Drink = td.Drink
		req.FloorCondition = td.FloorCondition
	}
	if fields["booking"] {
		req.BookingEnabled = td.BookingEnabled
		req.TicketsTotal = td.TicketsTotal
	}
}

// applyTemplateFieldsCreate mirrors applyTemplateFields for the create path.
// EventCreateReq has no BookingEnabled/TicketsTotal fields (the create API
// doesn't support them yet), so the "booking" field set is a no-op here.
func applyTemplateFieldsCreate(req *EventCreateReq, td *templateEventData, fields map[string]bool, locations []Location) {
	if fields["timing"] {
		req.StartTime = mergeTemplateTime(req.StartTime, td.StartTime)
		req.EndTime = mergeTemplateTime(req.EndTime, td.EndTime)
	}
	if fields["org"] && td.OrgID > 0 {
		oid := td.OrgID
		req.OrganizationID = &oid
	}
	if fields["loc"] && td.LocID > 0 {
		for _, l := range locations {
			if l.ID == td.LocID {
				req.Location = EventLocReq{
					Location:  l.Location,
					Address:   l.Address,
					Town:      l.Town,
					Country:   l.Country,
					Latitude:  l.Latitude,
					Longitude: l.Longitude,
				}
				break
			}
		}
	}
	if fields["type_flags"] {
		req.HasBall = td.HasBall
		req.HasWorkshop = td.HasWorkshop
		req.HasFestival = td.HasFestival
		req.WorkshopDifficulty = td.WorkshopDifficulty
	}
	if fields["url"] {
		req.URL = td.URL
		req.BookingURL = td.BookingURL
	}
	if fields["pricing"] {
		if td.PricingType != "" && td.PricingType != "none" {
			p := &Pricing{Type: td.PricingType}
			switch td.PricingType {
			case "single":
				p.Amount = td.PricingAmount
				p.Currency = td.PricingCurrency
			case "multiple":
				p.Prices = td.PricingLines
			}
			req.Pricing = p
		} else {
			req.Pricing = nil
		}
	}
	if fields["tags"] {
		req.Tags = td.Tags
	}
	if fields["dances"] {
		req.Dances = td.DanceIDs
	}
	if fields["food_drink"] {
		req.Food = td.Food
		req.Drink = td.Drink
		req.FloorCondition = td.FloorCondition
	}
}

func locationDisplayName(l Location) string {
	name := l.ShortName
	if name == "" {
		name = l.Location
	}
	if l.Town != "" {
		name += ", " + l.Town
	}
	return name
}

func adminEventsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}

		q := r.URL.Query()
		includePast := q.Get("include_past") == "1"
		orgID, _ := strconv.Atoi(q.Get("org_id"))
		locationID, _ := strconv.Atoi(q.Get("location_id"))
		musicianID, _ := strconv.Atoi(q.Get("musician_id"))
		dateFrom := q.Get("date_from")
		dateTo := q.Get("date_to")
		filterType := q.Get("type")
		filterDance := q.Get("dance")
		filterCity := q.Get("city")
		createdAfter := q.Get("created_after")
		filterSource := q.Get("source")
		filterUnpublished := q.Get("unpublished") == "1"
		filterFlagged := q.Get("flagged") == "1"

		params := url.Values{}
		if includePast {
			params.Set("include_past", "true")
		}
		if dateFrom != "" {
			if t, err := time.Parse("2006-01-02", dateFrom); err == nil {
				params.Set("start_time_after", strconv.FormatInt(t.Unix()-1, 10))
			}
		}
		if dateTo != "" {
			if t, err := time.Parse("2006-01-02", dateTo); err == nil {
				params.Set("start_time_before", strconv.FormatInt(t.Add(24*time.Hour).Unix(), 10))
			}
		}
		if locationID != 0 {
			params.Set("location_id", strconv.Itoa(locationID))
		}
		if musicianID != 0 {
			params.Set("musician_id", strconv.Itoa(musicianID))
		}
		if createdAfter != "" {
			params.Set("created_after", createdAfter)
		}
		if filterSource != "" {
			params.Set("source", filterSource)
		}
		if filterUnpublished {
			params.Set("is_published", "false")
			if !includePast {
				params.Set("include_past", "true")
			}
		}
		if filterFlagged && !includePast {
			params.Set("include_past", "true")
		}

		// Fetch all events when any filter is active so in-memory filters
		// (org, type, dance, city, flagged) operate on the full result set.
		hasFilter := includePast || orgID != 0 || locationID != 0 || musicianID != 0 ||
			dateFrom != "" || dateTo != "" || filterType != "" || filterDance != "" ||
			filterCity != "" || createdAfter != "" || filterSource != "" ||
			filterUnpublished || filterFlagged
		limit := 100
		if hasFilter {
			limit = 1000
			params.Set("limit", "1000")
		} else {
			params.Set("limit", strconv.Itoa(limit))
		}

		// org/type/dance/city/flagged are narrowed in-memory below (the API has
		// no matching query params for them), which decouples the API's `total`
		// from what's actually displayed. Offset-based pagination is therefore
		// only offered when none of those are active, so Prev/Next stay accurate.
		clientFilterActive := orgID != 0 || filterType != "" || filterDance != "" || filterCity != "" || filterFlagged
		offset := 0
		if !clientFilterActive {
			if o, err := strconv.Atoi(q.Get("offset")); err == nil && o > 0 {
				offset = o
			}
		}
		if offset > 0 {
			params.Set("offset", strconv.Itoa(offset))
		}

		token := getSessionToken(r)
		events, total, err := client.GetAdminEventsWithTotal(r.Context(), token, params)
		if err != nil {
			http.Error(w, "could not load events", http.StatusBadGateway)
			return
		}
		// org filter: -1 = no org assigned, >0 = specific org
		if orgID == -1 {
			filtered := events[:0]
			for _, e := range events {
				if e.OrganizationID == nil {
					filtered = append(filtered, e)
				}
			}
			events = filtered
		} else if orgID != 0 {
			filtered := events[:0]
			for _, e := range events {
				if e.OrganizationID != nil && *e.OrganizationID == orgID {
					filtered = append(filtered, e)
				}
			}
			events = filtered
		}
		if filterType != "" {
			filtered := events[:0]
			for _, e := range events {
				switch filterType {
				case "ball":
					if e.HasBall {
						filtered = append(filtered, e)
					}
				case "workshop":
					if e.HasWorkshop {
						filtered = append(filtered, e)
					}
				case "festival":
					if e.HasFestival {
						filtered = append(filtered, e)
					}
				}
			}
			events = filtered
		}
		if filterDance != "" {
			filtered := events[:0]
			for _, e := range events {
				for _, d := range e.DanceNames {
					if d == filterDance {
						filtered = append(filtered, e)
						break
					}
				}
			}
			events = filtered
		}
		if filterCity != "" {
			filtered := events[:0]
			for _, e := range events {
				if e.Location != nil && e.Location.Town == filterCity {
					filtered = append(filtered, e)
				}
			}
			events = filtered
		}
		if filterFlagged {
			filtered := events[:0]
			for _, e := range events {
				if e.NeedsDuplicateReview {
					filtered = append(filtered, e)
				}
			}
			events = filtered
		}

		var prevURL, nextURL string
		if !clientFilterActive {
			pageURL := func(off int) string {
				v := url.Values{}
				for k, vv := range r.URL.Query() {
					v[k] = vv
				}
				if off > 0 {
					v.Set("offset", strconv.Itoa(off))
				} else {
					v.Del("offset")
				}
				return "/admin/events?" + v.Encode()
			}
			if offset > 0 {
				p := offset - limit
				if p < 0 {
					p = 0
				}
				prevURL = pageURL(p)
			}
			if offset+len(events) < total {
				nextURL = pageURL(offset + limit)
			}
		}

		orgs, _ := client.GetOrganizations(r.Context())
		locs, _ := client.GetLocations(r.Context())
		musicians, _ := client.GetMusicians(r.Context())
		dances, _ := client.GetDances(r.Context())
		allTags, _ := client.GetTags(r.Context())
		series, _ := client.GetSeriesList(r.Context(), token)
		sort.Slice(locs, func(i, j int) bool {
			if locs[i].Town != locs[j].Town {
				return locs[i].Town < locs[j].Town
			}
			ni := locs[i].ShortName
			if ni == "" {
				ni = locs[i].Location
			}
			nj := locs[j].ShortName
			if nj == "" {
				nj = locs[j].Location
			}
			return ni < nj
		})

		title := i18n.T(r, "admin_events_title")
		orgMap := make(map[int]string, len(orgs))
		for _, o := range orgs {
			orgMap[o.ID] = o.Name
		}
		var filterOrgName string
		if orgID == -1 {
			filterOrgName = i18n.T(r, "filter_no_org")
		} else if orgID != 0 {
			filterOrgName = orgMap[orgID]
		}
		var filterLocationName string
		if locationID != 0 {
			for _, l := range locs {
				if l.ID == locationID {
					filterLocationName = locationDisplayName(l)
					break
				}
			}
		}
		renderTemplate(w, tmpls.adminEvents, tmplData(r, cfg, i18n, title, AdminEventsData{
			Events:             events,
			Organizations:      orgs,
			OrgMap:             orgMap,
			Locations:          locs,
			Musicians:          musicians,
			Dances:             dances,
			AllTags:            allTags,
			Series:             series,
			FilterIncludePast:  includePast,
			FilterOrgID:        orgID,
			FilterOrgName:      filterOrgName,
			FilterLocationID:   locationID,
			FilterLocationName: filterLocationName,
			FilterCity:         filterCity,
			FilterDateFrom:     dateFrom,
			FilterDateTo:       dateTo,
			FilterMusicianID:   musicianID,
			FilterType:         filterType,
			FilterDance:        filterDance,
			FilterCreatedAfter: createdAfter,
			FilterSource:       filterSource,
			FilterUnpublished:  filterUnpublished,
			FilterFlagged:      filterFlagged,
			TotalCount:         total,
			PrevURL:            prevURL,
			NextURL:            nextURL,
		}))
	}
}

func adminEventNewPageHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		bundle := client.FetchRefBundle(r.Context())
		defaultDanceIDs := loadDefaultDanceIDs(db)
		selected := buildSelectedDanceNamesFromIDs(defaultDanceIDs, bundle.Dances)

		token := getSessionToken(r)
		var cachedUserOrgIDs []int
		getUserOrgs := func() []int {
			if cachedUserOrgIDs != nil {
				return cachedUserOrgIDs
			}
			if su.Role == "admin" {
				for _, o := range bundle.Orgs {
					cachedUserOrgIDs = append(cachedUserOrgIDs, o.ID)
				}
			} else {
				cachedUserOrgIDs = getUserOrgIDs(r.Context(), client, su.ID, token)
			}
			return cachedUserOrgIDs
		}

		var prefill *EventPrefill

		if cloneID := r.URL.Query().Get("clone_from"); cloneID != "" {
			if id, err := strconv.Atoi(cloneID); err == nil {
				if ev, err := client.GetEvent(r.Context(), id); err == nil {
					pf := prefillFromEvent(ev)
					// Non-admin cloning an event from an org they don't belong to:
					// clear org and location so it can't expose restricted data.
					if su.Role != "admin" && ev.OrganizationID != nil {
						member := false
						for _, oid := range getUserOrgs() {
							if oid == *ev.OrganizationID {
								member = true
								break
							}
						}
						if !member {
							pf.OrgID = 0
							pf.LocID = 0
							pf.Location = ""
							pf.Town = ""
							pf.Country = ""
						}
					}
					pf.CloneMode = true
					prefill = pf
				}
			}
		} else if tplIDStr := r.URL.Query().Get("tpl_id"); tplIDStr != "" {
			if tplID, err := strconv.Atoi(tplIDStr); err == nil {
				if tpl, err := getTemplate(db, tplID); err == nil && templateAccessible(tpl, su.ID, su.Role == "admin", getUserOrgs()) {
					var td templateEventData
					if json.Unmarshal([]byte(tpl.Data), &td) == nil {
						prefill = prefillFromTemplate(td)
					}
				}
			}
		} else if r.URL.Query().Get("title") != "" {
			prefillTags := r.URL.Query()["tags"]
			prefill = &EventPrefill{
				Title:       r.URL.Query().Get("title"),
				Description: r.URL.Query().Get("description"),
				URL:         r.URL.Query().Get("url"),
				Date:        r.URL.Query().Get("date"),
				EndDate:     r.URL.Query().Get("end_date"),
				StartTime:   r.URL.Query().Get("start_time"),
				EndTime:     r.URL.Query().Get("end_time"),
				Location:    r.URL.Query().Get("location"),
				Town:        r.URL.Query().Get("town"),
				Country:     r.URL.Query().Get("country"),
				Tags:        prefillTags,
			}
		}

		if prefill == nil {
			oid, _ := strconv.Atoi(r.URL.Query().Get("org_id"))
			lid, _ := strconv.Atoi(r.URL.Query().Get("loc_id"))
			if oid > 0 || lid > 0 {
				prefill = &EventPrefill{OrgID: oid, LocID: lid}
			}
		}

		tmpls2, _ := listTemplates(db, su.ID, getUserOrgs())

		var userOrgs []Organization
		if su.Role == "admin" {
			userOrgs = bundle.Orgs
		} else {
			orgIDSet := make(map[int]bool)
			for _, oid := range getUserOrgs() {
				orgIDSet[oid] = true
			}
			for _, o := range bundle.Orgs {
				if orgIDSet[o.ID] {
					userOrgs = append(userOrgs, o)
				}
			}
		}

		event := eventFromPrefill(prefill)
		locOrgFirst, locOthers := splitEventLocations(bundle.Locations, event)
		var eventOrg *Organization
		if event.OrganizationID != nil {
			for i := range bundle.Orgs {
				if bundle.Orgs[i].ID == *event.OrganizationID {
					eventOrg = &bundle.Orgs[i]
					break
				}
			}
		}

		title := i18n.T(r, "admin_event_new_title")
		renderTemplate(w, tmpls.adminEventForm, tmplData(r, cfg, i18n, title, AdminEventFormData{
			IsNew:              true,
			Event:              event,
			Org:                eventOrg,
			Organizations:      bundle.Orgs,
			Locations:          bundle.Locations,
			LocOrgFirst:        locOrgFirst,
			LocOthers:          locOthers,
			Musicians:          bundle.Musicians,
			Instructors:        bundle.Instructors,
			Dances:             bundle.Dances,
			SelectedDanceNames: selected,
			UserOrgs:           userOrgs,
			Templates:          tmpls2,
			Prefill:            prefill,
		}))
	}
}

// ── Dedicated template-creation mode (#841) ──────────────────────────────────
//
// /admin/templates/new reuses admin_event_form.html (IsTemplateMode:true hides
// Title/Description/Date and the apply-template panel, and promotes the
// name/org fields) but never calls client.CreateEvent — it builds a
// templateEventData directly from the submitted fields and saves it, so no
// draft event is created as a side effect the way the old "save as template"
// intent on a real event did.

func adminTemplateNewPageHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		bundle := client.FetchRefBundle(r.Context())
		token := getSessionToken(r)

		var userOrgs []Organization
		if su.Role == "admin" {
			userOrgs = bundle.Orgs
		} else {
			orgIDSet := make(map[int]bool)
			for _, oid := range getUserOrgIDs(r.Context(), client, su.ID, token) {
				orgIDSet[oid] = true
			}
			for _, o := range bundle.Orgs {
				if orgIDSet[o.ID] {
					userOrgs = append(userOrgs, o)
				}
			}
		}

		var prefill *EventPrefill
		oid, _ := strconv.Atoi(r.URL.Query().Get("org_id"))
		if oid > 0 {
			prefill = &EventPrefill{OrgID: oid}
		}
		event := eventFromPrefill(prefill)
		locOrgFirst, locOthers := splitEventLocations(bundle.Locations, event)

		title := i18n.T(r, "admin_template_new_title")
		renderTemplate(w, tmpls.adminEventForm, tmplData(r, cfg, i18n, title, AdminEventFormData{
			IsNew:          true,
			IsTemplateMode: true,
			Event:          event,
			Organizations:  bundle.Orgs,
			Locations:      bundle.Locations,
			LocOrgFirst:    locOrgFirst,
			LocOthers:      locOthers,
			Dances:         bundle.Dances,
			UserOrgs:       userOrgs,
			Prefill:        prefill,
		}))
	}
}

func adminTemplateCreateHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		bundle := client.FetchRefBundle(r.Context())
		token := getSessionToken(r)

		var userOrgs []Organization
		if su.Role == "admin" {
			userOrgs = bundle.Orgs
		} else {
			orgIDSet := make(map[int]bool)
			for _, oid := range getUserOrgIDs(r.Context(), client, su.ID, token) {
				orgIDSet[oid] = true
			}
			for _, o := range bundle.Orgs {
				if orgIDSet[o.ID] {
					userOrgs = append(userOrgs, o)
				}
			}
		}

		renderErr := func(errKey string) {
			title := i18n.T(r, "admin_template_new_title")
			renderTemplate(w, tmpls.adminEventForm, tmplData(r, cfg, i18n, title, AdminEventFormData{
				IsNew:          true,
				IsTemplateMode: true,
				Organizations:  bundle.Orgs,
				Locations:      bundle.Locations,
				Dances:         bundle.Dances,
				UserOrgs:       userOrgs,
				ErrorKey:       errKey,
			}))
		}

		name := strings.TrimSpace(r.FormValue("tpl_name"))
		if name == "" {
			renderErr("admin_template_name_required")
			return
		}

		var tplOrgID *int
		if v := strings.TrimSpace(r.FormValue("tpl_org_id")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				tplOrgID = &n
			}
		}

		var orgID int
		switch r.FormValue("org_choice") {
		case "existing":
			if v := r.FormValue("org_id"); v != "" {
				orgID, _ = strconv.Atoi(v)
			}
		case "new":
			newOrgName := strings.TrimSpace(r.FormValue("new_org_name"))
			if newOrgName != "" {
				created, err := client.CreateOrganization(r.Context(), Organization{Name: newOrgName}, token)
				if err != nil {
					renderErr("admin_save_error")
					return
				}
				orgID = created.ID
			}
		}

		var locID int
		if r.FormValue("loc_choice") == "existing" {
			if v := r.FormValue("loc_id"); v != "" {
				locID, _ = strconv.Atoi(v)
			}
		}

		var pricing *Pricing
		if pt := r.FormValue("pricing_type"); pt != "" && pt != "none" {
			p := &Pricing{Type: pt}
			switch pt {
			case "single":
				if amt := r.FormValue("pricing_amount"); amt != "" {
					if f, err := strconv.ParseFloat(amt, 64); err == nil {
						p.Amount = f
					}
				}
				p.Currency = strings.TrimSpace(r.FormValue("pricing_currency"))
			case "multiple":
				labels := r.Form["pl_label"]
				amounts := r.Form["pl_amount"]
				for i, lbl := range labels {
					lbl = strings.TrimSpace(lbl)
					if lbl == "" {
						continue
					}
					var amt float64
					if i < len(amounts) {
						if f, err := strconv.ParseFloat(strings.TrimSpace(amounts[i]), 64); err == nil {
							amt = f
						}
					}
					p.Prices = append(p.Prices, Price{Label: lbl, Amount: amt})
				}
				if len(p.Prices) == 0 {
					p = nil
				}
			}
			pricing = p
		}

		tags := r.Form["tags"]
		var danceIDs []int
		for _, v := range r.Form["dance_ids"] {
			if n, err := strconv.Atoi(v); err == nil {
				danceIDs = append(danceIDs, n)
			}
		}

		starts := r.Form["tt_start"]
		ends := r.Form["tt_end"]
		titles := r.Form["tt_title"]
		descs := r.Form["tt_desc"]
		rooms := r.Form["tt_room"]
		ttTypes := r.Form["tt_type"]
		locIDs := r.Form["tt_loc_id"]
		musIDs := r.Form["tt_musician_id"]
		var ttEntries []TimetableEntry
		for i, s := range starts {
			s = strings.TrimSpace(s)
			if i >= len(titles) {
				break
			}
			t := strings.TrimSpace(titles[i])
			if s == "" && t == "" {
				continue
			}
			entry := TimetableEntry{StartTime: s, Title: t}
			if i < len(ends) {
				entry.EndTime = strings.TrimSpace(ends[i])
			}
			if i < len(descs) {
				entry.Description = strings.TrimSpace(descs[i])
			}
			if i < len(rooms) {
				entry.Room = strings.TrimSpace(rooms[i])
			}
			if i < len(ttTypes) {
				if v := strings.TrimSpace(ttTypes[i]); v == "workshop" {
					entry.EntryType = "workshop"
				} else {
					entry.EntryType = "bal"
				}
			}
			if i < len(locIDs) {
				if v, err := strconv.Atoi(strings.TrimSpace(locIDs[i])); err == nil && v > 0 {
					entry.LocationID = &v
				}
			}
			if i < len(musIDs) {
				if v, err := strconv.Atoi(strings.TrimSpace(musIDs[i])); err == nil && v > 0 {
					entry.MusicianID = &v
				}
			}
			ttEntries = append(ttEntries, entry)
		}

		td := templateEventData{
			URL:                strings.TrimSpace(r.FormValue("url")),
			BookingURL:         strings.TrimSpace(r.FormValue("booking_url")),
			StartTime:          templateTimeString(r.FormValue("start_time")),
			EndTime:            templateTimeString(r.FormValue("end_time")),
			HasBall:            sliceContains(tags, "bal-folk"),
			HasWorkshop:        sliceContains(tags, "dance-workshop") || sliceContains(tags, "musician-workshop"),
			HasFestival:        sliceContains(tags, "festival"),
			WorkshopDifficulty: r.FormValue("workshop_difficulty"),
			OrgID:              orgID,
			LocID:              locID,
			Tags:               tags,
			DanceIDs:           danceIDs,
			Food:               r.FormValue("food"),
			Drink:              r.FormValue("drink"),
			FloorCondition:     r.FormValue("floor_condition"),
			Attributes:         eventAttrsFromForm(r),
			ContactName:        strings.TrimSpace(r.FormValue("contact_name")),
			ContactEmail:       strings.TrimSpace(r.FormValue("contact_email")),
			BookingEnabled:     r.FormValue("booking_enabled") != "",
			Timetable:          ttEntries,
		}
		if pricing != nil {
			td.PricingType = pricing.Type
			td.PricingAmount = pricing.Amount
			td.PricingCurrency = pricing.Currency
			td.PricingLines = pricing.Prices
		}
		if n, err := strconv.Atoi(r.FormValue("tickets_total")); err == nil {
			td.TicketsTotal = n
		}

		data, err := json.Marshal(td)
		if err != nil {
			renderErr("admin_save_error")
			return
		}
		if _, err := saveTemplate(db, su.ID, tplOrgID, nil, nil, name, string(data)); err != nil {
			log.Printf("save template error: %v", err)
			renderErr("admin_save_error")
			return
		}
		http.Redirect(w, r, "/admin/templates", http.StatusSeeOther)
	}
}

func adminEventCreateHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		bundle := client.FetchRefBundle(r.Context())
		token := getSessionToken(r)
		renderErr := func(errKey string) {
			title := i18n.T(r, "admin_event_new_title")
			renderTemplate(w, tmpls.adminEventForm, tmplData(r, cfg, i18n, title, AdminEventFormData{
				IsNew:         true,
				Organizations: bundle.Orgs,
				Locations:     bundle.Locations,
				Musicians:     bundle.Musicians,
				Instructors:   bundle.Instructors,
				Dances:        bundle.Dances,
				ErrorKey:      errKey,
			}))
		}

		intent := r.FormValue("intent")

		date := r.FormValue("date")
		endDate := r.FormValue("end_date")
		if endDate == "" {
			endDate = date
		}
		startT := r.FormValue("start_time")
		endT := r.FormValue("end_time")
		startTime, endTime := "", ""
		if date != "" && startT != "" {
			startTime = date + "T" + startT + ":00"
		}
		if endDate != "" && endT != "" {
			endTime = endDate + "T" + endT + ":00"
		}

		var orgID *int
		switch r.FormValue("org_choice") {
		case "existing":
			if v := r.FormValue("org_id"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					orgID = &n
				}
			}
		case "new":
			newOrg := Organization{Name: strings.TrimSpace(r.FormValue("new_org_name"))}
			if newOrg.Name != "" {
				created, err := client.CreateOrganization(r.Context(), newOrg, getSessionToken(r))
				if err != nil {
					renderErr("admin_save_error")
					return
				}
				orgID = &created.ID
			}
		}

		var locReq EventLocReq
		var selectedLocID int
		switch r.FormValue("loc_choice") {
		case "existing":
			if v := r.FormValue("loc_id"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					for _, l := range bundle.Locations {
						if l.ID == n {
							selectedLocID = l.ID
							locReq = EventLocReq{
								Location:  l.Location,
								Address:   l.Address,
								Town:      l.Town,
								Country:   l.Country,
								Latitude:  l.Latitude,
								Longitude: l.Longitude,
							}
							break
						}
					}
				}
			}
		case "new":
			locReq = EventLocReq{
				Location:    strings.TrimSpace(r.FormValue("new_loc_name")),
				ShortName:   strings.TrimSpace(r.FormValue("new_loc_short_name")),
				Address:     strings.TrimSpace(r.FormValue("new_loc_address")),
				Zipcode:     strings.TrimSpace(r.FormValue("new_loc_zip")),
				Town:        strings.TrimSpace(r.FormValue("new_loc_town")),
				Country:     strings.TrimSpace(r.FormValue("new_loc_country")),
				CountryCode: strings.TrimSpace(r.FormValue("new_loc_country_code")),
				Region:      strings.TrimSpace(r.FormValue("new_loc_region")),
				Latitude:    parseLatLng(r.FormValue("new_loc_lat")),
				Longitude:   parseLatLng(r.FormValue("new_loc_lng")),
			}
		}

		createStandaloneLocation := r.FormValue("loc_choice") == "new" && locReq.Location != ""
		var pricing *Pricing
		if pt := r.FormValue("pricing_type"); pt != "" && pt != "none" {
			p := &Pricing{Type: pt}
			switch pt {
			case "single":
				if amt := r.FormValue("pricing_amount"); amt != "" {
					if f, err := strconv.ParseFloat(amt, 64); err == nil {
						p.Amount = f
					}
				}
				p.Currency = strings.TrimSpace(r.FormValue("pricing_currency"))
			case "multiple":
				labels := r.MultipartForm.Value["pl_label"]
				amounts := r.MultipartForm.Value["pl_amount"]
				for i, lbl := range labels {
					lbl = strings.TrimSpace(lbl)
					if lbl == "" {
						continue
					}
					var amt float64
					if i < len(amounts) {
						if f, err := strconv.ParseFloat(strings.TrimSpace(amounts[i]), 64); err == nil {
							amt = f
						}
					}
					p.Prices = append(p.Prices, Price{Label: lbl, Amount: amt})
				}
				if len(p.Prices) == 0 {
					p = nil
				}
			}
			pricing = p
		}

		tags := r.MultipartForm.Value["tags"]

		var danceIDs []int
		for _, v := range r.MultipartForm.Value["dance_ids"] {
			if n, err2 := strconv.Atoi(v); err2 == nil {
				danceIDs = append(danceIDs, n)
			}
		}

		var musicianIDs []int
		for _, v := range r.MultipartForm.Value["musician_ids"] {
			if n, err2 := strconv.Atoi(strings.TrimSpace(v)); err2 == nil {
				musicianIDs = append(musicianIDs, n)
			}
		}
		var instructorIDs []int
		for _, v := range r.MultipartForm.Value["instructor_ids"] {
			if n, err2 := strconv.Atoi(strings.TrimSpace(v)); err2 == nil {
				instructorIDs = append(instructorIDs, n)
			}
		}

		var createRoomID *int
		if v := r.FormValue("room_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				createRoomID = &n
			}
		}
		req := EventCreateReq{
			Title:          strings.TrimSpace(r.FormValue("title")),
			Description:    strings.TrimSpace(r.FormValue("description")),
			StartTime:      startTime,
			EndTime:        endTime,
			HasBall:        sliceContains(tags, "bal-folk"),
			HasWorkshop:    sliceContains(tags, "dance-workshop") || sliceContains(tags, "musician-workshop"),
			HasFestival:    sliceContains(tags, "festival"),
			BookingURL:     strings.TrimSpace(r.FormValue("booking_url")),
			Food:           r.FormValue("food"),
			Drink:          r.FormValue("drink"),
			FloorCondition: r.FormValue("floor_condition"),
			Attributes:     eventAttrsFromForm(r),
			ContactName:    strings.TrimSpace(r.FormValue("contact_name")),
			ContactEmail:   strings.TrimSpace(r.FormValue("contact_email")),
			Tags:           tags,
			URL:            strings.TrimSpace(r.FormValue("url")),
			OrganizationID: orgID,
			Pricing:        pricing,
			Location:       locReq,
			Musicians:      musicianIDs,
			Instructors:    instructorIDs,
			Dances:         danceIDs,
			RoomID:         createRoomID,
		}

		// Apply template overrides if submitted (suggestion acceptance flow).
		if tplIDStr := r.FormValue("tpl_id"); tplIDStr != "" {
			if tplID, err2 := strconv.Atoi(tplIDStr); err2 == nil && tplID > 0 {
				if tpl, err2 := getTemplate(db, tplID); err2 == nil {
					var td templateEventData
					if err2 := json.Unmarshal([]byte(tpl.Data), &td); err2 == nil {
						fieldsSet := make(map[string]bool)
						for _, f := range r.MultipartForm.Value["tpl_fields"] {
							fieldsSet[f] = true
						}
						applyTemplateFieldsCreate(&req, &td, fieldsSet, bundle.Locations)
					}
				}
			}
		}

		renderErrFull := func(errKey string) {
			ev := Event{
				Title:          req.Title,
				Description:    req.Description,
				StartTime:      req.StartTime,
				EndTime:        req.EndTime,
				HasBall:        req.HasBall,
				HasWorkshop:    req.HasWorkshop,
				HasFestival:    req.HasFestival,
				BookingURL:     req.BookingURL,
				Food:           req.Food,
				Drink:          req.Drink,
				FloorCondition: req.FloorCondition,
				Attributes:     req.Attributes,
				ContactName:    req.ContactName,
				ContactEmail:   req.ContactEmail,
				Tags:           req.Tags,
				URL:            req.URL,
				OrganizationID: req.OrganizationID,
				Pricing:        req.Pricing,
				Musicians:      musiciansByID(musicianIDs, bundle.Musicians),
				Instructors:    instructorsByID(instructorIDs, bundle.Instructors),
			}
			if selectedLocID > 0 {
				ev.LocationID = &selectedLocID
			}
			locOrgFirst, locOthers := splitEventLocations(bundle.Locations, ev)
			var eventOrg *Organization
			if ev.OrganizationID != nil {
				for i := range bundle.Orgs {
					if bundle.Orgs[i].ID == *ev.OrganizationID {
						eventOrg = &bundle.Orgs[i]
						break
					}
				}
			}
			var userOrgs []Organization
			if su.Role == "admin" {
				userOrgs = bundle.Orgs
			} else {
				orgIDSet := make(map[int]bool)
				for _, oid := range getUserOrgIDs(r.Context(), client, su.ID, token) {
					orgIDSet[oid] = true
				}
				for _, o := range bundle.Orgs {
					if orgIDSet[o.ID] {
						userOrgs = append(userOrgs, o)
					}
				}
			}
			title := i18n.T(r, "admin_event_new_title")
			renderTemplate(w, tmpls.adminEventForm, tmplData(r, cfg, i18n, title, AdminEventFormData{
				IsNew:              true,
				Event:              ev,
				Org:                eventOrg,
				Organizations:      bundle.Orgs,
				Locations:          bundle.Locations,
				LocOrgFirst:        locOrgFirst,
				LocOthers:          locOthers,
				Musicians:          bundle.Musicians,
				Instructors:        bundle.Instructors,
				Dances:             bundle.Dances,
				SelectedDanceNames: buildSelectedDanceNamesFromIDs(danceIDs, bundle.Dances),
				UserOrgs:           userOrgs,
				ErrorKey:           errKey,
			}))
		}

		if req.Title == "" {
			renderErrFull("evt_title_required")
			return
		}

		event, err := client.CreateEvent(r.Context(), req, token)
		if err != nil {
			log.Printf("create event error: %v", err)
			renderErrFull("admin_save_error")
			return
		}

		if createStandaloneLocation {
			newLoc := Location{
				Location:    locReq.Location,
				ShortName:   locReq.ShortName,
				Address:     locReq.Address,
				Zipcode:     locReq.Zipcode,
				Town:        locReq.Town,
				Country:     locReq.Country,
				CountryCode: locReq.CountryCode,
				Region:      locReq.Region,
				Latitude:    locReq.Latitude,
				Longitude:   locReq.Longitude,
			}
			if created, lerr := client.CreateLocation(r.Context(), newLoc, getSessionToken(r)); lerr == nil {
				if orgID != nil {
					_ = client.BulkAssignLocationOrg(r.Context(), []int{created.ID}, orgID, getSessionToken(r))
				}
			} else {
				log.Printf("create standalone location error: %v", lerr)
			}
		}

		if file, header, ferr := r.FormFile("image"); ferr == nil {
			defer file.Close()
			data, rerr := io.ReadAll(file)
			if rerr == nil {
				if uerr := client.UploadEventImage(r.Context(), event.ID, data, header.Filename, getSessionToken(r)); uerr != nil {
					log.Printf("upload image error: %v", uerr)
				}
			}
		}

		starts := r.MultipartForm.Value["tt_start"]
		ends := r.MultipartForm.Value["tt_end"]
		titles := r.MultipartForm.Value["tt_title"]
		descs := r.MultipartForm.Value["tt_desc"]
		rooms := r.MultipartForm.Value["tt_room"]
		ttTypes := r.MultipartForm.Value["tt_type"]
		locIDs := r.MultipartForm.Value["tt_loc_id"]
		musIDs := r.MultipartForm.Value["tt_musician_id"]
		musNames := r.MultipartForm.Value["tt_musician_name"]
		var ttEntries []TimetableEntryReq
		for i, s := range starts {
			s = strings.TrimSpace(s)
			if i >= len(titles) {
				break
			}
			t := strings.TrimSpace(titles[i])
			if s == "" && t == "" {
				continue
			}
			entry := TimetableEntryReq{StartTime: s, Title: t}
			if i < len(ends) {
				entry.EndTime = strings.TrimSpace(ends[i])
			}
			if i < len(descs) {
				entry.Description = strings.TrimSpace(descs[i])
			}
			if i < len(rooms) {
				entry.Room = strings.TrimSpace(rooms[i])
			}
			if i < len(ttTypes) {
				if v := strings.TrimSpace(ttTypes[i]); v == "workshop" {
					entry.EntryType = "workshop"
				} else {
					entry.EntryType = "bal"
				}
			}
			if i < len(locIDs) {
				if v, err := strconv.Atoi(strings.TrimSpace(locIDs[i])); err == nil && v > 0 {
					entry.LocationID = &v
				}
			}
			if i < len(musIDs) {
				if v, err := strconv.Atoi(strings.TrimSpace(musIDs[i])); err == nil && v > 0 {
					entry.MusicianID = &v
				}
			}
			if entry.MusicianID == nil && i < len(musNames) {
				if name := strings.TrimSpace(musNames[i]); name != "" {
					if m, merr := client.CreateMusician(r.Context(), Musician{Bandname: name}, getSessionToken(r)); merr == nil {
						entry.MusicianID = &m.ID
					} else {
						log.Printf("create musician %q: %v", name, merr)
					}
				}
			}
			ttEntries = append(ttEntries, entry)
		}
		if len(ttEntries) > 0 {
			if terr := client.AddTimetableEntries(r.Context(), event.ID, ttEntries, getSessionToken(r)); terr != nil {
				log.Printf("add timetable error: %v", terr)
			}
		}

		switch intent {
		case "clone":
			http.Redirect(w, r, fmt.Sprintf("/admin/events/new?clone_from=%d", event.ID), http.StatusSeeOther)
		case "save-template":
			name := strings.TrimSpace(r.FormValue("tpl_name"))
			if name != "" {
				var tplOrgID *int
				if v := strings.TrimSpace(r.FormValue("tpl_org_id")); v != "" {
					if n, err2 := strconv.Atoi(v); err2 == nil && n > 0 {
						tplOrgID = &n
					}
				}
				if saved, gerr := client.GetEvent(r.Context(), event.ID); gerr == nil {
					if _, terr := saveEventAsTemplate(db, su.ID, tplOrgID, name, saved); terr != nil {
						log.Printf("save as template error: %v", terr)
					}
				}
			}
			http.Redirect(w, r, fmt.Sprintf("/admin/events/%d/edit", event.ID), http.StatusSeeOther)
		case "create-series":
			seriesURL := fmt.Sprintf("/admin/series/new?ids=%d&prefill_event_id=%d", event.ID, event.ID)
			if orgID != nil {
				seriesURL += fmt.Sprintf("&org_id=%d", *orgID)
			}
			http.Redirect(w, r, seriesURL, http.StatusSeeOther)
		default:
			http.Redirect(w, r, fmt.Sprintf("/admin/events/%d/edit", event.ID), http.StatusSeeOther)
		}
	}
}

func musiciansByID(ids []int, all []Musician) []Musician {
	var out []Musician
	for _, id := range ids {
		for _, m := range all {
			if m.ID == id {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

func instructorsByID(ids []int, all []Instructor) []Instructor {
	var out []Instructor
	for _, id := range ids {
		for _, in := range all {
			if in.ID == id {
				out = append(out, in)
				break
			}
		}
	}
	return out
}

func buildSelectedDanceNames(event Event) map[string]bool {
	m := make(map[string]bool, len(event.DanceNames))
	for _, n := range event.DanceNames {
		m[n] = true
	}
	return m
}

func buildSelectedDanceNamesFromIDs(ids []int, dances []Dance) map[string]bool {
	idSet := make(map[int]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	m := make(map[string]bool)
	for _, d := range dances {
		if idSet[d.ID] {
			m[d.Name] = true
		}
	}
	return m
}

// locationQuality returns a score for how complete a location is.
// Higher is better: coords are worth 2, a non-stub name is worth 1.
func locationQuality(loc *Location) int {
	if loc == nil {
		return 0
	}
	q := 0
	if loc.Latitude != nil {
		q += 2
	}
	if loc.Location != "" && !strings.Contains(loc.Location, "Unknown") {
		q += 1
	}
	return q
}

func loadDefaultDanceIDs(db *sql.DB) []int {
	raw := getSiteSetting(db, "default_dance_ids")
	if raw == "" {
		return nil
	}
	var ids []int
	json.Unmarshal([]byte(raw), &ids)
	return ids
}

func adminEventEditPageHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
		var event Event
		var eventErr error
		var bundle RefBundle
		var wg sync.WaitGroup
		wg.Add(2)
		tok := getSessionToken(r)
		go func() { defer wg.Done(); event, eventErr = client.GetEventAuthed(r.Context(), id, tok) }()
		go func() { defer wg.Done(); bundle = client.FetchRefBundle(r.Context()) }()
		wg.Wait()
		if eventErr != nil {
			http.NotFound(w, r)
			return
		}
		var userOrgs []Organization
		if su := getSessionUser(r); su != nil {
			if su.Role == "admin" {
				userOrgs = bundle.Orgs
			} else {
				orgIDSet := make(map[int]bool)
				for _, oid := range getUserOrgIDs(r.Context(), client, su.ID, getSessionToken(r)) {
					orgIDSet[oid] = true
				}
				for _, o := range bundle.Orgs {
					if orgIDSet[o.ID] {
						userOrgs = append(userOrgs, o)
					}
				}
			}
		}
		locOrgFirst, locOthers := splitEventLocations(bundle.Locations, event)
		var editTemplates []EventTemplate
		if su := getSessionUser(r); su != nil {
			tok := getSessionToken(r)
			var orgIDs []int
			if su.Role == "admin" {
				for _, o := range bundle.Orgs {
					orgIDs = append(orgIDs, o.ID)
				}
			} else {
				orgIDs = getUserOrgIDs(r.Context(), client, su.ID, tok)
			}
			editTemplates, _ = listTemplates(db, su.ID, orgIDs)
		}
		var eventOrg *Organization
		if event.OrganizationID != nil {
			for i := range bundle.Orgs {
				if bundle.Orgs[i].ID == *event.OrganizationID {
					eventOrg = &bundle.Orgs[i]
					break
				}
			}
		}
		seriesList, _ := client.GetSeriesList(r.Context(), tok)
		var currentSeries *EventSeries
		if event.SeriesID != nil {
			for i := range seriesList {
				if seriesList[i].ID == *event.SeriesID {
					currentSeries = &seriesList[i]
					break
				}
			}
			// Series may belong to a different org than the user has access to;
			// fetch it directly if not found in the accessible list.
			if currentSeries == nil {
				s, err := client.GetSeriesByID(r.Context(), *event.SeriesID, tok)
				if err == nil {
					currentSeries = &s
				}
			}
		}
		title := i18n.T(r, "admin_event_edit_title")
		renderTemplate(w, tmpls.adminEventForm, tmplData(r, cfg, i18n, title, AdminEventFormData{
			IsNew:              false,
			Event:              event,
			Org:                eventOrg,
			Organizations:      bundle.Orgs,
			Locations:          bundle.Locations,
			LocOrgFirst:        locOrgFirst,
			LocOthers:          locOthers,
			Musicians:          bundle.Musicians,
			Instructors:        bundle.Instructors,
			Dances:             bundle.Dances,
			SelectedDanceNames: buildSelectedDanceNames(event),
			UserOrgs:           userOrgs,
			Templates:          editTemplates,
			Series:             seriesList,
			CurrentSeries:      currentSeries,
			CanDelete:          event.Deletable,
			TimetableError:     r.URL.Query().Get("tt_error"),
		}))
	}
}

func adminEventSaveHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		bundle := client.FetchRefBundle(r.Context())
		saveTok := getSessionToken(r)
		intent := r.FormValue("intent")
		renderErr := func(errKey string) {
			event, _ := client.GetEventAuthed(r.Context(), id, saveTok)
			locOrgFirst, locOthers := splitEventLocations(bundle.Locations, event)
			var evtOrg *Organization
			if event.OrganizationID != nil {
				for i := range bundle.Orgs {
					if bundle.Orgs[i].ID == *event.OrganizationID {
						evtOrg = &bundle.Orgs[i]
						break
					}
				}
			}
			title := i18n.T(r, "admin_event_edit_title")
			renderTemplate(w, tmpls.adminEventForm, tmplData(r, cfg, i18n, title, AdminEventFormData{
				IsNew:              false,
				Event:              event,
				Org:                evtOrg,
				Organizations:      bundle.Orgs,
				Locations:          bundle.Locations,
				LocOrgFirst:        locOrgFirst,
				LocOthers:          locOthers,
				Musicians:          bundle.Musicians,
				Instructors:        bundle.Instructors,
				Dances:             bundle.Dances,
				SelectedDanceNames: buildSelectedDanceNames(event),
				ErrorKey:           errKey,
			}))
		}

		date := r.FormValue("date")
		endDate := r.FormValue("end_date")
		if endDate == "" {
			endDate = date
		}
		startT := r.FormValue("start_time")
		endT := r.FormValue("end_time")
		startTime, endTime := "", ""
		if date != "" && startT != "" {
			startTime = date + "T" + startT + ":00"
		}
		if endDate != "" && endT != "" {
			endTime = endDate + "T" + endT + ":00"
		}

		var orgID *int
		switch r.FormValue("org_choice") {
		case "existing":
			if v := r.FormValue("org_id"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					orgID = &n
				}
			}
		case "new":
			newOrg := Organization{Name: strings.TrimSpace(r.FormValue("new_org_name"))}
			if newOrg.Name != "" {
				created, err := client.CreateOrganization(r.Context(), newOrg, getSessionToken(r))
				if err != nil {
					renderErr("admin_save_error")
					return
				}
				orgID = &created.ID
			}
		}

		var locReq EventLocReq
		var selectedLocID int
		switch r.FormValue("loc_choice") {
		case "existing":
			if v := r.FormValue("loc_id"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					for _, l := range bundle.Locations {
						if l.ID == n {
							selectedLocID = l.ID
							locReq = EventLocReq{
								Location:  l.Location,
								Address:   l.Address,
								Town:      l.Town,
								Country:   l.Country,
								Latitude:  l.Latitude,
								Longitude: l.Longitude,
							}
							break
						}
					}
				}
			}
		case "new":
			locReq = EventLocReq{
				Location:    strings.TrimSpace(r.FormValue("new_loc_name")),
				ShortName:   strings.TrimSpace(r.FormValue("new_loc_short_name")),
				Address:     strings.TrimSpace(r.FormValue("new_loc_address")),
				Zipcode:     strings.TrimSpace(r.FormValue("new_loc_zip")),
				Town:        strings.TrimSpace(r.FormValue("new_loc_town")),
				Country:     strings.TrimSpace(r.FormValue("new_loc_country")),
				CountryCode: strings.TrimSpace(r.FormValue("new_loc_country_code")),
				Region:      strings.TrimSpace(r.FormValue("new_loc_region")),
				Latitude:    parseLatLng(r.FormValue("new_loc_lat")),
				Longitude:   parseLatLng(r.FormValue("new_loc_lng")),
			}
		}

		var pricing *Pricing
		if pt := r.FormValue("pricing_type"); pt != "" && pt != "none" {
			p := &Pricing{Type: pt}
			switch pt {
			case "single":
				if amt := r.FormValue("pricing_amount"); amt != "" {
					if f, err := strconv.ParseFloat(amt, 64); err == nil {
						p.Amount = f
					}
				}
				p.Currency = strings.TrimSpace(r.FormValue("pricing_currency"))
			case "multiple":
				labels := r.MultipartForm.Value["pl_label"]
				amounts := r.MultipartForm.Value["pl_amount"]
				for i, lbl := range labels {
					lbl = strings.TrimSpace(lbl)
					if lbl == "" {
						continue
					}
					var amt float64
					if i < len(amounts) {
						if f, err := strconv.ParseFloat(strings.TrimSpace(amounts[i]), 64); err == nil {
							amt = f
						}
					}
					p.Prices = append(p.Prices, Price{Label: lbl, Amount: amt})
				}
				if len(p.Prices) == 0 {
					p = nil
				}
			}
			pricing = p
		}

		tags := r.MultipartForm.Value["tags"]

		var musicianIDs []int
		for _, v := range r.MultipartForm.Value["musician_ids"] {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				musicianIDs = append(musicianIDs, n)
			}
		}
		var instructorIDs []int
		for _, v := range r.MultipartForm.Value["instructor_ids"] {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				instructorIDs = append(instructorIDs, n)
			}
		}
		var danceIDs []int
		for _, v := range r.MultipartForm.Value["dance_ids"] {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				danceIDs = append(danceIDs, n)
			}
		}

		var saveRoomID *int
		if v := r.FormValue("room_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				saveRoomID = &n
			}
		}
		ticketsTotal, _ := strconv.Atoi(r.FormValue("tickets_total"))
		req := EventUpdateReq{
			Title:          strings.TrimSpace(r.FormValue("title")),
			Description:    strings.TrimSpace(r.FormValue("description")),
			StartTime:      startTime,
			EndTime:        endTime,
			HasBall:        sliceContains(tags, "bal-folk"),
			HasWorkshop:    sliceContains(tags, "dance-workshop") || sliceContains(tags, "musician-workshop"),
			HasFestival:    sliceContains(tags, "festival"),
			BookingURL:     strings.TrimSpace(r.FormValue("booking_url")),
			Food:           r.FormValue("food"),
			Drink:          r.FormValue("drink"),
			FloorCondition: r.FormValue("floor_condition"),
			Attributes:     eventAttrsFromForm(r),
			ContactName:    strings.TrimSpace(r.FormValue("contact_name")),
			ContactEmail:   strings.TrimSpace(r.FormValue("contact_email")),
			IsCancelled:    r.FormValue("is_cancelled") == "on",
			Availability:   r.FormValue("availability"),
			TicketsTotal:   ticketsTotal,
			BookingEnabled: r.FormValue("booking_enabled") == "on",
			IsPublished:    r.FormValue("is_published") == "on",
			Tags:           tags,
			URL:            strings.TrimSpace(r.FormValue("url")),
			OrganizationID: orgID,
			Pricing:        pricing,
			Location:       locReq,
			Musicians:      musicianIDs,
			Instructors:    instructorIDs,
			Dances:         danceIDs,
			RoomID:         saveRoomID,
		}

		// Apply template overrides if submitted (suggestion acceptance flow).
		var tplOverride *templateEventData
		var tplFieldsSet map[string]bool
		if tplIDStr := r.FormValue("tpl_id"); tplIDStr != "" {
			if tplID, err2 := strconv.Atoi(tplIDStr); err2 == nil && tplID > 0 {
				if tpl, err2 := getTemplate(db, tplID); err2 == nil {
					var td templateEventData
					if err2 := json.Unmarshal([]byte(tpl.Data), &td); err2 == nil {
						tplOverride = &td
						tplFieldsSet = make(map[string]bool)
						for _, f := range r.MultipartForm.Value["tpl_fields"] {
							tplFieldsSet[f] = true
						}
					}
				}
			}
		}
		if tplOverride != nil {
			applyTemplateFields(&req, tplOverride, tplFieldsSet, bundle.Locations)
		}

		renderErrFull := func(errKey string) {
			event, _ := client.GetEventAuthed(r.Context(), id, saveTok)
			event.Title = req.Title
			event.Description = req.Description
			event.StartTime = req.StartTime
			event.EndTime = req.EndTime
			event.HasBall = req.HasBall
			event.HasWorkshop = req.HasWorkshop
			event.HasFestival = req.HasFestival
			event.BookingURL = req.BookingURL
			event.Food = req.Food
			event.Drink = req.Drink
			event.FloorCondition = req.FloorCondition
			event.Attributes = req.Attributes
			event.ContactName = req.ContactName
			event.ContactEmail = req.ContactEmail
			event.IsCancelled = req.IsCancelled
			event.Availability = req.Availability
			event.TicketsTotal = req.TicketsTotal
			event.BookingEnabled = req.BookingEnabled
			event.IsPublished = req.IsPublished
			event.Tags = req.Tags
			event.URL = req.URL
			event.OrganizationID = req.OrganizationID
			event.Pricing = req.Pricing
			event.Musicians = musiciansByID(musicianIDs, bundle.Musicians)
			event.Instructors = instructorsByID(instructorIDs, bundle.Instructors)
			if selectedLocID > 0 {
				event.LocationID = &selectedLocID
			}
			locOrgFirst, locOthers := splitEventLocations(bundle.Locations, event)
			var evtOrg *Organization
			if event.OrganizationID != nil {
				for i := range bundle.Orgs {
					if bundle.Orgs[i].ID == *event.OrganizationID {
						evtOrg = &bundle.Orgs[i]
						break
					}
				}
			}
			var userOrgs []Organization
			if su.Role == "admin" {
				userOrgs = bundle.Orgs
			} else {
				orgIDSet := make(map[int]bool)
				for _, oid := range getUserOrgIDs(r.Context(), client, su.ID, saveTok) {
					orgIDSet[oid] = true
				}
				for _, o := range bundle.Orgs {
					if orgIDSet[o.ID] {
						userOrgs = append(userOrgs, o)
					}
				}
			}
			title := i18n.T(r, "admin_event_edit_title")
			renderTemplate(w, tmpls.adminEventForm, tmplData(r, cfg, i18n, title, AdminEventFormData{
				IsNew:              false,
				Event:              event,
				Org:                evtOrg,
				Organizations:      bundle.Orgs,
				Locations:          bundle.Locations,
				LocOrgFirst:        locOrgFirst,
				LocOthers:          locOthers,
				Musicians:          bundle.Musicians,
				Instructors:        bundle.Instructors,
				Dances:             bundle.Dances,
				SelectedDanceNames: buildSelectedDanceNamesFromIDs(danceIDs, bundle.Dances),
				UserOrgs:           userOrgs,
				ErrorKey:           errKey,
			}))
		}

		if req.Title == "" {
			renderErrFull("evt_title_required")
			return
		}

		if _, err := client.UpdateEvent(r.Context(), id, req, saveTok); err != nil {
			log.Printf("update event error: %v", err)
			renderErrFull("admin_save_error")
			return
		}

		if file, header, ferr := r.FormFile("image"); ferr == nil {
			defer file.Close()
			data, rerr := io.ReadAll(file)
			if rerr == nil {
				if uerr := client.UploadEventImage(r.Context(), id, data, header.Filename, getSessionToken(r)); uerr != nil {
					log.Printf("upload image error: %v", uerr)
				}
			}
		}

		var ttEntries []TimetableEntryReq
		if tplOverride != nil && tplFieldsSet["timetable"] && len(tplOverride.Timetable) > 0 {
			for _, e := range tplOverride.Timetable {
				ttEntries = append(ttEntries, TimetableEntryReq{
					StartTime:   e.StartTime,
					EndTime:     e.EndTime,
					Title:       e.Title,
					Description: e.Description,
					Room:        e.Room,
					LocationID:  e.LocationID,
					MusicianID:  e.MusicianID,
				})
			}
		} else {
			starts := r.MultipartForm.Value["tt_start"]
			ends := r.MultipartForm.Value["tt_end"]
			titles := r.MultipartForm.Value["tt_title"]
			descs := r.MultipartForm.Value["tt_desc"]
			rooms := r.MultipartForm.Value["tt_room"]
			ttTypes := r.MultipartForm.Value["tt_type"]
			locIDs := r.MultipartForm.Value["tt_loc_id"]
			musIDs := r.MultipartForm.Value["tt_musician_id"]
			musNames := r.MultipartForm.Value["tt_musician_name"]
			for i, s := range starts {
				s = strings.TrimSpace(s)
				if i >= len(titles) {
					break
				}
				t := strings.TrimSpace(titles[i])
				if s == "" && t == "" {
					continue
				}
				entry := TimetableEntryReq{StartTime: s, Title: t}
				if i < len(ends) {
					entry.EndTime = strings.TrimSpace(ends[i])
				}
				if i < len(descs) {
					entry.Description = strings.TrimSpace(descs[i])
				}
				if i < len(rooms) {
					entry.Room = strings.TrimSpace(rooms[i])
				}
				if i < len(ttTypes) {
					if v := strings.TrimSpace(ttTypes[i]); v == "workshop" {
						entry.EntryType = "workshop"
					} else {
						entry.EntryType = "bal"
					}
				}
				if i < len(locIDs) {
					if v, err := strconv.Atoi(strings.TrimSpace(locIDs[i])); err == nil && v > 0 {
						entry.LocationID = &v
					}
				}
				if i < len(musIDs) {
					if v, err := strconv.Atoi(strings.TrimSpace(musIDs[i])); err == nil && v > 0 {
						entry.MusicianID = &v
					}
				}
				if entry.MusicianID == nil && i < len(musNames) {
					if name := strings.TrimSpace(musNames[i]); name != "" {
						if m, merr := client.CreateMusician(r.Context(), Musician{Bandname: name}, getSessionToken(r)); merr == nil {
							entry.MusicianID = &m.ID
						} else {
							log.Printf("create musician %q: %v", name, merr)
						}
					}
				}
				ttEntries = append(ttEntries, entry)
			}
		}
		// Only replace the timetable when the form signals it was touched.
		// Skipping when untouched prevents a parsing failure or premature
		// form submission from silently wiping existing entries.
		var ttError string
		if r.FormValue("timetable_edited") == "1" {
			if ttEntries == nil {
				ttEntries = []TimetableEntryReq{}
			}
			if err := client.ReplaceTimetable(r.Context(), id, ttEntries, getSessionToken(r)); err != nil {
				log.Printf("replace timetable error: %v", err)
				ttError = err.Error()
			}
		}

		if req.IsPublished {
			go deliverUpdateToFollowers(cfg, db, client, id)
		}

		// editRedirect appends a tt_error query param (surfacing a timetable
		// save failure — see #808 follow-up) to the standard edit-page
		// redirect target, when present.
		editRedirect := fmt.Sprintf("/admin/events/%d/edit", id)
		if ttError != "" {
			editRedirect += "?tt_error=" + url.QueryEscape(ttError)
		}

		switch intent {
		case "clone":
			http.Redirect(w, r, fmt.Sprintf("/admin/events/new?clone_from=%d", id), http.StatusSeeOther)
		case "save-template":
			name := strings.TrimSpace(r.FormValue("tpl_name"))
			if name != "" {
				var tplOrgID *int
				if v := strings.TrimSpace(r.FormValue("tpl_org_id")); v != "" {
					if n, err2 := strconv.Atoi(v); err2 == nil && n > 0 {
						tplOrgID = &n
					}
				}
				if saved, gerr := client.GetEvent(r.Context(), id); gerr == nil {
					if _, terr := saveEventAsTemplate(db, su.ID, tplOrgID, name, saved); terr != nil {
						log.Printf("save as template error: %v", terr)
					}
				}
			}
			http.Redirect(w, r, editRedirect, http.StatusSeeOther)
		case "assign-series":
			if seriesID, serr := strconv.Atoi(r.FormValue("series_id")); serr == nil && seriesID > 0 {
				_ = client.AssignEventsToSeries(r.Context(), seriesID, []int{id}, getSessionToken(r))
			}
			http.Redirect(w, r, editRedirect, http.StatusSeeOther)
		case "remove-series":
			_ = client.RemoveEventFromSeries(r.Context(), id, getSessionToken(r))
			http.Redirect(w, r, editRedirect, http.StatusSeeOther)
		case "create-series":
			seriesURL := fmt.Sprintf("/admin/series/new?ids=%d&prefill_event_id=%d", id, id)
			if req.OrganizationID != nil {
				seriesURL += fmt.Sprintf("&org_id=%d", *req.OrganizationID)
			}
			http.Redirect(w, r, seriesURL, http.StatusSeeOther)
		default:
			http.Redirect(w, r, editRedirect, http.StatusSeeOther)
		}
	}
}

// ── Admin org dashboard (/admin/organization/{slug}) ─────────────────────────

type AdminOrgDashboardData struct {
	Org                 Organization
	Slug                string
	Events              []Event
	OrgMap              map[int]string
	Locations           []Location
	Dances              []Dance
	AllTags             []Tag
	Series              []EventSeries
	FilterIncludePast   bool
	FilterType          string
	FilterDance         string
	FilterLocationIDs   []int
	FilterLocationIDSet map[int]bool
	FilterLocationNames []string
	TotalCount          int
	IsMember            bool   // admin or member of this org
	NewEventOrgID       int    // org to pre-assign "new event" to
	NewEventOrgName     string // org name shown on the button when not a member
	OrgLocations        []Location
	LocEventCounts      map[int]int
	OrgTemplates        []EventTemplate
	OrgSeries           []EventSeries
}

func adminOrgDashboardHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		slug := r.PathValue("slug")

		orgs, err := client.GetOrganizations(r.Context())
		if err != nil {
			http.Error(w, "could not load organizations", http.StatusBadGateway)
			return
		}

		var org Organization
		var found bool
		for _, o := range orgs {
			if effectiveSlug(o) == slug {
				org = o
				found = true
				break
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}

		token := getSessionToken(r)

		orgMap := make(map[int]string, len(orgs))
		for _, o := range orgs {
			orgMap[o.ID] = o.Name
		}

		// Membership and new-event org determination.
		var isMember bool
		var newEventOrgID int
		var newEventOrgName string

		if su.Role == "admin" {
			isMember = true
			newEventOrgID = org.ID
			newEventOrgName = org.Name
		} else {
			userOrgIDs := getUserOrgIDs(r.Context(), client, su.ID, token)
			for _, oid := range userOrgIDs {
				if oid == org.ID {
					isMember = true
					break
				}
			}
			if isMember {
				newEventOrgID = org.ID
				newEventOrgName = org.Name
			} else if len(userOrgIDs) > 0 {
				newEventOrgID = userOrgIDs[0]
				newEventOrgName = orgMap[newEventOrgID]
			}
		}

		includePast := r.URL.Query().Get("include_past") == "1"
		filterType := r.URL.Query().Get("type")
		filterDance := r.URL.Query().Get("dance")
		var filterLocationIDs []int
		for _, v := range r.URL.Query()["location_id"] {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				filterLocationIDs = append(filterLocationIDs, n)
			}
		}
		filterLocationIDSet := make(map[int]bool, len(filterLocationIDs))
		for _, id := range filterLocationIDs {
			filterLocationIDSet[id] = true
		}

		baseParams := url.Values{}
		baseParams.Set("organization_id", strconv.Itoa(org.ID))
		baseParams.Set("limit", "1000")
		if includePast {
			baseParams.Set("include_past", "true")
		}

		// Fetched without the location filter so per-location event counts in
		// the "assigned locations" section stay stable regardless of which
		// location(s) the bottom list is currently filtered to.
		allOrgEvents, _, err := client.GetAdminEventsWithTotal(r.Context(), token, baseParams)
		if err != nil {
			http.Error(w, "could not load events", http.StatusBadGateway)
			return
		}

		params := url.Values{}
		for k, v := range baseParams {
			params[k] = v
		}
		if len(filterLocationIDs) > 0 {
			ids := make([]string, len(filterLocationIDs))
			for i, id := range filterLocationIDs {
				ids[i] = strconv.Itoa(id)
			}
			params.Set("location_id", strings.Join(ids, ","))
		}
		events, total := allOrgEvents, len(allOrgEvents)
		if len(filterLocationIDs) > 0 {
			events, total, err = client.GetAdminEventsWithTotal(r.Context(), token, params)
			if err != nil {
				http.Error(w, "could not load events", http.StatusBadGateway)
				return
			}
		}

		// type/dance are narrowed in-memory (mirroring adminEventsHandler,
		// which has no matching API params for these either) since the API's
		// applyEventFilters already covers the server-safe filters above.
		if filterType != "" {
			filtered := events[:0]
			for _, e := range events {
				switch filterType {
				case "ball":
					if e.HasBall {
						filtered = append(filtered, e)
					}
				case "workshop":
					if e.HasWorkshop {
						filtered = append(filtered, e)
					}
				case "festival":
					if e.HasFestival {
						filtered = append(filtered, e)
					}
				}
			}
			events = filtered
		}
		if filterDance != "" {
			filtered := events[:0]
			for _, e := range events {
				for _, d := range e.DanceNames {
					if d == filterDance {
						filtered = append(filtered, e)
						break
					}
				}
			}
			events = filtered
		}

		locs, _ := client.GetLocations(r.Context())
		dances, _ := client.GetDances(r.Context())
		allTags, _ := client.GetTags(r.Context())
		series, _ := client.GetSeriesList(r.Context(), token)

		locEventCounts := make(map[int]int, len(locs))
		for _, ev := range allOrgEvents {
			if ev.LocationID != nil {
				locEventCounts[*ev.LocationID]++
			}
		}
		var orgLocations []Location
		for _, l := range locs {
			for _, oid := range l.OrganizationIDs {
				if oid == org.ID {
					orgLocations = append(orgLocations, l)
					break
				}
			}
		}
		var filterLocationNames []string
		for _, l := range orgLocations {
			if filterLocationIDSet[l.ID] {
				filterLocationNames = append(filterLocationNames, locationDisplayName(l))
			}
		}

		var orgTemplates []EventTemplate
		if tmpls2, err := listTemplatesForOrg(db, org.ID); err == nil {
			orgTemplates = tmpls2
		}
		var orgSeries []EventSeries
		for _, s := range series {
			if s.OrganizationID != nil && *s.OrganizationID == org.ID {
				orgSeries = append(orgSeries, s)
			}
		}

		renderTemplate(w, tmpls.adminOrgDashboard, tmplData(r, cfg, i18n, org.Name, AdminOrgDashboardData{
			Org:                  org,
			Slug:                 slug,
			Events:               events,
			OrgMap:               orgMap,
			Locations:            locs,
			Dances:               dances,
			AllTags:              allTags,
			Series:               series,
			FilterIncludePast:    includePast,
			FilterType:           filterType,
			FilterDance:          filterDance,
			FilterLocationIDs:    filterLocationIDs,
			FilterLocationIDSet:  filterLocationIDSet,
			FilterLocationNames:  filterLocationNames,
			TotalCount:           total,
			IsMember:             isMember,
			NewEventOrgID:        newEventOrgID,
			NewEventOrgName:      newEventOrgName,
			OrgLocations:         orgLocations,
			LocEventCounts:       locEventCounts,
			OrgTemplates:         orgTemplates,
			OrgSeries:            orgSeries,
		}))
	}
}

// ── Admin location dashboard (/admin/location/{id}) ──────────────────────────

type AdminLocationDashboardData struct {
	Location          Location
	Events            []Event
	OrgMap            map[int]string
	Locations         []Location
	Dances            []Dance
	AllTags           []Tag
	Series            []EventSeries
	FilterIncludePast bool
	TotalCount        int
	NewEventOrgID     int    // org to pre-assign "new event" to
	NewEventOrgName   string // shown on the button
}

func adminLocationDashboardHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
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

		loc, err := client.GetLocation(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		token := getSessionToken(r)

		orgs, _ := client.GetOrganizations(r.Context())
		orgMap := make(map[int]string, len(orgs))
		for _, o := range orgs {
			orgMap[o.ID] = o.Name
		}

		// Determine which org to pre-assign for new events.
		var newEventOrgID int
		var newEventOrgName string

		if su.Role == "admin" {
			if len(loc.OrganizationIDs) > 0 {
				newEventOrgID = loc.OrganizationIDs[0]
				newEventOrgName = orgMap[newEventOrgID]
			}
		} else {
			userOrgIDs := getUserOrgIDs(r.Context(), client, su.ID, token)
			locOrgSet := make(map[int]bool, len(loc.OrganizationIDs))
			for _, oid := range loc.OrganizationIDs {
				locOrgSet[oid] = true
			}
			for _, oid := range userOrgIDs {
				if locOrgSet[oid] {
					newEventOrgID = oid
					newEventOrgName = orgMap[oid]
					break
				}
			}
			if newEventOrgID == 0 && len(userOrgIDs) > 0 {
				newEventOrgID = userOrgIDs[0]
				newEventOrgName = orgMap[newEventOrgID]
			}
		}

		includePast := r.URL.Query().Get("include_past") == "1"
		params := url.Values{}
		params.Set("location_id", strconv.Itoa(id))
		params.Set("limit", "1000")
		if includePast {
			params.Set("include_past", "true")
		}

		events, total, err := client.GetAdminEventsWithTotal(r.Context(), token, params)
		if err != nil {
			http.Error(w, "could not load events", http.StatusBadGateway)
			return
		}

		locs, _ := client.GetLocations(r.Context())
		dances, _ := client.GetDances(r.Context())
		allTags, _ := client.GetTags(r.Context())
		series, _ := client.GetSeriesList(r.Context(), token)

		locTitle := loc.ShortName
		if locTitle == "" {
			locTitle = loc.Location
		}

		renderTemplate(w, tmpls.adminLocationDashboard, tmplData(r, cfg, i18n, locTitle, AdminLocationDashboardData{
			Location:          loc,
			Events:            events,
			OrgMap:            orgMap,
			Locations:         locs,
			Dances:            dances,
			AllTags:           allTags,
			Series:            series,
			FilterIncludePast: includePast,
			TotalCount:        total,
			NewEventOrgID:     newEventOrgID,
			NewEventOrgName:   newEventOrgName,
		}))
	}
}

// POST /admin/api/instructor/quick-create — inline instructor creation from event form.
// Normalises the requested name, returns an existing instructor on a name match, or creates a new one.
func adminInstructorQuickCreateHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
			http.Error(w, `{"error":"invalid"}`, http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(req.Name)
		normName := normalizeForMatch(name)

		instructors, _ := client.GetInstructors(r.Context())
		for _, inst := range instructors {
			if normalizeForMatch(inst.Name) == normName {
				json.NewEncoder(w).Encode(map[string]any{"id": inst.ID, "name": inst.Name, "existing": true})
				return
			}
		}

		inst, err := client.CreateInstructor(r.Context(), Instructor{Name: name}, getSessionToken(r))
		if err != nil {
			http.Error(w, `{"error":"create failed"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": inst.ID, "name": inst.Name, "existing": false})
	}
}

// POST /admin/api/musician/quick-create — inline musician creation from event form.
// Normalises the requested bandname, returns an existing musician on a name match
// (using the same normalizeForMatch logic as enrichment), or creates a new one.
func adminMusicianQuickCreateHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Bandname string `json:"bandname"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Bandname) == "" {
			http.Error(w, `{"error":"invalid"}`, http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(req.Bandname)
		normName := normalizeForMatch(name)

		musicians, _ := client.GetMusicians(r.Context())
		for _, m := range musicians {
			if normalizeForMatch(m.Bandname) == normName {
				json.NewEncoder(w).Encode(map[string]any{"id": m.ID, "bandname": m.Bandname, "existing": true})
				return
			}
		}

		m, err := client.CreateMusician(r.Context(), Musician{Bandname: name}, getSessionToken(r))
		if err != nil {
			http.Error(w, `{"error":"create failed"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": m.ID, "bandname": m.Bandname, "existing": false})
	}
}
