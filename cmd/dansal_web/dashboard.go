package main

import (
	"database/sql"
	"fmt"
	"github.com/ademant/dansal/internal/logging"
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
	// BoardAlerts (#1717) are upcoming board posts grouped by event, one row
	// per (event × type-group), sorted by event start ascending. Empty when
	// the user has no orgs or none of their upcoming events have active posts.
	BoardAlerts []DashBoardAlert
}

// DashBoardAlert is one line in the dashboard board-posts section.
type DashBoardAlert struct {
	EventID    int
	EventTitle string
	StartTime  string
	TypeGroup  string // "ride", "sleep", "ticket", "lost"
	Count      int
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
					logging.Errorf("dashboard: could not load user orgs: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				allOrgs, err = client.GetOrganizations(ctx)
				if err != nil {
					logging.Errorf("dashboard: could not load organizations: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				stats, err = client.GetMeStats(ctx, token)
				if err != nil {
					logging.Errorf("dashboard: could not load stats: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				orgStats, err = client.GetOrgStats(ctx)
				if err != nil {
					logging.Errorf("dashboard: could not load org stats: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				allLocs, err = client.GetLocations(ctx)
				if err != nil {
					logging.Errorf("dashboard: could not load locations: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				series, err = client.GetSeriesList(ctx, token)
				if err != nil {
					logging.Errorf("dashboard: could not load series: %v", err)
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
						logging.Errorf("dashboard: could not load events for org %d: %v", oid, err)
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

		// Board alerts (#1717): fetch active, upcoming board posts for the user's
		// orgs and group them by event × type-group for the dashboard section.
		var boardAlerts []DashBoardAlert
		if len(userOrgIDs) > 0 {
			params := url.Values{}
			params.Set("upcoming", "1")
			for _, oid := range userOrgIDs {
				params.Add("organization_id", strconv.Itoa(oid))
			}
			posts, err := client.GetAllContactPosts(ctx, params)
			if err != nil {
				logging.Errorf("dashboard: could not load board posts: %v", err)
			} else if len(posts) > 0 {
				type key struct {
					eventID   int
					typeGroup string
				}
				type evtMeta struct{ title, startTime string }
				counts := make(map[key]int)
				metas := make(map[int]evtMeta)
				for _, p := range posts {
					g := "ride"
					switch p.Type {
					case "sleep_offer", "sleep_request":
						g = "sleep"
					case "ticket_offer", "ticket_request":
						g = "ticket"
					case "lost_item", "found_item":
						g = "lost"
					}
					counts[key{p.EventID, g}]++
					if _, ok := metas[p.EventID]; !ok && p.Event != nil {
						metas[p.EventID] = evtMeta{p.Event.Title, p.Event.StartTime}
					}
				}
				for k, n := range counts {
					m := metas[k.eventID]
					boardAlerts = append(boardAlerts, DashBoardAlert{
						EventID: k.eventID, EventTitle: m.title,
						StartTime: m.startTime, TypeGroup: k.typeGroup, Count: n,
					})
				}
				sort.Slice(boardAlerts, func(i, j int) bool {
					si, sj := boardAlerts[i].StartTime, boardAlerts[j].StartTime
					if si != sj {
						return si < sj
					}
					return fmt.Sprintf("%d%s", boardAlerts[i].EventID, boardAlerts[i].TypeGroup) <
						fmt.Sprintf("%d%s", boardAlerts[j].EventID, boardAlerts[j].TypeGroup)
				})
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
			BoardAlerts:       boardAlerts,
		}))
	}
}
