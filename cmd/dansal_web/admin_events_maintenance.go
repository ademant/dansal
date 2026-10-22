package main

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
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
	FilterHasSource    bool
	// #1584: multi-select musician and instructor filters, and the CSV export
	// link that carries the same filters.
	MusicianSelected   map[int]bool
	InstructorSelected map[int]bool
	ExportURL          string
	ReturnURL          string
	// paging
	TotalCount   int
	Page         int
	TotalPages   int
	HasFilter    bool
	PagerBaseURL string
}

// maintenanceFilter is the maintenance view's filter set (#1584). The page and
// the CSV export both build their API query from it, so an export always holds
// exactly the events the page shows.
type maintenanceFilter struct {
	IncludePast   bool
	Unpublished   bool
	OrgID         int
	LocationID    int
	City          string
	DateFrom      string
	DateTo        string
	HasSource     bool
	MusicianIDs   []int
	InstructorIDs []int
}

func parseMaintenanceFilter(q url.Values) maintenanceFilter {
	f := maintenanceFilter{
		IncludePast:   q.Get("include_past") == "1",
		Unpublished:   q.Get("unpublished") == "1",
		City:          q.Get("city"),
		DateFrom:      q.Get("date_from"),
		DateTo:        q.Get("date_to"),
		HasSource:     q.Get("has_source") == "1",
		MusicianIDs:   positiveIDs(q["musician_id"]),
		InstructorIDs: positiveIDs(q["instructor_id"]),
	}
	f.OrgID, _ = strconv.Atoi(q.Get("org_id"))
	f.LocationID, _ = strconv.Atoi(q.Get("location_id"))
	return f
}

// active reports whether any filter narrows the list.
func (f maintenanceFilter) active() bool {
	return f.IncludePast || f.Unpublished || f.OrgID != 0 || f.LocationID != 0 || f.City != "" ||
		f.DateFrom != "" || f.DateTo != "" || f.HasSource || len(f.MusicianIDs) > 0 || len(f.InstructorIDs) > 0
}

// apiParams is the event-list query for the filter, without paging. The
// unpublished view's second query (email_verified, #982) is added by
// fetchMaintenanceEvents.
func (f maintenanceFilter) apiParams() url.Values {
	p := url.Values{}
	if f.IncludePast {
		p.Set("include_past", "true")
	}
	if f.Unpublished {
		p.Set("is_published", "false")
		if !f.IncludePast {
			p.Set("include_past", "true")
		}
	}
	if f.HasSource && !f.IncludePast {
		p.Set("include_past", "true")
	}
	if t, ok := parseISODate(f.DateFrom); ok {
		p.Set("start_time_after", strconv.FormatInt(t.Unix()-1, 10))
	}
	if t, ok := parseISODate(f.DateTo); ok {
		p.Set("start_time_before", strconv.FormatInt(t.Add(24*time.Hour).Unix(), 10))
	}
	if f.LocationID != 0 {
		p.Set("location_id", strconv.Itoa(f.LocationID))
	}
	if f.OrgID != 0 {
		p.Set("organization_id", strconv.Itoa(f.OrgID))
	}
	if f.City != "" {
		p.Set("town", f.City)
	}
	if f.HasSource {
		p.Set("has_source", "1")
	}
	for _, id := range f.MusicianIDs {
		p.Add("event_musician_id", strconv.Itoa(id))
	}
	for _, id := range f.InstructorIDs {
		p.Add("event_instructor_id", strconv.Itoa(id))
	}
	return p
}

// fetchMaintenanceEvents returns every event the filter selects, following the
// API's pages to the end. The unpublished view also includes events stuck at
// email_verified=0 (#982), which the is_published query alone misses.
func fetchMaintenanceEvents(ctx context.Context, client *DansalClient, token string, f maintenanceFilter) ([]Event, error) {
	queries := []url.Values{f.apiParams()}
	if f.Unpublished {
		ev := f.apiParams()
		ev.Del("is_published")
		ev.Set("email_verified", "false")
		queries = append(queries, ev)
	}
	seen := map[int]bool{}
	var out []Event
	for _, base := range queries {
		for offset := 0; ; {
			p := url.Values{}
			for k, v := range base {
				p[k] = v
			}
			p.Set("limit", strconv.Itoa(apiListLimit))
			p.Set("offset", strconv.Itoa(offset))
			page, total, err := client.GetAdminEventsWithTotal(ctx, token, p)
			if err != nil {
				return nil, err
			}
			for _, e := range page {
				if !seen[e.ID] {
					seen[e.ID] = true
					out = append(out, e)
				}
			}
			offset += len(page)
			if len(page) == 0 || offset >= total {
				break
			}
		}
	}
	return out, nil
}

// positiveIDs reads ids from repeated and/or comma-separated query values,
// dropping anything that is not a positive integer.
func positiveIDs(raw []string) []int {
	var out []int
	for _, v := range raw {
		for _, p := range strings.Split(v, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n > 0 {
				out = append(out, n)
			}
		}
	}
	return out
}

func adminEventsMaintenanceHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}

		q := r.URL.Query()
		f := parseMaintenanceFilter(q)

		page := 1
		if p, _ := strconv.Atoi(q.Get("page")); p > 1 {
			page = p
		}
		const pageSize = adminListPageSize

		token := getSessionToken(r)
		var events []Event
		var total, totalPages int
		if f.Unpublished {
			// The unpublished view merges two queries (#982), so it is listed
			// whole rather than paged.
			ev, err := fetchMaintenanceEvents(r.Context(), client, token, f)
			if err != nil {
				http.Error(w, "could not load events", http.StatusBadGateway)
				return
			}
			events, total, totalPages = ev, len(ev), 1
		} else {
			params := f.apiParams()
			params.Set("limit", strconv.Itoa(pageSize))
			params.Set("offset", strconv.Itoa((page-1)*pageSize))
			ev, t, err := client.GetAdminEventsWithTotal(r.Context(), token, params)
			if err != nil {
				http.Error(w, "could not load events", http.StatusBadGateway)
				return
			}
			events, total, totalPages = ev, t, 1
			if total > pageSize {
				totalPages = (total + pageSize - 1) / pageSize
			}
		}

		// Build pager base URL (all current filter params except page)
		pagerQ := url.Values{}
		for k, vs := range q {
			if k != "page" {
				pagerQ[k] = vs
			}
		}
		pagerBaseURL := "/admin/events/maintenance?" + pagerQ.Encode() + "&page="
		exportURL := "/admin/events/export.csv?" + pagerQ.Encode()

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
		if f.OrgID != 0 {
			filterOrgName = orgMap[f.OrgID]
		}
		var filterLocationName string
		if f.LocationID != 0 {
			for _, l := range locs {
				if l.ID == f.LocationID {
					filterLocationName = locationDisplayName(l)
					break
				}
			}
		}
		musicianSelected := make(map[int]bool, len(f.MusicianIDs))
		for _, id := range f.MusicianIDs {
			musicianSelected[id] = true
		}
		instructorSelected := make(map[int]bool, len(f.InstructorIDs))
		for _, id := range f.InstructorIDs {
			instructorSelected[id] = true
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
			FilterIncludePast:  f.IncludePast,
			FilterOrgID:        f.OrgID,
			FilterOrgName:      filterOrgName,
			FilterLocationID:   f.LocationID,
			FilterLocationName: filterLocationName,
			FilterCity:         f.City,
			FilterDateFrom:     f.DateFrom,
			FilterDateTo:       f.DateTo,
			FilterUnpublished:  f.Unpublished,
			FilterHasSource:    f.HasSource,
			MusicianSelected:   musicianSelected,
			InstructorSelected: instructorSelected,
			ExportURL:          exportURL,
			ReturnURL:          r.URL.RequestURI(),
			TotalCount:         total,
			Page:               page,
			TotalPages:         totalPages,
			HasFilter:          f.active(),
			PagerBaseURL:       pagerBaseURL,
		}))
	}
}
