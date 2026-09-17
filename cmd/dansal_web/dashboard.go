package main

import (
	"database/sql"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
)

// DashboardData is the template data for /dashboard.
type DashboardData struct {
	Stats             MeStats
	UserOrgs          []Organization
	OrgMap            map[int]string
	OrgStats          map[int]OrgStatRecord
	Events            []Event
	PinnedTemplates   []EventTemplate
	UnpinnedTemplates []EventTemplate
	TemplateOrgMap    map[int]string
	Series            []EventSeries
	// OrgLocations/OrgFutureCounts/LocFutureCounts back the "Your orgs"
	// org -> locations tree (#1332): OrgLocations is the set of locations
	// assigned to each user org (mirrors adminOrgDashboardHandler's
	// OrgLocations); the two count maps are computed by grouping the
	// already-fetched future-only Events slice, so no extra event fetch
	// is needed.
	OrgLocations   map[int][]Location
	OrgFutureCount map[int]int
	LocFutureCount map[int]map[int]int
}

func dashboardHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		ctx := r.Context()

		var (
			userOrgIDs []int
			allOrgs    []Organization
			stats      MeStats
			orgStats   map[int]OrgStatRecord
			series     []EventSeries
			allLocs    []Location
		)

		fetchParallel(
			func() error {
				var err error
				userOrgIDs, err = client.GetUserOrganizationIDs(ctx, su.ID, token)
				if err != nil {
					log.Printf("dashboard: could not load user orgs: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				allOrgs, err = client.GetOrganizations(ctx)
				if err != nil {
					log.Printf("dashboard: could not load organizations: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				stats, err = client.GetMeStats(ctx, token)
				if err != nil {
					log.Printf("dashboard: could not load stats: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				orgStats, err = client.GetOrgStats(ctx)
				if err != nil {
					log.Printf("dashboard: could not load org stats: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				allLocs, err = client.GetLocations(ctx)
				if err != nil {
					log.Printf("dashboard: could not load locations: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				series, err = client.GetSeriesList(ctx, token)
				if err != nil {
					log.Printf("dashboard: could not load series: %v", err)
				}
				return nil
			},
		)

		orgSet := make(map[int]bool, len(userOrgIDs))
		for _, id := range userOrgIDs {
			orgSet[id] = true
		}
		var userOrgs []Organization
		for _, o := range allOrgs {
			if orgSet[o.ID] {
				userOrgs = append(userOrgs, o)
			}
		}

		var events []Event
		if len(userOrgIDs) > 0 {
			var evtWg sync.WaitGroup
			var evtMu sync.Mutex
			for _, oid := range userOrgIDs {
				oid := oid
				evtWg.Add(1)
				go func() {
					defer evtWg.Done()
					params := url.Values{}
					params.Set("organization_id", strconv.Itoa(oid))
					params.Set("limit", "1000")
					evts, err := client.GetAdminEvents(ctx, token, params)
					if err != nil {
						log.Printf("dashboard: could not load events for org %d: %v", oid, err)
					}
					evtMu.Lock()
					events = append(events, evts...)
					evtMu.Unlock()
				}()
			}
			evtWg.Wait()
			sort.Slice(events, func(i, j int) bool {
				return events[i].StartTime < events[j].StartTime
			})
		}

		orgMap := make(map[int]string, len(userOrgs))
		for _, o := range userOrgs {
			orgMap[o.ID] = o.Name
		}

		// #1332: org -> its assigned locations (mirrors adminOrgDashboardHandler's
		// OrgLocations), plus future-event counts per org and per (org, location)
		// pair, computed from the events slice above rather than a second fetch.
		orgLocations := make(map[int][]Location, len(userOrgs))
		for _, l := range allLocs {
			for _, oid := range l.OrganizationIDs {
				if orgSet[oid] {
					orgLocations[oid] = append(orgLocations[oid], l)
				}
			}
		}
		orgFutureCount := make(map[int]int, len(userOrgs))
		locFutureCount := make(map[int]map[int]int, len(userOrgs))
		for _, ev := range events {
			if ev.OrganizationID == nil {
				continue
			}
			oid := *ev.OrganizationID
			orgFutureCount[oid]++
			if ev.LocationID != nil {
				if locFutureCount[oid] == nil {
					locFutureCount[oid] = make(map[int]int)
				}
				locFutureCount[oid][*ev.LocationID]++
			}
		}

		// Presets: templates the user has access to (own + orgs they belong to;
		// all orgs for admins, matching adminTemplatesHandler's scope), split
		// into pinned (shown on the dashboard) and unpinned (the "add" dropdown).
		templateOrgIDs := userOrgIDs
		if su.Role == "admin" {
			templateOrgIDs = make([]int, 0, len(allOrgs))
			for _, o := range allOrgs {
				templateOrgIDs = append(templateOrgIDs, o.ID)
			}
		}
		accessibleTemplates, _ := listTemplates(db, su.ID, templateOrgIDs)
		pinnedIDs, _ := listPinnedTemplateIDs(db, su.ID)
		var pinnedTemplates, unpinnedTemplates []EventTemplate
		for _, t := range accessibleTemplates {
			if pinnedIDs[t.ID] {
				pinnedTemplates = append(pinnedTemplates, t)
			} else {
				unpinnedTemplates = append(unpinnedTemplates, t)
			}
		}
		templateOrgMap := make(map[int]string, len(allOrgs))
		for _, o := range allOrgs {
			templateOrgMap[o.ID] = o.Name
		}

		title := i18n.T(r, "dashboard_title")
		renderTemplate(w, tmpls.dashboard, tmplData(r, cfg, i18n, title, DashboardData{
			Stats:             stats,
			UserOrgs:          userOrgs,
			OrgMap:            orgMap,
			OrgStats:          orgStats,
			Events:            events,
			PinnedTemplates:   pinnedTemplates,
			UnpinnedTemplates: unpinnedTemplates,
			TemplateOrgMap:    templateOrgMap,
			Series:            series,
			OrgLocations:      orgLocations,
			OrgFutureCount:    orgFutureCount,
			LocFutureCount:    locFutureCount,
		}))
	}
}
