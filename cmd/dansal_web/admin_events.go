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

// ── Events ────────────────────────────────────────────────────────────────────

type AdminEventsData struct {
	Events             []Event
	Organizations      []Organization
	Locations          []Location
	Musicians          []Musician
	Dances             []Dance
	AllTags            []Tag
	UserOrgs           []Organization
	Series             []EventSeries
	FilterIncludePast  bool
	FilterOrgID        int // -1 = no org assigned
	FilterLocationID   int
	FilterDateFrom     string
	FilterDateTo       string
	FilterMusicianID   int
	FilterType         string // "ball", "workshop", "festival"
	FilterDance        string
	FilterCreatedAfter string
	FilterSource       string
	FilterUnpublished  bool
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

type AdminEventNewData struct {
	Organizations      []Organization
	Locations          []Location
	Musicians          []Musician
	Dances             []Dance
	GroupedTags        []TagGroup
	SelectedDanceNames map[string]bool
	ErrorKey           string
	Prefill            *EventPrefill
	Templates          []EventTemplate
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
		ref := r.Header.Get("Referer")
		if ref == "" {
			ref = "/admin/events?unpublished=1"
		}
		http.Redirect(w, r, ref, http.StatusSeeOther)
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
		ref := r.Header.Get("Referer")
		if ref == "" {
			ref = fmt.Sprintf("/events/%d", id)
		}
		http.Redirect(w, r, ref, http.StatusSeeOther)
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
		ref := r.Header.Get("Referer")
		if ref == "" {
			ref = "/admin/events"
		}
		http.Redirect(w, r, ref, http.StatusSeeOther)
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
		ref := r.Header.Get("Referer")
		if ref == "" {
			ref = "/admin/events"
		}
		http.Redirect(w, r, ref, http.StatusSeeOther)
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
		ref := r.Header.Get("Referer")
		if ref == "" {
			ref = "/admin/events"
		}
		http.Redirect(w, r, ref, http.StatusSeeOther)
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
			ref := r.Header.Get("Referer")
			if ref == "" {
				ref = "/admin/events"
			}
			http.Redirect(w, r, ref, http.StatusSeeOther)
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
		_ = client.BulkSetEventAttributes(r.Context(), payload, getSessionToken(r))
		ref := r.Header.Get("Referer")
		if ref == "" {
			ref = "/admin/events"
		}
		http.Redirect(w, r, ref, http.StatusSeeOther)
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
		if len(ids) < 2 {
			http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
			return
		}
		ctx := r.Context()
		token := getSessionToken(r)

		ph := make([]string, len(ids))
		qargs := make([]interface{}, len(ids))
		for i, id := range ids {
			ph[i] = "?"
			qargs[i] = id
		}
		inClause := "(" + strings.Join(ph, ",") + ")"

		// Determine base from DB (newest user-edited, or newest by created_at).
		type evRow struct {
			id        int
			changedAt int64
			changedBy string
			createdAt string
		}
		evrows, err := db.QueryContext(ctx,
			"SELECT id, COALESCE(changed_at,0), COALESCE(changed_by,''), created_at FROM events WHERE id IN "+inClause,
			qargs...)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		var dbRows []evRow
		for evrows.Next() {
			var er evRow
			evrows.Scan(&er.id, &er.changedAt, &er.changedBy, &er.createdAt)
			dbRows = append(dbRows, er)
		}
		evrows.Close()
		if len(dbRows) < 2 {
			http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
			return
		}

		hasEdited := false
		for _, er := range dbRows {
			if er.changedBy != "" {
				hasEdited = true
				break
			}
		}
		baseRow := dbRows[0]
		for _, er := range dbRows[1:] {
			if hasEdited {
				baseEdited := baseRow.changedBy != ""
				erEdited := er.changedBy != ""
				if !baseEdited && erEdited {
					baseRow = er
					continue
				}
				if baseEdited && !erEdited {
					continue
				}
				if er.changedAt > baseRow.changedAt {
					baseRow = er
				}
			} else {
				if er.createdAt > baseRow.createdAt {
					baseRow = er
				}
			}
		}
		baseID := baseRow.id

		base, err := client.GetEventAuthed(ctx, baseID, token)
		if err != nil {
			http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
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
		http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
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
	orgs, _ := client.GetOrganizations(ctx)
	var ids []int
	for _, o := range orgs {
		members, err := client.GetOrganizationMembers(ctx, o.ID, token)
		if err != nil {
			continue
		}
		for _, m := range members {
			if m.UserID == userID {
				ids = append(ids, o.ID)
				break
			}
		}
	}
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
		createdAfter := q.Get("created_after")
		filterSource := q.Get("source")
		filterUnpublished := q.Get("unpublished") == "1"

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

		token := getSessionToken(r)
		events, err := client.GetAdminEvents(r.Context(), token, params)
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

		var userOrgs []Organization
		if su := getSessionUser(r); su != nil {
			if su.Role == "admin" {
				userOrgs = orgs
			} else {
				orgIDSet := make(map[int]bool)
				for _, oid := range getUserOrgIDs(r.Context(), client, su.ID, getSessionToken(r)) {
					orgIDSet[oid] = true
				}
				for _, o := range orgs {
					if orgIDSet[o.ID] {
						userOrgs = append(userOrgs, o)
					}
				}
			}
		}

		title := i18n.T(r, "admin_events_title")
		renderTemplate(w, tmpls.adminEvents, tmplData(r, cfg, i18n, title, AdminEventsData{
			Events:             events,
			Organizations:      orgs,
			Locations:          locs,
			Musicians:          musicians,
			Dances:             dances,
			AllTags:            allTags,
			UserOrgs:           userOrgs,
			Series:             series,
			FilterIncludePast:  includePast,
			FilterOrgID:        orgID,
			FilterLocationID:   locationID,
			FilterDateFrom:     dateFrom,
			FilterDateTo:       dateTo,
			FilterMusicianID:   musicianID,
			FilterType:         filterType,
			FilterDance:        filterDance,
			FilterCreatedAfter: createdAfter,
			FilterSource:       filterSource,
			FilterUnpublished:  filterUnpublished,
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

		tmpls2, _ := listTemplates(db, su.ID, getUserOrgs())

		title := i18n.T(r, "admin_event_new_title")
		renderTemplate(w, tmpls.adminEventNew, tmplData(r, cfg, i18n, title, AdminEventNewData{
			Organizations:      bundle.Orgs,
			Locations:          bundle.Locations,
			Musicians:          bundle.Musicians,
			Dances:             bundle.Dances,
			SelectedDanceNames: selected,
			Prefill:            prefill,
			Templates:          tmpls2,
		}))
	}
}

func adminEventCreateHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		bundle := client.FetchRefBundle(r.Context())
		renderErr := func(errKey string) {
			title := i18n.T(r, "admin_event_new_title")
			renderTemplate(w, tmpls.adminEventNew, tmplData(r, cfg, i18n, title, AdminEventNewData{
				Organizations: bundle.Orgs,
				Locations:     bundle.Locations,
				Musicians:     bundle.Musicians,
				Dances:        bundle.Dances,
				ErrorKey:      errKey,
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
		switch r.FormValue("loc_choice") {
		case "existing":
			if v := r.FormValue("loc_id"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					for _, l := range bundle.Locations {
						if l.ID == n {
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
				Location:  strings.TrimSpace(r.FormValue("new_loc_name")),
				ShortName: strings.TrimSpace(r.FormValue("new_loc_short_name")),
				Address:   strings.TrimSpace(r.FormValue("new_loc_address")),
				Zipcode:   strings.TrimSpace(r.FormValue("new_loc_zip")),
				Town:      strings.TrimSpace(r.FormValue("new_loc_town")),
				Country:   strings.TrimSpace(r.FormValue("new_loc_country")),
				Latitude:  parseLatLng(r.FormValue("new_loc_lat")),
				Longitude: parseLatLng(r.FormValue("new_loc_lng")),
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
			Dances:         danceIDs,
		}

		if req.Title == "" {
			renderErr("evt_title_required")
			return
		}

		event, err := client.CreateEvent(r.Context(), req, getSessionToken(r))
		if err != nil {
			log.Printf("create event error: %v", err)
			renderErr("admin_save_error")
			return
		}

		if createStandaloneLocation {
			newLoc := Location{
				Location:  locReq.Location,
				ShortName: locReq.ShortName,
				Address:   locReq.Address,
				Zipcode:   locReq.Zipcode,
				Town:      locReq.Town,
				Country:   locReq.Country,
				Latitude:  locReq.Latitude,
				Longitude: locReq.Longitude,
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

		http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
	}
}

type AdminEventEditData struct {
	Event              Event
	Org                *Organization
	Organizations      []Organization
	Locations          []Location // all locations (used for timetable rows)
	LocOrgFirst        []Location // location dropdown: same-org locations first
	LocOthers          []Location // location dropdown: remaining locations
	Musicians          []Musician
	Dances             []Dance
	GroupedTags        []TagGroup
	SelectedDanceNames map[string]bool
	ErrorKey           string
	UserOrgs           []Organization
	Templates          []EventTemplate
	Series             []EventSeries  // series the user can assign to
	CurrentSeries      *EventSeries   // set when event already belongs to a series
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
		renderTemplate(w, tmpls.adminEventEdit, tmplData(r, cfg, i18n, title, AdminEventEditData{
			Event:              event,
			Org:                eventOrg,
			Organizations:      bundle.Orgs,
			Locations:          bundle.Locations,
			LocOrgFirst:        locOrgFirst,
			LocOthers:          locOthers,
			Musicians:          bundle.Musicians,
			Dances:             bundle.Dances,
			SelectedDanceNames: buildSelectedDanceNames(event),
			UserOrgs:           userOrgs,
			Templates:          editTemplates,
			Series:             seriesList,
			CurrentSeries:      currentSeries,
		}))
	}
}

func adminEventSaveHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		bundle := client.FetchRefBundle(r.Context())
		saveTok := getSessionToken(r)
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
			renderTemplate(w, tmpls.adminEventEdit, tmplData(r, cfg, i18n, title, AdminEventEditData{
				Event:              event,
				Org:                evtOrg,
				Organizations:      bundle.Orgs,
				Locations:          bundle.Locations,
				LocOrgFirst:        locOrgFirst,
				LocOthers:          locOthers,
				Musicians:          bundle.Musicians,
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
		switch r.FormValue("loc_choice") {
		case "existing":
			if v := r.FormValue("loc_id"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					for _, l := range bundle.Locations {
						if l.ID == n {
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
				Location:  strings.TrimSpace(r.FormValue("new_loc_name")),
				ShortName: strings.TrimSpace(r.FormValue("new_loc_short_name")),
				Address:   strings.TrimSpace(r.FormValue("new_loc_address")),
				Zipcode:   strings.TrimSpace(r.FormValue("new_loc_zip")),
				Town:      strings.TrimSpace(r.FormValue("new_loc_town")),
				Country:   strings.TrimSpace(r.FormValue("new_loc_country")),
				Latitude:  parseLatLng(r.FormValue("new_loc_lat")),
				Longitude: parseLatLng(r.FormValue("new_loc_lng")),
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
		var danceIDs []int
		for _, v := range r.MultipartForm.Value["dance_ids"] {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				danceIDs = append(danceIDs, n)
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
			Dances:         danceIDs,
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

		if req.Title == "" {
			renderErr("evt_title_required")
			return
		}

		if _, err := client.UpdateEvent(r.Context(), id, req, getSessionToken(r)); err != nil {
			log.Printf("update event error: %v", err)
			renderErr("admin_save_error")
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
		if err := client.ReplaceTimetable(r.Context(), id, ttEntries, getSessionToken(r)); err != nil {
			log.Printf("replace timetable error: %v", err)
		}

		if req.IsPublished {
			go deliverUpdateToFollowers(cfg, db, client, id)
		}
		http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
	}
}
