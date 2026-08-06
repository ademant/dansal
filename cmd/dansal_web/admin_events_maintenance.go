package main

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

type AdminEventsMaintenanceData struct {
	Events             []Event
	OrgMap             map[int]string
	SeriesMap          map[int]string
	Organizations      []Organization
	Locations          []Location
	Musicians          []Musician
	Instructors        []Instructor
	Dances             []Dance
	FormatTags         []Tag
	Series             []EventSeries
	FilterIncludePast  bool
	FilterOrgID        int
	FilterOrgName      string
	FilterLocationID   int
	FilterLocationName string
	FilterCity         string
	FilterDateFrom     string
	FilterDateTo       string
	FilterUnpublished  bool
	ReturnURL          string
	// paging
	TotalCount   int
	Page         int
	TotalPages   int
	HasFilter    bool
	PagerBaseURL string
}

func adminEventsMaintenanceHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}

		q := r.URL.Query()
		includePast := q.Get("include_past") == "1"
		filterUnpublished := q.Get("unpublished") == "1"
		orgID, _ := strconv.Atoi(q.Get("org_id"))
		locationID, _ := strconv.Atoi(q.Get("location_id"))
		filterCity := q.Get("city")
		dateFrom := q.Get("date_from")
		dateTo := q.Get("date_to")

		hasFilter := includePast || filterUnpublished || orgID != 0 || locationID != 0 || filterCity != "" || dateFrom != "" || dateTo != ""

		page := 1
		if p, _ := strconv.Atoi(q.Get("page")); p > 1 {
			page = p
		}
		const pageSize = 100

		params := url.Values{}
		if includePast {
			params.Set("include_past", "true")
		}
		if filterUnpublished {
			params.Set("is_published", "false")
			if !includePast {
				params.Set("include_past", "true")
			}
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
		if hasFilter {
			params.Set("limit", "1000")
		} else {
			params.Set("limit", strconv.Itoa(pageSize))
			params.Set("offset", strconv.Itoa((page-1)*pageSize))
		}

		token := getSessionToken(r)
		events, total, err := client.GetAdminEventsWithTotal(r.Context(), token, params)
		if err != nil {
			http.Error(w, "could not load events", http.StatusBadGateway)
			return
		}

		if filterUnpublished {
			// #982: is_published=false alone misses events stuck at
			// email_verified=0 — those are invisible to the API's base guard
			// regardless of published status. Merge in a second request so the
			// "unpublished" shortcut doesn't silently skip them.
			evParams := url.Values{}
			for k, v := range params {
				evParams[k] = v
			}
			evParams.Del("is_published")
			evParams.Set("email_verified", "false")
			if evEvents, _, evErr := client.GetAdminEventsWithTotal(r.Context(), token, evParams); evErr == nil {
				seen := make(map[int]bool, len(events))
				for _, e := range events {
					seen[e.ID] = true
				}
				for _, e := range evEvents {
					if !seen[e.ID] {
						events = append(events, e)
						seen[e.ID] = true
						total++
					}
				}
			}
		}

		totalPages := 1
		if !hasFilter && total > pageSize {
			totalPages = (total + pageSize - 1) / pageSize
		}
		// Build pager base URL (all current filter params except page)
		pagerQ := url.Values{}
		for k, vs := range q {
			if k != "page" {
				pagerQ[k] = vs
			}
		}
		pagerBaseURL := "/admin/events/maintenance?" + pagerQ.Encode() + "&page="

		if orgID != 0 {
			filtered := events[:0]
			for _, e := range events {
				if e.OrganizationID != nil && *e.OrganizationID == orgID {
					filtered = append(filtered, e)
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

		orgs, _ := client.GetOrganizations(r.Context())
		locs, _ := client.GetLocations(r.Context())
		musicians, _ := client.GetMusicians(r.Context())
		instructors, _ := client.GetInstructors(r.Context())
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

		orgMap := make(map[int]string, len(orgs))
		for _, o := range orgs {
			orgMap[o.ID] = o.Name
		}
		seriesMap := make(map[int]string, len(series))
		for _, s := range series {
			seriesMap[s.ID] = s.Title
		}

		var formatTags []Tag
		for _, t := range allTags {
			if t.Category == "format" {
				formatTags = append(formatTags, t)
			}
		}

		var filterOrgName string
		if orgID != 0 {
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

		title := i18n.T(r, "admin_event_maintenance_title")
		renderTemplate(w, tmpls.adminEventsMaintenance, tmplData(r, cfg, i18n, title, AdminEventsMaintenanceData{
			Events:             events,
			OrgMap:             orgMap,
			SeriesMap:          seriesMap,
			Organizations:      orgs,
			Locations:          locs,
			Musicians:          musicians,
			Instructors:        instructors,
			Dances:             dances,
			FormatTags:         formatTags,
			Series:             series,
			FilterIncludePast:  includePast,
			FilterOrgID:        orgID,
			FilterOrgName:      filterOrgName,
			FilterLocationID:   locationID,
			FilterLocationName: filterLocationName,
			FilterCity:         filterCity,
			FilterDateFrom:     dateFrom,
			FilterDateTo:       dateTo,
			FilterUnpublished:  filterUnpublished,
			ReturnURL:          r.URL.RequestURI(),
			TotalCount:         total,
			Page:               page,
			TotalPages:         totalPages,
			HasFilter:          hasFilter,
			PagerBaseURL:       pagerBaseURL,
		}))
	}
}
