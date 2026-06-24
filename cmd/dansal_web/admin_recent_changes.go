package main

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
)

// RecentChange is one row in the unified "recent changes" overview: the most
// recent edit (or feed-fetch update, for events) across all admin-managed
// entity types, merged and sorted by time.
type RecentChange struct {
	Kind      string // events, locations, organizations, musicians, instructors
	Name      string
	EditURL   string
	ChangedBy string
	ChangedAt int64
	ViaFetch  bool
}

const recentChangesLimit = 50

// AdminRecentChangesData is the template data for /admin/recent-changes.
type AdminRecentChangesData struct {
	Changes []RecentChange
}

// adminRecentChangesHandler renders a single time-sorted overview of the
// last edit to every event/location/organization/musician/instructor, by
// merging the existing list endpoints in Go rather than adding a dedicated
// REST endpoint. It's a last-state snapshot, not a full history.
//
// Available to any logged-in user (same gate as /admin/management, no role
// check) — changed_by is never exposed to anonymous visitors since this
// route requires login.
func adminRecentChangesHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		token := getSessionToken(r)

		var changes []RecentChange

		if events, err := client.GetAdminEvents(ctx, token, url.Values{"include_past": {"true"}}); err == nil {
			for _, ev := range events {
				if ev.ChangedAt == "" {
					continue
				}
				ts := parseChangedAt(ev.ChangedAt)
				if ts == 0 {
					continue
				}
				changes = append(changes, RecentChange{
					Kind:      "events",
					Name:      ev.Title,
					EditURL:   "/admin/events/" + strconv.Itoa(ev.ID) + "/edit",
					ChangedBy: ev.ChangedBy,
					ChangedAt: ts,
					ViaFetch:  ev.ChangedBy == "fetch",
				})
			}
		}
		if locs, err := client.GetLocations(ctx); err == nil {
			for _, loc := range locs {
				if loc.UpdatedAt == 0 {
					continue
				}
				changes = append(changes, RecentChange{
					Kind:      "locations",
					Name:      loc.Location,
					EditURL:   "/admin/locations/" + strconv.Itoa(loc.ID) + "/edit",
					ChangedBy: loc.UpdatedBy,
					ChangedAt: loc.UpdatedAt,
				})
			}
		}
		if orgs, err := client.GetOrganizations(ctx); err == nil {
			for _, o := range orgs {
				if o.UpdatedAt == 0 {
					continue
				}
				changes = append(changes, RecentChange{
					Kind:      "organizations",
					Name:      o.Name,
					EditURL:   "/admin/organizations/" + strconv.Itoa(o.ID) + "/edit",
					ChangedBy: o.UpdatedBy,
					ChangedAt: o.UpdatedAt,
				})
			}
		}
		if musicians, err := client.GetMusicians(ctx); err == nil {
			for _, m := range musicians {
				if m.UpdatedAt == 0 {
					continue
				}
				changes = append(changes, RecentChange{
					Kind:      "musicians",
					Name:      m.Bandname,
					EditURL:   "/admin/musicians/" + strconv.Itoa(m.ID) + "/edit",
					ChangedBy: m.UpdatedBy,
					ChangedAt: m.UpdatedAt,
				})
			}
		}
		if instructors, err := client.GetInstructors(ctx); err == nil {
			for _, inst := range instructors {
				if inst.UpdatedAt == 0 {
					continue
				}
				changes = append(changes, RecentChange{
					Kind:      "instructors",
					Name:      inst.Name,
					EditURL:   "/admin/instructors/" + strconv.Itoa(inst.ID) + "/edit",
					ChangedBy: inst.UpdatedBy,
					ChangedAt: inst.UpdatedAt,
				})
			}
		}

		sort.Slice(changes, func(i, j int) bool { return changes[i].ChangedAt > changes[j].ChangedAt })
		if len(changes) > recentChangesLimit {
			changes = changes[:recentChangesLimit]
		}

		title := i18n.T(r, "recent_changes_title")
		renderTemplate(w, tmpls.adminRecentChanges, tmplData(r, cfg, i18n, title, AdminRecentChangesData{
			Changes: changes,
		}))
	}
}
