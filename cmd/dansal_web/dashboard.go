package main

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
)

// DashboardData is the template data for /dashboard.
type DashboardData struct {
	Stats    MeStats
	UserOrgs []Organization
	OrgMap   map[int]string
	OrgStats map[int]OrgStatRecord
	Events   []Event
}

func dashboardHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
			mu         sync.Mutex
			wg         sync.WaitGroup
		)

		wg.Add(4)
		go func() {
			defer wg.Done()
			userOrgIDs, _ = client.GetUserOrganizationIDs(ctx, su.ID, token)
		}()
		go func() {
			defer wg.Done()
			allOrgs, _ = client.GetOrganizations(ctx)
		}()
		go func() {
			defer wg.Done()
			stats, _ = client.GetMeStats(ctx, token)
		}()
		go func() {
			defer wg.Done()
			orgStats, _ = client.GetOrgStats(ctx)
		}()
		wg.Wait()

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
			for _, oid := range userOrgIDs {
				oid := oid
				evtWg.Add(1)
				go func() {
					defer evtWg.Done()
					params := url.Values{}
					params.Set("organization_id", strconv.Itoa(oid))
					evts, _ := client.GetAdminEvents(ctx, token, params)
					mu.Lock()
					events = append(events, evts...)
					mu.Unlock()
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

		title := i18n.T(r, "dashboard_title")
		renderTemplate(w, tmpls.dashboard, tmplData(r, cfg, i18n, title, DashboardData{
			Stats:    stats,
			UserOrgs: userOrgs,
			OrgMap:   orgMap,
			OrgStats: orgStats,
			Events:   events,
		}))
	}
}
