package main

import (
	"context"
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
	"time"
)

// ── Organizations ─────────────────────────────────────────────────────────────

func orgFromForm(r *http.Request) Organization {
	return Organization{
		Name:             strings.TrimSpace(r.FormValue("name")),
		Description:      strings.TrimSpace(r.FormValue("description")),
		ActorName:        strings.TrimSpace(r.FormValue("actor_name")),
		Website:          strings.TrimSpace(r.FormValue("website")),
		Instagram:        strings.TrimSpace(r.FormValue("instagram")),
		Mastodon:         strings.TrimSpace(r.FormValue("mastodon")),
		Facebook:         strings.TrimSpace(r.FormValue("facebook")),
		ContactEmail:     strings.TrimSpace(r.FormValue("contact_email")),
		ContactName:      strings.TrimSpace(r.FormValue("contact_name")),
		WikidataID:       strings.TrimSpace(r.FormValue("wikidata_id")),
		NotesMd:          strings.TrimSpace(r.FormValue("notes_md")),
		ChatLinks:        chatLinksFromForm(r),
		ImageAIGenerated: r.FormValue("image_ai_generated") == "1",
	}
}

// chatLinksFromForm reads one URL input per known chat_links platform
// (chat_<platform>, see chatLinkPlatformOrder) and builds the ChatLinks
// array, skipping any left blank.
func chatLinksFromForm(r *http.Request) []ChatLink {
	var links []ChatLink
	for _, p := range chatLinkPlatformOrder {
		if url := strings.TrimSpace(r.FormValue("chat_" + p.Slug)); url != "" {
			links = append(links, ChatLink{Platform: p.Slug, URL: url})
		}
	}
	return links
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
	Org                   Organization
	ErrorKey              string
	Follows               []FollowRecord
	FollowErr             string
	Members               []OrgMember
	AssignedLocations     []Location
	UnassignedLocations   []Location
	HasActorWithFollowers bool // True if organization has an actor that has followers
	IsAdmin               bool
	From                  string
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
		err := fetchParallel(
			func() error { var err error; orgs, err = client.GetOrganizations(r.Context()); return err },
			func() error {
				var err error
				statMap, err = client.GetOrgStats(r.Context())
				if err != nil {
					log.Printf("admin orgs: could not load stats: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				sources, err = client.GetFetchSources(r.Context(), token)
				if err != nil {
					log.Printf("admin orgs: could not load fetch sources: %v", err)
				}
				return nil
			},
			func() error {
				var err error
				locations, err = client.GetLocations(r.Context())
				if err != nil {
					log.Printf("admin orgs: could not load locations: %v", err)
				}
				return nil
			},
		)
		if err != nil {
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
		myOrgIDs := memberOrgSet(r, client, user)
		if isAdmin {
			myOrgIDs = map[int]bool{}
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
			forbidden(w, r)
			return
		}
		title := i18n.T(r, "admin_new")
		renderTemplate(w, tmpls.adminOrgEdit, tmplData(r, cfg, i18n, title, AdminOrgEditData{IsAdmin: user.Role == "admin"}))
	}
}

func adminOrgCreateHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			forbidden(w, r)
			return
		}
		if err := r.ParseMultipartForm(maxMultipartSize); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		org := orgFromForm(r)
		if err := validateURLDomain(r.Context(), org.Website); err != nil {
			title := i18n.T(r, "admin_new")
			renderTemplate(w, tmpls.adminOrgEdit, tmplData(r, cfg, i18n, title, AdminOrgEditData{
				Org:      org,
				ErrorKey: "url_domain_not_found",
				IsAdmin:  user.Role == "admin",
			}))
			return
		}
		token := getSessionToken(r)
		created, err := client.CreateOrganization(r.Context(), org, token)
		if err != nil {
			title := i18n.T(r, "admin_new")
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
					IsAdmin:  user.Role == "admin",
				}))
				return
			}
		}
		if file, header, ferr := r.FormFile("avatar"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadOrgAvatar(r.Context(), created.ID, data, header.Filename, token); uerr != nil {
				log.Printf("upload org avatar error: %v", uerr)
			}
		}
		go notifyIndexNowPaths(cfg.publicBaseURL(), siteCfg.IndexNowKey(), []string{"/org/" + effectiveSlug(created)})
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
			forbidden(w, r)
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
		// Non-admins may only edit orgs they belong to.
		if user.Role != "admin" && !memberOrgSet(r, client, user)[org.ID] {
			forbidden(w, r)
			return
		}
		token := getSessionToken(r)
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
			From:                  safeReturnPath(r.URL.Query().Get("from")),
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
			forbidden(w, r)
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
			forbidden(w, r)
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
			forbidden(w, r)
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
			if err := client.RemoveOrgMember(r.Context(), orgID, userID, token); err != nil {
				log.Printf("remove org member %d from %d: %v", userID, orgID, err)
			}
		} else {
			if err := client.AddOrgMember(r.Context(), orgID, userID, token); err != nil {
				log.Printf("add org member %d to %d: %v", userID, orgID, err)
			}
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
		orgID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		if user.Role != "admin" {
			org, err := client.GetOrganization(r.Context(), orgID)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			if !memberOrgSet(r, client, user)[org.ID] {
				forbidden(w, r)
				return
			}
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		action := r.FormValue("action")
		locID, err := strconv.Atoi(r.FormValue("location_id"))
		if err != nil {
			http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", orgID), http.StatusSeeOther)
			return
		}
		if action == "remove" {
			if err := client.UnassignLocationOrg(r.Context(), locID, orgID, token); err != nil {
				log.Printf("unassign org %d from location %d: %v", orgID, locID, err)
			}
		} else {
			if err := client.BulkAssignLocationOrg(r.Context(), []int{locID}, &orgID, token); err != nil {
				log.Printf("assign org %d to location %d: %v", orgID, locID, err)
			}
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
			forbidden(w, r)
			return
		}
		// A logo upload triggers a slow AVIF re-encode on the backend (WASM-based
		// encoder, can take well over the server's default 30s WriteTimeout for a
		// detailed photo) — extend the deadline for this request rather than
		// raising it server-wide.
		adminWriteDeadline(w)
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
		if user.Role != "admin" && !memberOrgSet(r, client, user)[originalOrg.ID] {
			forbidden(w, r)
			return
		}

		if err := r.ParseMultipartForm(maxMultipartSize); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		from := safeReturnPath(r.FormValue("from"))
		org := orgFromForm(r)
		if err := validateURLDomain(r.Context(), org.Website); err != nil {
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, tmpls.adminOrgEdit, tmplData(r, cfg, i18n, title, AdminOrgEditData{
				Org:      org,
				ErrorKey: "url_domain_not_found",
				IsAdmin:  user.Role == "admin",
				From:     from,
			}))
			return
		}
		if err := client.UpdateOrganization(r.Context(), id, org, token); err != nil {
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, tmpls.adminOrgEdit, tmplData(r, cfg, i18n, title, AdminOrgEditData{
				Org:      org,
				ErrorKey: "admin_save_error",
				IsAdmin:  user.Role == "admin",
				From:     from,
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
					From:     from,
				}))
				return
			}
		}
		if file, header, ferr := r.FormFile("avatar"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadOrgAvatar(r.Context(), id, data, header.Filename, token); uerr != nil {
				log.Printf("upload org avatar error: %v", uerr)
			}
		}

		go notifyIndexNowPaths(cfg.publicBaseURL(), siteCfg.IndexNowKey(), []string{"/org/" + effectiveSlug(org)})

		// Handle actor rename if actor name changed
		if org.ActorName != originalOrg.ActorName {
			newSlug := effectiveSlug(org)
			if newSlug != effectiveSlug(originalOrg) {
				if _, err := ensureActorWithMove(cfg, db, id, newSlug); err != nil {
					log.Printf("actor rename for org %d: %v", id, err)
				}
			}
		}

		target := "/admin/organizations"
		if from != "" {
			target = from
		}
		if p := safeReturnPath(target); p != "" {
			target = p
		} else {
			target = "/admin/organizations"
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}

func adminOrgDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			forbidden(w, r)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := client.DeleteOrganization(r.Context(), id, getSessionToken(r)); err != nil {
			log.Printf("delete organization %d: %v", id, err)
		}
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
				if err := client.BulkRunFetchSources(r.Context(), ids, token); err != nil {
					log.Printf("run feeds for org %d: %v", id, err)
				}
			}
		}
		http.Redirect(w, r, "/admin/organizations", http.StatusSeeOther)
	}
}

// POST /admin/organizations/{id}/redeliver
// Re-delivers Create activities for all published events of this org to its
// AP followers. Uses Create (not Update) so Mastodon adds posts it has never
// seen before. Existing posts are simply re-created, which is idempotent for
// Mastodon (it deduplicates by Note ID).
func adminOrgRedeliverHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
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
		// Redelivery blasts the org's whole follower list — restrict it to
		// admins and members of that org, matching adminOrgEditPageHandler's
		// access rule (#1002).
		if user.Role != "admin" && !memberOrgSet(r, client, user)[id] {
			forbidden(w, r)
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			params := url.Values{
				"organization_id": {strconv.Itoa(id)},
				"limit":           {"200"},
				"future":          {"true"},
				"include_past":    {"true"},
			}
			events, err := client.GetEventsFiltered(ctx, params)
			if err != nil {
				log.Printf("redeliver org %d: fetch events: %v", id, err)
				return
			}
			actor, err := getActorByOrgID(db, id)
			if err != nil {
				log.Printf("redeliver org %d: actor not found: %v", id, err)
				return
			}
			sent := 0
			for _, e := range events {
				if !e.IsPublished {
					continue
				}
				activity := buildCreateActivity(cfg, actor.OrgSlug, e)
				if err := deliverActivityToFollowers(cfg, db, actor, activity); err != nil {
					log.Printf("redeliver org %d event %d: %v", id, e.ID, err)
				} else {
					sent++
				}
			}
			log.Printf("redeliver org %d: sent %d Create activities", id, sent)
		}()
		http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", id), http.StatusSeeOther)
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
			forbidden(w, r)
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
		if err := client.DeleteOrgImage(r.Context(), id, getSessionToken(r)); err != nil {
			log.Printf("delete org image %d: %v", id, err)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", id), http.StatusSeeOther)
	}
}

func adminOrgAvatarDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		if err := client.DeleteOrgAvatar(r.Context(), id, getSessionToken(r)); err != nil {
			log.Printf("delete org avatar %d: %v", id, err)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/organizations/%d/edit", id), http.StatusSeeOther)
	}
}
