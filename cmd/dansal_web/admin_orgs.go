package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ── Organizations ─────────────────────────────────────────────────────────────

func orgFromForm(r *http.Request) Organization {
	return Organization{
		Name:         strings.TrimSpace(r.FormValue("name")),
		Description:  strings.TrimSpace(r.FormValue("description")),
		ActorName:    strings.TrimSpace(r.FormValue("actor_name")),
		Website:      strings.TrimSpace(r.FormValue("website")),
		Instagram:    strings.TrimSpace(r.FormValue("instagram")),
		Mastodon:     strings.TrimSpace(r.FormValue("mastodon")),
		Facebook:     strings.TrimSpace(r.FormValue("facebook")),
		ContactEmail: strings.TrimSpace(r.FormValue("contact_email")),
		ContactName:  strings.TrimSpace(r.FormValue("contact_name")),
		NotesMd:      strings.TrimSpace(r.FormValue("notes_md")),
	}
}

type OrgStats struct {
	Org           Organization
	Slug          string
	EventCount    int
	LocationCount int
	FetchSources  []FetchSource
	MainTown      string
	CanEdit       bool // true for admins and org members
}

type AdminOrgsData struct {
	Stats   []OrgStats
	CanEdit bool
}

type AdminOrgEditData struct {
	Org                 Organization
	ErrorKey            string
	Follows             []FollowRecord
	FollowErr           string
	Members             []OrgMember
	AssignedLocations   []Location
	UnassignedLocations []Location
	HasActorWithFollowers bool // True if organization has an actor that has followers
	IsAdmin             bool
}

func adminOrgsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		var orgs []Organization
		var statMap map[int]OrgStatRecord
		var sources []FetchSource
		var locations []Location
		var orgsErr error
		var wg sync.WaitGroup
		wg.Add(4)
		go func() { defer wg.Done(); orgs, orgsErr = client.GetOrganizations(r.Context()) }()
		go func() { defer wg.Done(); statMap, _ = client.GetOrgStats(r.Context()) }()
		go func() { defer wg.Done(); sources, _ = client.GetFetchSources(r.Context(), token) }()
		go func() { defer wg.Done(); locations, _ = client.GetLocations(r.Context()) }()
		wg.Wait()
		if orgsErr != nil {
			http.Error(w, "could not load organizations", http.StatusBadGateway)
			return
		}
		srcsByOrg := map[int][]FetchSource{}
		for _, s := range sources {
			if s.OrganizationID != nil {
				srcsByOrg[*s.OrganizationID] = append(srcsByOrg[*s.OrganizationID], s)
			}
		}
		// Build town frequency map per org from linked locations.
		townsByOrg := map[int]map[string]int{}
		for _, loc := range locations {
			if loc.Town == "" {
				continue
			}
			for _, oid := range loc.OrganizationIDs {
				if townsByOrg[oid] == nil {
					townsByOrg[oid] = map[string]int{}
				}
				townsByOrg[oid][loc.Town]++
			}
		}
		mainTown := func(orgID int) string {
			towns := townsByOrg[orgID]
			best, bestCount := "", 0
			for t, c := range towns {
				if c > bestCount || (c == bestCount && t < best) {
					best, bestCount = t, c
				}
			}
			return best
		}
		isAdmin := user.Role == "admin"
		// For non-admins, build their org membership set to show edit button per org.
		myOrgIDs := map[int]bool{}
		if !isAdmin {
			for _, id := range getUserOrgIDsFromOrgs(r.Context(), client, user.ID, token, orgs) {
				myOrgIDs[id] = true
			}
		}
		stats := make([]OrgStats, len(orgs))
		for i, o := range orgs {
			st := statMap[o.ID]
			stats[i] = OrgStats{
				Org:           o,
				Slug:          effectiveSlug(o),
				EventCount:    st.EventCount,
				LocationCount: st.LocationCount,
				FetchSources:  srcsByOrg[o.ID],
				MainTown:      mainTown(o.ID),
				CanEdit:       isAdmin || myOrgIDs[o.ID],
			}
		}
		title := i18n.T(r, "admin_orgs_title")
		renderTemplate(w, tmpls.adminOrgs, tmplData(r, cfg, i18n, title, AdminOrgsData{
			Stats:   stats,
			CanEdit: isAdmin,
		}))
	}
}

func adminOrgNewPageHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		title := i18n.T(r, "admin_new")
		renderTemplate(w, tmpls.adminOrgEdit, tmplData(r, cfg, i18n, title, AdminOrgEditData{}))
	}
}

func adminOrgCreateHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		org := orgFromForm(r)
		token := getSessionToken(r)
		created, err := client.CreateOrganization(r.Context(), org, token)
		if err != nil {
			title := i18n.T(r, "admin_new")
			renderTemplate(w, tmpls.adminOrgEdit, tmplData(r, cfg, i18n, title, AdminOrgEditData{
				Org:      org,
				ErrorKey: "admin_save_error",
			}))
			return
		}
		if file, header, ferr := r.FormFile("image"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadOrgImage(r.Context(), created.ID, data, header.Filename, token); uerr != nil {
				log.Printf("upload org image error: %v", uerr)
				errKey := "admin_save_error"
				if strings.Contains(uerr.Error(), "too large") {
					errKey = "image_too_large"
				}
				title := i18n.T(r, "admin_new")
				renderTemplate(w, tmpls.adminOrgEdit, tmplData(r, cfg, i18n, title, AdminOrgEditData{
					Org:      created,
					ErrorKey: errKey,
				}))
				return
			}
		}
		http.Redirect(w, r, "/admin/organizations", http.StatusSeeOther)
	}
}

func adminOrgEditPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" && user.Role != "user" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		org, err := client.GetOrganization(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		// Non-admins may only edit orgs they belong to.
		if user.Role != "admin" {
			myOrgs := getUserOrgIDsFromOrgs(r.Context(), client, user.ID, token, []Organization{org})
			if len(myOrgs) == 0 {
				http.Error(w, "Forbidden: you are not a member of this organisation", http.StatusForbidden)
				return
			}
		}
		var follows []FollowRecord
		hasActorWithFollowers := false
		if actor, err := getActorByOrgID(db, id); err == nil {
			follows, _ = listFollows(db, actor.ID)
			// Check incoming followers (distinct from outgoing follows above)
			if fs, _ := listFollowers(db, id); len(fs) > 0 {
				hasActorWithFollowers = true
			}
		}
		members, _ := client.GetOrganizationMembers(r.Context(), id, token)
		allLocations, _ := client.GetLocations(r.Context())
		var assigned, unassigned []Location
		for _, loc := range allLocations {
			found := false
			for _, oid := range loc.OrganizationIDs {
				if oid == id {
					found = true
					break
				}
			}
			if found {
				assigned = append(assigned, loc)
			} else {
				unassigned = append(unassigned, loc)
			}
		}
		// Collect reference coordinates from assigned locations that have them.
		var refLats, refLngs []float64
		for _, loc := range assigned {
			if loc.Latitude != nil && loc.Longitude != nil {
				refLats = append(refLats, *loc.Latitude)
				refLngs = append(refLngs, *loc.Longitude)
			}
		}
		sort.SliceStable(unassigned, func(i, j int) bool {
			li, lj := unassigned[i], unassigned[j]
			if len(refLats) > 0 {
				iHas := li.Latitude != nil && li.Longitude != nil
				jHas := lj.Latitude != nil && lj.Longitude != nil
				if iHas != jHas {
					return iHas
				}
				if iHas {
					di, dj := math.MaxFloat64, math.MaxFloat64
					for k := range refLats {
						if d := geoDistKm(refLats[k], refLngs[k], *li.Latitude, *li.Longitude); d < di {
							di = d
						}
						if d := geoDistKm(refLats[k], refLngs[k], *lj.Latitude, *lj.Longitude); d < dj {
							dj = d
						}
					}
					if math.Abs(di-dj) > 0.1 {
						return di < dj
					}
				}
			}
			if li.Town != lj.Town {
				return li.Town < lj.Town
			}
			ni := li.ShortName
			if ni == "" {
				ni = li.Location
			}
			nj := lj.ShortName
			if nj == "" {
				nj = lj.Location
			}
			return ni < nj
		})
		title := i18n.T(r, "admin_edit")
		renderTemplate(w, tmpls.adminOrgEdit, tmplData(r, cfg, i18n, title, AdminOrgEditData{
			Org:                   org,
			Follows:               follows,
			FollowErr:             r.URL.Query().Get("follow_err"),
			Members:               members,
			AssignedLocations:     assigned,
			UnassignedLocations:   unassigned,
			HasActorWithFollowers: hasActorWithFollowers,
			IsAdmin:               user.Role == "admin",
		}))
	}
}

func adminOrgFollowHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
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
		input := strings.TrimSpace(r.FormValue("followee"))
		if input == "" {
			http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", id), http.StatusSeeOther)
			return
		}
		actor, err := getActorByOrgID(db, id)
		if err != nil {
			http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit?follow_err=no+actor", id), http.StatusSeeOther)
			return
		}
		apID, inboxURL, err := resolveActorFromInput(r.Context(), client.HTTP, input)
		if err != nil {
			log.Printf("follow resolve %q: %v", input, err)
			http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit?follow_err=%s", id, url.QueryEscape("could not resolve: "+err.Error())), http.StatusSeeOther)
			return
		}
		followActivityID, err := sendFollowActivity(cfg, actor, apID, inboxURL)
		if err != nil {
			log.Printf("sendFollow %s: %v", apID, err)
			http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit?follow_err=%s", id, url.QueryEscape("delivery failed: "+err.Error())), http.StatusSeeOther)
			return
		}
		if err := addFollow(db, actor.ID, apID, inboxURL, followActivityID); err != nil {
			log.Printf("addFollow: %v", err)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", id), http.StatusSeeOther)
	}
}

func adminOrgUnfollowHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
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
		followeeAPID := strings.TrimSpace(r.FormValue("followee_ap_id"))
		if followeeAPID == "" {
			http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", id), http.StatusSeeOther)
			return
		}
		actor, err := getActorByOrgID(db, id)
		if err != nil {
			http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", id), http.StatusSeeOther)
			return
		}
		if follow, err := getFollow(db, actor.ID, followeeAPID); err == nil {
			if err := sendUndoFollow(cfg, actor, followeeAPID, follow.FolloweeInbox, follow.FollowActivityID); err != nil {
				log.Printf("sendUndoFollow %s: %v", followeeAPID, err)
			}
		}
		if err := removeFollow(db, actor.ID, followeeAPID); err != nil {
			log.Printf("removeFollow: %v", err)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", id), http.StatusSeeOther)
	}
}

func adminOrgMemberHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		orgID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		action := r.FormValue("action")
		userID, err := strconv.Atoi(r.FormValue("user_id"))
		if err != nil {
			http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", orgID), http.StatusSeeOther)
			return
		}
		if action == "remove" {
			_ = client.RemoveOrgMember(r.Context(), orgID, userID, token)
		} else {
			_ = client.AddOrgMember(r.Context(), orgID, userID, token)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", orgID), http.StatusSeeOther)
	}
}

func adminOrgLocationsHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		orgID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		action := r.FormValue("action")
		locID, err := strconv.Atoi(r.FormValue("location_id"))
		if err != nil {
			http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", orgID), http.StatusSeeOther)
			return
		}
		if action == "remove" {
			_ = client.UnassignLocationOrg(r.Context(), locID, orgID, token)
		} else {
			_ = client.BulkAssignLocationOrg(r.Context(), []int{locID}, &orgID, token)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", orgID), http.StatusSeeOther)
	}
}

func adminOrgSaveHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" && user.Role != "user" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}

		// Fetch original organization to detect actor name changes
		originalOrg, err := client.GetOrganization(r.Context(), id)
		if err != nil {
			http.Error(w, "failed to load organization: "+err.Error(), http.StatusBadGateway)
			return
		}
		token := getSessionToken(r)
		if user.Role != "admin" {
			myOrgs := getUserOrgIDsFromOrgs(r.Context(), client, user.ID, token, []Organization{originalOrg})
			if len(myOrgs) == 0 {
				http.Error(w, "Forbidden: you are not a member of this organisation", http.StatusForbidden)
				return
			}
		}

		if err := r.ParseMultipartForm(10 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		org := orgFromForm(r)
		if err := client.UpdateOrganization(r.Context(), id, org, token); err != nil {
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, tmpls.adminOrgEdit, tmplData(r, cfg, i18n, title, AdminOrgEditData{
				Org:      org,
				ErrorKey: "admin_save_error",
				IsAdmin:  user.Role == "admin",
			}))
			return
		}
		if file, header, ferr := r.FormFile("image"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadOrgImage(r.Context(), id, data, header.Filename, token); uerr != nil {
				log.Printf("upload org image error: %v", uerr)
				errKey := "admin_save_error"
				if strings.Contains(uerr.Error(), "too large") {
					errKey = "image_too_large"
				}
				title := i18n.T(r, "admin_edit")
				renderTemplate(w, tmpls.adminOrgEdit, tmplData(r, cfg, i18n, title, AdminOrgEditData{
					Org:      org,
					ErrorKey: errKey,
					IsAdmin:  user.Role == "admin",
				}))
				return
			}
		}

		// Handle actor rename if actor name changed
		if org.ActorName != originalOrg.ActorName {
			newSlug := effectiveSlug(org)
			if newSlug != effectiveSlug(originalOrg) {
				if _, err := ensureActorWithMove(cfg, db, id, newSlug); err != nil {
					log.Printf("actor rename for org %d: %v", id, err)
				}
			}
		}

		http.Redirect(w, r, "/admin/organizations", http.StatusSeeOther)
	}
}

func adminOrgDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = client.DeleteOrganization(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, "/admin/organizations", http.StatusSeeOther)
	}
}

func adminOrgRunFeedsHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		sources, err := client.GetFetchSources(r.Context(), token)
		if err == nil {
			var ids []int
			for _, s := range sources {
				if s.OrganizationID != nil && *s.OrganizationID == id {
					ids = append(ids, s.ID)
				}
			}
			if len(ids) > 0 {
				_ = client.BulkRunFetchSources(r.Context(), ids, token)
			}
		}
		http.Redirect(w, r, "/admin/organizations", http.StatusSeeOther)
	}
}

// GET /admin/organizations/check-actor-name?name=foo[&id=123]
// AJAX endpoint: checks if an actor_name is available.
func adminOrgCheckActorNameHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			json.NewEncoder(w).Encode(map[string]any{"available": false, "reason": "empty"})
			return
		}
		if name == cfg.RelayActorName {
			json.NewEncoder(w).Encode(map[string]any{"available": false, "reason": "reserved"})
			return
		}
		excludeID, _ := strconv.Atoi(r.URL.Query().Get("id"))
		orgs, err := client.GetOrganizations(r.Context())
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"available": false, "reason": "error"})
			return
		}
		for _, o := range orgs {
			if o.ActorName == name && o.ID != excludeID {
				json.NewEncoder(w).Encode(map[string]any{"available": false, "reason": "taken"})
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"available": true})
	}
}

func adminOrgImageDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		_ = client.DeleteOrgImage(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", id), http.StatusSeeOther)
	}
}
