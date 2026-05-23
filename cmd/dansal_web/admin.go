package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// parseLatLng parses a form string to *float64; returns nil for empty or invalid input.
func parseLatLng(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return &f
	}
	return nil
}

func parseOsmID(s string) *int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return &n
	}
	return nil
}

// requireLogin redirects to /login if no session user, returning false when redirect was sent.
func requireLogin(w http.ResponseWriter, r *http.Request) (*SessionUser, bool) {
	u := getSessionUser(r)
	if u == nil {
		next := r.URL.RequestURI()
		http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
		return nil, false
	}
	return u, true
}

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
	}
}

type OrgStats struct {
	Org           Organization
	Slug          string
	EventCount    int
	LocationCount int
	FetchSources  []FetchSource
	MainTown      string
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
			}
		}
		title := i18n.T(r, "admin_orgs_title")
		renderTemplate(w, tmpls.adminOrgs, tmplData(r, cfg, i18n, title, AdminOrgsData{
			Stats:   stats,
			CanEdit: user.Role == "admin",
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
		if user.Role != "admin" {
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
		var follows []FollowRecord
		if actor, err := getActorByOrgID(db, id); err == nil {
			follows, _ = listFollows(db, actor.ID)
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
		title := i18n.T(r, "admin_edit")
		renderTemplate(w, tmpls.adminOrgEdit, tmplData(r, cfg, i18n, title, AdminOrgEditData{
			Org:                 org,
			Follows:             follows,
			FollowErr:           r.URL.Query().Get("follow_err"),
			Members:             members,
			AssignedLocations:   assigned,
			UnassignedLocations: unassigned,
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

func adminOrgSaveHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		org := orgFromForm(r)
		token := getSessionToken(r)
		if err := client.UpdateOrganization(r.Context(), id, org, token); err != nil {
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, tmpls.adminOrgEdit, tmplData(r, cfg, i18n, title, AdminOrgEditData{
				Org:      org,
				ErrorKey: "admin_save_error",
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
				}))
				return
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
		if name == "relay" {
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

// ── Musicians ─────────────────────────────────────────────────────────────────

type AdminMusiciansData struct {
	Musicians []Musician
}

type AdminMusicianEditData struct {
	Musician Musician
	Events   []Event
	IsNew    bool
	ErrorKey string
}

func musicianFromForm(r *http.Request) Musician {
	beginYear, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("begin_year")))
	return Musician{
		Bandname:     strings.TrimSpace(r.FormValue("bandname")),
		ShortName:    strings.TrimSpace(r.FormValue("short_name")),
		Internetsite: strings.TrimSpace(r.FormValue("internetsite")),
		Description:  strings.TrimSpace(r.FormValue("description")),
		MBID:         strings.TrimSpace(r.FormValue("mbid")),
		WikidataID:   strings.TrimSpace(r.FormValue("wikidata_id")),
		DiscogsID:    strings.TrimSpace(r.FormValue("discogs_id")),
		Country:      strings.TrimSpace(r.FormValue("country")),
		BeginYear:    beginYear,
		Biography:    strings.TrimSpace(r.FormValue("biography")),
		MembersJSON:  linesToJSON(r.FormValue("members")),
		AlbumsJSON:   linesToJSON(r.FormValue("albums")),
		Mastodon:     strings.TrimSpace(r.FormValue("mastodon")),
		Instagram:    strings.TrimSpace(r.FormValue("instagram")),
		Facebook:     strings.TrimSpace(r.FormValue("facebook")),
		Soundcloud:   strings.TrimSpace(r.FormValue("soundcloud")),
		Spotify:      strings.TrimSpace(r.FormValue("spotify")),
		Deezer:       strings.TrimSpace(r.FormValue("deezer")),
		Genre:        strings.TrimSpace(r.FormValue("genre")),
	}
}

// linesToJSON converts a newline-separated text input to a JSON string array.
func linesToJSON(s string) string {
	var items []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			items = append(items, line)
		}
	}
	if len(items) == 0 {
		return ""
	}
	b, _ := json.Marshal(items)
	return string(b)
}

func adminMusiciansHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		musicians, err := client.GetMusicians(r.Context())
		if err != nil {
			http.Error(w, "could not load musicians", http.StatusBadGateway)
			return
		}
		title := i18n.T(r, "admin_musicians_title")
		renderTemplate(w, tmpls.adminMusicians, tmplData(r, cfg, i18n, title, AdminMusiciansData{Musicians: musicians}))
	}
}

func adminMusicianNewPageHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		title := i18n.T(r, "admin_new")
		renderTemplate(w, tmpls.adminMusicianEdit, tmplData(r, cfg, i18n, title, AdminMusicianEditData{IsNew: true}))
	}
}

func adminMusicianCreateHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		m := musicianFromForm(r)
		created, err := client.CreateMusician(r.Context(), m, getSessionToken(r))
		if err != nil {
			title := i18n.T(r, "admin_new")
			renderTemplate(w, tmpls.adminMusicianEdit, tmplData(r, cfg, i18n, title, AdminMusicianEditData{
				Musician: m, IsNew: true, ErrorKey: "admin_save_error",
			}))
			return
		}
		if file, header, ferr := r.FormFile("image"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadMusicianImage(r.Context(), created.ID, data, header.Filename, getSessionToken(r)); uerr != nil {
				log.Printf("upload musician image error: %v", uerr)
			}
		}
		http.Redirect(w, r, "/admin/musicians", http.StatusSeeOther)
	}
}

func adminMusicianEditPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
		musician, err := client.GetMusician(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		title := i18n.T(r, "admin_edit")
		renderTemplate(w, tmpls.adminMusicianEdit, tmplData(r, cfg, i18n, title, AdminMusicianEditData{
			Musician: musician,
		}))
	}
}

func adminMusicianSaveHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		m := musicianFromForm(r)
		if err := client.UpdateMusician(r.Context(), id, m, getSessionToken(r)); err != nil {
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, tmpls.adminMusicianEdit, tmplData(r, cfg, i18n, title, AdminMusicianEditData{
				Musician: m, ErrorKey: "admin_save_error",
			}))
			return
		}
		if file, header, ferr := r.FormFile("image"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadMusicianImage(r.Context(), id, data, header.Filename, getSessionToken(r)); uerr != nil {
				log.Printf("upload musician image error: %v", uerr)
			}
		}
		http.Redirect(w, r, "/admin/musicians", http.StatusSeeOther)
	}
}

func adminMusicianDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		_ = client.DeleteMusician(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, "/admin/musicians", http.StatusSeeOther)
	}
}

// ── Fetch sources ─────────────────────────────────────────────────────────────

type AdminFetchurlsData struct {
	Sources []FetchSource
	OrgMap  map[int]Organization
	Orgs    []Organization
}

type AdminFetchurlEditData struct {
	Source             FetchSource
	Orgs               []Organization
	OrgMap             map[int]Organization
	Dances             []Dance
	SelectedDanceNames map[string]bool
	ErrorKey           string
}

func adminFetchurlsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		sources, err := client.GetFetchSources(r.Context(), token)
		if err != nil {
			http.Error(w, "could not load feed sources", http.StatusBadGateway)
			return
		}
		orgs, _ := client.GetOrganizations(r.Context())
		orgMap := make(map[int]Organization, len(orgs))
		for _, o := range orgs {
			orgMap[o.ID] = o
		}
		title := i18n.T(r, "admin_fetchurls_title")
		renderTemplate(w, tmpls.adminFetchurls, tmplData(r, cfg, i18n, title, AdminFetchurlsData{
			Sources: sources,
			OrgMap:  orgMap,
			Orgs:    orgs,
		}))
	}
}

type AdminFetchurlNewData struct {
	Orgs     []Organization
	ErrorKey string
	URL      string
}

func adminFetchurlNewPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		orgs, _ := client.GetOrganizations(r.Context())
		title := i18n.T(r, "fetch_new_title")
		renderTemplate(w, tmpls.adminFetchurlNew, tmplData(r, cfg, i18n, title, AdminFetchurlNewData{Orgs: orgs}))
	}
}

func adminFetchurlNewPostHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		rawURL := strings.TrimSpace(r.FormValue("url"))
		typ := r.FormValue("type")
		rawTags := strings.TrimSpace(r.FormValue("tags"))
		var tags []string
		for _, t := range strings.FieldsFunc(rawTags, func(r rune) bool { return r == ',' || r == ' ' }) {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		var orgID *int
		if v := r.FormValue("organization_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				orgID = &n
			}
		}
		token := getSessionToken(r)
		if _, err := client.CreateFetchSource(r.Context(), rawURL, typ, tags, orgID, token); err != nil {
			orgs, _ := client.GetOrganizations(r.Context())
			title := i18n.T(r, "fetch_new_title")
			renderTemplate(w, tmpls.adminFetchurlNew, tmplData(r, cfg, i18n, title, AdminFetchurlNewData{
				Orgs:     orgs,
				ErrorKey: "fetch_add_error",
				URL:      rawURL,
			}))
			return
		}
		http.Redirect(w, r, "/admin/fetchurls", http.StatusSeeOther)
	}
}

func adminFetchurlEditPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
		src, err := client.GetFetchSource(r.Context(), id, token)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		orgs, _ := client.GetOrganizations(r.Context())
		orgMap := make(map[int]Organization, len(orgs))
		for _, o := range orgs {
			orgMap[o.ID] = o
		}
		dances, _ := client.GetDances(r.Context())
		selected := buildSelectedDanceNamesFromIDs(src.DanceIDs, dances)
		title := i18n.T(r, "admin_edit")
		renderTemplate(w, tmpls.adminFetchurlEdit, tmplData(r, cfg, i18n, title, AdminFetchurlEditData{
			Source:             src,
			Orgs:               orgs,
			OrgMap:             orgMap,
			Dances:             dances,
			SelectedDanceNames: selected,
		}))
	}
}

func adminFetchurlSaveHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
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

		typ := r.FormValue("type")
		rawTags := strings.TrimSpace(r.FormValue("tags"))
		var tags []string
		for _, t := range strings.FieldsFunc(rawTags, func(r rune) bool { return r == ',' || r == ' ' }) {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		var orgID *int
		if v := r.FormValue("organization_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				orgID = &n
			}
		}

		var danceIDs []int
		for _, v := range r.Form["dance_ids"] {
			if n, err2 := strconv.Atoi(v); err2 == nil {
				danceIDs = append(danceIDs, n)
			}
		}

		token := getSessionToken(r)
		if err := client.UpdateFetchSource(r.Context(), id, typ, tags, danceIDs, orgID, token); err != nil {
			src, _ := client.GetFetchSource(r.Context(), id, token)
			orgs, _ := client.GetOrganizations(r.Context())
			orgMap := make(map[int]Organization, len(orgs))
			for _, o := range orgs {
				orgMap[o.ID] = o
			}
			dances, _ := client.GetDances(r.Context())
			selected := buildSelectedDanceNamesFromIDs(danceIDs, dances)
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, tmpls.adminFetchurlEdit, tmplData(r, cfg, i18n, title, AdminFetchurlEditData{
				Source:             src,
				Orgs:               orgs,
				OrgMap:             orgMap,
				Dances:             dances,
				SelectedDanceNames: selected,
				ErrorKey:           "admin_save_error",
			}))
			return
		}
		http.Redirect(w, r, "/admin/fetchurls", http.StatusSeeOther)
	}
}

func adminFetchurlDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		_ = client.DeleteFetchSource(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, "/admin/fetchurls", http.StatusSeeOther)
	}
}

func adminFetchurlRunHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		count, runErr := client.RunFetchSource(r.Context(), id, getSessionToken(r))
		if r.Header.Get("Accept") == "application/json" {
			w.Header().Set("Content-Type", "application/json")
			if runErr != nil {
				w.WriteHeader(http.StatusBadGateway)
				json.NewEncoder(w).Encode(map[string]string{"error": runErr.Error()})
				return
			}
			json.NewEncoder(w).Encode(map[string]int{"count": count})
			return
		}
		http.Redirect(w, r, "/admin/fetchurls", http.StatusSeeOther)
	}
}

func adminFetchurlBulkHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		for _, s := range r.Form["src_ids"] {
			if n, err := strconv.Atoi(s); err == nil {
				ids = append(ids, n)
			}
		}
		if len(ids) == 0 {
			http.Redirect(w, r, "/admin/fetchurls", http.StatusSeeOther)
			return
		}
		token := getSessionToken(r)
		action := r.FormValue("bulk_action")
		switch action {
		case "delete":
			if user.Role != "admin" {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			_ = client.BulkDeleteFetchSources(r.Context(), ids, token)
		case "run":
			_ = client.BulkRunFetchSources(r.Context(), ids, token)
		case "assign-org":
			if user.Role != "admin" {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			var orgID *int
			if v := r.FormValue("organization_id"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					orgID = &n
				}
			}
			_ = client.BulkAssignFetchSourceOrg(r.Context(), ids, orgID, token)
		case "add-tag":
			newTag := strings.TrimSpace(r.FormValue("new_tag"))
			if newTag != "" {
				idSet := make(map[int]bool, len(ids))
				for _, id := range ids {
					idSet[id] = true
				}
				sources, err := client.GetFetchSources(r.Context(), token)
				if err == nil {
					for _, src := range sources {
						if !idSet[src.ID] {
							continue
						}
						hasTag := false
						for _, t := range src.Tags {
							if t == newTag {
								hasTag = true
								break
							}
						}
						if !hasTag {
							newTags := append(src.Tags, newTag)
							_ = client.UpdateFetchSource(r.Context(), src.ID, src.Type, newTags, src.DanceIDs, src.OrganizationID, token)
						}
					}
				}
			}
		}
		http.Redirect(w, r, "/admin/fetchurls", http.StatusSeeOther)
	}
}

// ── Locations ─────────────────────────────────────────────────────────────────

type AdminLocationsData struct {
	Locations []Location
	OrgMap    map[int]Organization
	Orgs      []Organization
	IsAdmin   bool
}

type AdminLocationEditData struct {
	Location Location
	Orgs     []Organization
	ErrorKey string
}

func buildOrgMap(orgs []Organization) map[int]Organization {
	m := make(map[int]Organization, len(orgs))
	for _, o := range orgs {
		m[o.ID] = o
	}
	return m
}

func adminLocationsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		locs, err := client.GetLocations(r.Context())
		if err != nil {
			http.Error(w, "could not load locations", http.StatusBadGateway)
			return
		}
		sort.Slice(locs, func(i, j int) bool {
			if locs[i].Town != locs[j].Town {
				return locs[i].Town < locs[j].Town
			}
			return locs[i].Location < locs[j].Location
		})
		orgs, _ := client.GetOrganizations(r.Context())
		title := i18n.T(r, "admin_locations_title")
		renderTemplate(w, tmpls.adminLocations, tmplData(r, cfg, i18n, title, AdminLocationsData{
			Locations: locs,
			OrgMap:    buildOrgMap(orgs),
			Orgs:      orgs,
			IsAdmin:   user.Role == "admin",
		}))
	}
}

func adminLocationBulkAssignHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		var ids []int
		for _, s := range r.Form["loc_ids"] {
			if n, err := strconv.Atoi(s); err == nil {
				ids = append(ids, n)
			}
		}
		var orgID *int
		if v := r.FormValue("organization_id"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				orgID = &n
			}
		}
		if len(ids) > 0 {
			client.BulkAssignLocationOrg(r.Context(), ids, orgID, getSessionToken(r))
		}
		http.Redirect(w, r, "/admin/locations", http.StatusSeeOther)
	}
}

func adminLocationNewPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		orgs, _ := client.GetOrganizations(r.Context())
		title := i18n.T(r, "admin_new")
		renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, AdminLocationEditData{Orgs: orgs}))
	}
}

func adminLocationCreateHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var orgIDs []int
		for _, v := range r.Form["organization_ids"] {
			if n, err := strconv.Atoi(v); err == nil {
				orgIDs = append(orgIDs, n)
			}
		}
		loc := Location{
			Location:        strings.TrimSpace(r.FormValue("location")),
			ShortName:       strings.TrimSpace(r.FormValue("short_name")),
			Address:         strings.TrimSpace(r.FormValue("address")),
			Zipcode:         strings.TrimSpace(r.FormValue("zipcode")),
			Town:            strings.TrimSpace(r.FormValue("town")),
			Country:         strings.TrimSpace(r.FormValue("country")),
			CountryCode:     strings.ToUpper(strings.TrimSpace(r.FormValue("country_code"))),
			Region:          strings.TrimSpace(r.FormValue("region")),
			Latitude:        parseLatLng(r.FormValue("latitude")),
			Longitude:       parseLatLng(r.FormValue("longitude")),
			Internetsite:    strings.TrimSpace(r.FormValue("internetsite")),
			OsmID:           parseOsmID(r.FormValue("osm_id")),
			OsmType:         strings.TrimSpace(r.FormValue("osm_type")),
			OrganizationIDs: orgIDs,
		}
		token := getSessionToken(r)
		if _, err := client.CreateLocation(r.Context(), loc, token); err != nil {
			orgs, _ := client.GetOrganizations(r.Context())
			title := i18n.T(r, "admin_new")
			renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, AdminLocationEditData{
				Location: loc,
				Orgs:     orgs,
				ErrorKey: "admin_save_error",
			}))
			return
		}
		http.Redirect(w, r, "/admin/locations", http.StatusSeeOther)
	}
}

func adminLocationEditPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
		loc, err := client.GetLocation(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		orgs, _ := client.GetOrganizations(r.Context())
		title := i18n.T(r, "admin_edit")
		renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, AdminLocationEditData{
			Location: loc,
			Orgs:     orgs,
		}))
	}
}

func adminLocationSaveHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
		var orgIDs []int
		for _, v := range r.Form["organization_ids"] {
			if n, err := strconv.Atoi(v); err == nil {
				orgIDs = append(orgIDs, n)
			}
		}
		loc := Location{
			ID:              id,
			Location:        strings.TrimSpace(r.FormValue("location")),
			ShortName:       strings.TrimSpace(r.FormValue("short_name")),
			Address:         strings.TrimSpace(r.FormValue("address")),
			Zipcode:         strings.TrimSpace(r.FormValue("zipcode")),
			Town:            strings.TrimSpace(r.FormValue("town")),
			Country:         strings.TrimSpace(r.FormValue("country")),
			CountryCode:     strings.ToUpper(strings.TrimSpace(r.FormValue("country_code"))),
			Region:          strings.TrimSpace(r.FormValue("region")),
			Latitude:        parseLatLng(r.FormValue("latitude")),
			Longitude:       parseLatLng(r.FormValue("longitude")),
			Internetsite:    strings.TrimSpace(r.FormValue("internetsite")),
			OsmID:           parseOsmID(r.FormValue("osm_id")),
			OsmType:         strings.TrimSpace(r.FormValue("osm_type")),
			OrganizationIDs: orgIDs,
		}
		token := getSessionToken(r)
		if err := client.UpdateLocation(r.Context(), id, loc, token); err != nil {
			orgs, _ := client.GetOrganizations(r.Context())
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, AdminLocationEditData{
				Location: loc,
				Orgs:     orgs,
				ErrorKey: "admin_save_error",
			}))
			return
		}
		http.Redirect(w, r, "/admin/locations", http.StatusSeeOther)
	}
}

func adminLocationDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		_ = client.DeleteLocation(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, "/admin/locations", http.StatusSeeOther)
	}
}

// ── Events ────────────────────────────────────────────────────────────────────

type AdminEventsData struct {
	Events             []Event
	Organizations      []Organization
	Musicians          []Musician
	Dances             []Dance
	FilterIncludePast  bool
	FilterOrgID        int    // -1 = no org assigned
	FilterDateFrom     string
	FilterDateTo       string
	FilterMusicianID   int
	FilterType         string // "ball", "workshop", "festival"
	FilterDance        string
	FilterCreatedAfter string
	FilterSource       string
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

type AdminImportEventsData struct {
	PreviewEvents []PreviewEvent
	PreviewJSON   []string
	Error         string
	FeedURL       string
	FeedType      string
}

// ── Users & Invites ───────────────────────────────────────────────────────────

type AdminUsersData struct {
	IsAdmin        bool
	Users          []UserInfo
	Orgs           []Organization
	OrgMap         map[int]Organization
	UserOrgs       map[int][]int
	Invites        []InviteLink
	BaseURL        string
	NewInviteToken string
}

func adminUsersHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		isAdmin := su.Role == "admin"

		orgs, _ := client.GetOrganizations(r.Context())
		orgMap := make(map[int]Organization, len(orgs))
		for _, o := range orgs {
			orgMap[o.ID] = o
		}

		userOrgs := make(map[int][]int)
		for _, o := range orgs {
			members, err := client.GetOrganizationMembers(r.Context(), o.ID, token)
			if err != nil {
				continue
			}
			for _, m := range members {
				userOrgs[m.UserID] = append(userOrgs[m.UserID], o.ID)
			}
		}

		var users []UserInfo
		if isAdmin {
			users, _ = client.GetAllUsers(r.Context(), token)
		} else {
			seen := make(map[int]bool)
			for _, orgID := range userOrgs[su.ID] {
				members, _ := client.GetOrganizationMembers(r.Context(), orgID, token)
				for _, m := range members {
					if !seen[m.UserID] {
						seen[m.UserID] = true
						users = append(users, UserInfo{ID: m.UserID, Username: m.Username})
					}
				}
			}
		}

		invites, _ := client.ListInvites(r.Context(), token)
		active := make([]InviteLink, 0, len(invites))
		for _, inv := range invites {
			if inv.UsedAt == "" {
				active = append(active, inv)
			}
		}

		title := i18n.T(r, "admin_users_title")
		renderTemplate(w, tmpls.adminUsers, tmplData(r, cfg, i18n, title, AdminUsersData{
			IsAdmin:        isAdmin,
			Users:          users,
			Orgs:           orgs,
			OrgMap:         orgMap,
			UserOrgs:       userOrgs,
			Invites:        active,
			BaseURL:        cfg.publicBaseURL(),
			NewInviteToken: r.URL.Query().Get("new_invite"),
		}))
	}
}

func adminUserDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = client.DeleteUser(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func adminUserRoleHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" {
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
		_ = client.UpdateUser(r.Context(), id, map[string]string{"role": r.FormValue("role")}, getSessionToken(r))
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func adminUserOrgHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		userID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		action := r.FormValue("action")
		orgID, err := strconv.Atoi(r.FormValue("org_id"))
		if err != nil {
			http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
			return
		}
		if action == "remove" {
			_ = client.RemoveOrgMember(r.Context(), orgID, userID, token)
		} else {
			_ = client.AddOrgMember(r.Context(), orgID, userID, token)
		}
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func adminUsersBulkHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if su.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)
		action := r.FormValue("action")
		for _, idStr := range r.Form["user_ids"] {
			id, err := strconv.Atoi(idStr)
			if err != nil {
				continue
			}
			switch action {
			case "delete":
				_ = client.DeleteUser(r.Context(), id, token)
			case "org":
				orgID, err := strconv.Atoi(r.FormValue("org_id"))
				if err == nil {
					_ = client.AddOrgMember(r.Context(), orgID, id, token)
				}
			case "role":
				if role := r.FormValue("role"); role != "" {
					_ = client.UpdateUser(r.Context(), id, map[string]string{"role": role}, token)
				}
			}
		}
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func adminInviteCreateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		role := r.FormValue("role")
		if role == "" {
			role = "user"
		}
		var orgID *int
		if s := r.FormValue("org_id"); s != "" {
			if id, err := strconv.Atoi(s); err == nil {
				orgID = &id
			}
		}
		link, err := client.CreateInvite(r.Context(), role, orgID, token)
		if err != nil {
			http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/users?new_invite="+link.Token, http.StatusSeeOther)
	}
}

func adminInviteRevokeHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		invToken := r.PathValue("token")
		_ = client.RevokeInvite(r.Context(), invToken, getSessionToken(r))
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
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

func adminMusicianImageDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		_ = client.DeleteMusicianImage(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, fmt.Sprintf("/admin/musicians/%d/edit", id), http.StatusSeeOther)
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
		Location:           ev.Location,
		Town:               ev.LocationTown,
		Country:            ev.LocationCountry,
		Tags:               ev.Tags,
		OriginalDate:       isoDateStr(ev.StartTime),
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

// ── Event Templates ───────────────────────────────────────────────────────────

type AdminTemplatesData struct {
	Templates []EventTemplate
	OrgMap    map[int]Organization
}

func adminTemplatesHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		token := getSessionToken(r)
		userOrgIDs := getUserOrgIDs(r.Context(), client, su.ID, token)
		if su.Role == "admin" {
			allOrgs, _ := client.GetOrganizations(r.Context())
			userOrgIDs = make([]int, 0, len(allOrgs))
			for _, o := range allOrgs {
				userOrgIDs = append(userOrgIDs, o.ID)
			}
		}
		ts, _ := listTemplates(db, su.ID, userOrgIDs)
		orgs, _ := client.GetOrganizations(r.Context())
		orgMap := make(map[int]Organization, len(orgs))
		for _, o := range orgs {
			orgMap[o.ID] = o
		}
		title := i18n.T(r, "admin_templates_title")
		renderTemplate(w, tmpls.adminTemplates, tmplData(r, cfg, i18n, title, AdminTemplatesData{
			Templates: ts,
			OrgMap:    orgMap,
		}))
	}
}

func adminTemplateSaveHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		su, ok := requireLogin(w, r)
		if !ok {
			return
		}
		eventID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		ev, err := client.GetEvent(r.Context(), eventID)
		if err != nil {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		var orgID *int
		if v := strings.TrimSpace(r.FormValue("org_id")); v != "" {
			if n, err2 := strconv.Atoi(v); err2 == nil && n > 0 {
				orgID = &n
			}
		}
		data, _ := json.Marshal(templateDataFromEvent(ev))
		if _, err := saveTemplate(db, su.ID, orgID, name, string(data)); err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/templates", http.StatusSeeOther)
	}
}

func adminTemplateDeleteHandler(db *sql.DB) http.HandlerFunc {
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
		_ = deleteTemplate(db, id, su.ID, su.Role == "admin")
		http.Redirect(w, r, "/admin/templates", http.StatusSeeOther)
	}
}

func adminTemplateDataHandler(db *sql.DB) http.HandlerFunc {
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
		t, err := getTemplate(db, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(t.Data))
	}
}

type templateEventData struct {
	Title              string  `json:"title"`
	Description        string  `json:"description"`
	URL                string  `json:"url"`
	BookingURL         string  `json:"booking_url"`
	HasBall            bool    `json:"has_ball"`
	HasWorkshop        bool    `json:"has_workshop"`
	HasFestival        bool    `json:"has_festival"`
	WorkshopDifficulty string  `json:"workshop_difficulty"`
	OrgID              int     `json:"org_id"`
	LocID              int     `json:"loc_id"`
	PricingType        string  `json:"pricing_type"`
	PricingAmount      float64 `json:"pricing_amount"`
	PricingCurrency    string  `json:"pricing_currency"`
	PricingLines       []Price `json:"pricing_lines"`
	Tags               []string `json:"tags"`
	DanceIDs           []int   `json:"dance_ids"`
}

func templateDataFromEvent(ev Event) templateEventData {
	d := templateEventData{
		Title:              ev.Title,
		Description:        ev.Description,
		URL:                ev.URL,
		BookingURL:         ev.BookingURL,
		HasBall:            ev.HasBall,
		HasWorkshop:        ev.HasWorkshop,
		HasFestival:        ev.HasFestival,
		WorkshopDifficulty: ev.WorkshopDifficulty,
		Tags:               ev.Tags,
	}
	if ev.OrganizationID != nil {
		d.OrgID = *ev.OrganizationID
	}
	if ev.LocationID != nil {
		d.LocID = *ev.LocationID
	}
	if p := ev.Pricing; p != nil {
		d.PricingType = p.Type
		d.PricingAmount = p.Amount
		d.PricingCurrency = p.Currency
		d.PricingLines = p.Prices
	}
	return d
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
		musicianID, _ := strconv.Atoi(q.Get("musician_id"))
		dateFrom := q.Get("date_from")
		dateTo := q.Get("date_to")
		filterType := q.Get("type")
		filterDance := q.Get("dance")
		createdAfter := q.Get("created_after")
		filterSource := q.Get("source")

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
		if musicianID != 0 {
			params.Set("musician_id", strconv.Itoa(musicianID))
		}
		if createdAfter != "" {
			params.Set("created_after", createdAfter)
		}
		if filterSource != "" {
			params.Set("source", filterSource)
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
		musicians, _ := client.GetMusicians(r.Context())
		dances, _ := client.GetDances(r.Context())

		title := i18n.T(r, "admin_events_title")
		renderTemplate(w, tmpls.adminEvents, tmplData(r, cfg, i18n, title, AdminEventsData{
			Events:             events,
			Organizations:      orgs,
			Musicians:          musicians,
			Dances:             dances,
			FilterIncludePast:  includePast,
			FilterOrgID:        orgID,
			FilterDateFrom:     dateFrom,
			FilterDateTo:       dateTo,
			FilterMusicianID:   musicianID,
			FilterType:         filterType,
			FilterDance:        filterDance,
			FilterCreatedAfter: createdAfter,
			FilterSource:       filterSource,
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
				HasBall:     r.URL.Query().Get("has_ball") == "1",
				HasWorkshop: r.URL.Query().Get("has_workshop") == "1",
				HasFestival: r.URL.Query().Get("has_festival") == "1",
			}
		}

		tmpls2, _ := listTemplates(db, su.ID, getUserOrgs())

		allTags, _ := client.GetTags(r.Context())
		title := i18n.T(r, "admin_event_new_title")
		renderTemplate(w, tmpls.adminEventNew, tmplData(r, cfg, i18n, title, AdminEventNewData{
			Organizations:      bundle.Orgs,
			Locations:          bundle.Locations,
			Musicians:          bundle.Musicians,
			Dances:             bundle.Dances,
			GroupedTags:        buildGroupedTags(allTags, nil),
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
		allTags, _ := client.GetTags(r.Context())
		renderErr := func(errKey string) {
			title := i18n.T(r, "admin_event_new_title")
			renderTemplate(w, tmpls.adminEventNew, tmplData(r, cfg, i18n, title, AdminEventNewData{
				Organizations: bundle.Orgs,
				Locations:     bundle.Locations,
				Musicians:     bundle.Musicians,
				Dances:        bundle.Dances,
				GroupedTags:   buildGroupedTags(allTags, nil),
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
			Title:              strings.TrimSpace(r.FormValue("title")),
			Description:        strings.TrimSpace(r.FormValue("description")),
			StartTime:          startTime,
			EndTime:            endTime,
			HasBall:            r.FormValue("has_ball") == "on",
			HasWorkshop:        r.FormValue("has_workshop") == "on",
			HasFestival:        r.FormValue("has_festival") == "on",
			WorkshopDifficulty: r.FormValue("workshop_difficulty"),
			BookingURL:         strings.TrimSpace(r.FormValue("booking_url")),
			Tags:               tags,
			URL:                strings.TrimSpace(r.FormValue("url")),
			OrganizationID:     orgID,
			Pricing:            pricing,
			Location:           locReq,
			Dances:             danceIDs,
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
	Organizations      []Organization
	Locations          []Location
	Musicians          []Musician
	Dances             []Dance
	GroupedTags        []TagGroup
	SelectedDanceNames map[string]bool
	ErrorKey           string
	UserOrgs           []Organization
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

func adminEventEditPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
		var allTags []Tag
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); event, eventErr = client.GetEvent(r.Context(), id) }()
		go func() { defer wg.Done(); bundle = client.FetchRefBundle(r.Context()) }()
		go func() { defer wg.Done(); allTags, _ = client.GetTags(r.Context()) }()
		wg.Wait()
		if eventErr != nil {
			http.NotFound(w, r)
			return
		}
		checkedTags := make(map[string]bool, len(event.Tags))
		for _, t := range event.Tags {
			checkedTags[t] = true
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
		title := i18n.T(r, "admin_event_edit_title")
		renderTemplate(w, tmpls.adminEventEdit, tmplData(r, cfg, i18n, title, AdminEventEditData{
			Event:              event,
			Organizations:      bundle.Orgs,
			Locations:          bundle.Locations,
			Musicians:          bundle.Musicians,
			Dances:             bundle.Dances,
			GroupedTags:        buildGroupedTags(allTags, checkedTags),
			SelectedDanceNames: buildSelectedDanceNames(event),
			UserOrgs:           userOrgs,
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
		allTags, _ := client.GetTags(r.Context())
		renderErr := func(errKey string) {
			event, _ := client.GetEvent(r.Context(), id)
			title := i18n.T(r, "admin_event_edit_title")
			renderTemplate(w, tmpls.adminEventEdit, tmplData(r, cfg, i18n, title, AdminEventEditData{
				Event:              event,
				Organizations:      bundle.Orgs,
				Locations:          bundle.Locations,
				Musicians:          bundle.Musicians,
				Dances:             bundle.Dances,
				GroupedTags:        buildGroupedTags(allTags, nil),
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
			Title:              strings.TrimSpace(r.FormValue("title")),
			Description:        strings.TrimSpace(r.FormValue("description")),
			StartTime:          startTime,
			EndTime:            endTime,
			HasBall:            r.FormValue("has_ball") == "on",
			HasWorkshop:        r.FormValue("has_workshop") == "on",
			HasFestival:        r.FormValue("has_festival") == "on",
			WorkshopDifficulty: r.FormValue("workshop_difficulty"),
			BookingURL:         strings.TrimSpace(r.FormValue("booking_url")),
			IsCancelled:        r.FormValue("is_cancelled") == "on",
			Availability:       r.FormValue("availability"),
			TicketsTotal:       ticketsTotal,
			BookingEnabled:     r.FormValue("booking_enabled") == "on",
			IsPublished:        r.FormValue("is_published") == "on",
			Tags:               tags,
			URL:                strings.TrimSpace(r.FormValue("url")),
			OrganizationID:     orgID,
			Pricing:            pricing,
			Location:           locReq,
			Musicians:          musicianIDs,
			Dances:             danceIDs,
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
		if err := client.ReplaceTimetable(r.Context(), id, ttEntries, getSessionToken(r)); err != nil {
			log.Printf("replace timetable error: %v", err)
		}

		if req.IsPublished {
			go deliverUpdateToFollowers(cfg, db, client, id)
		}
		http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
	}
}

// ── Admin Dances ──────────────────────────────────────────────────────────────

type AdminDancesData struct {
	Dances   []Dance
	ErrorMsg string
}

func adminDancesHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		dances, _ := client.GetDances(r.Context())
		title := i18n.T(r, "admin_dances_title")
		renderTemplate(w, tmpls.adminDances, tmplData(r, cfg, i18n, title, AdminDancesData{Dances: dances}))
	}
}

func adminDanceCreateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		name := strings.TrimSpace(r.FormValue("name"))
		if name != "" {
			_, _ = client.CreateDance(r.Context(), name, getSessionToken(r))
		}
		http.Redirect(w, r, "/admin/dances", http.StatusSeeOther)
	}
}

func adminDanceDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		_ = client.DeleteDance(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, "/admin/dances", http.StatusSeeOther)
	}
}

// ── Admin Site Config ─────────────────────────────────────────────────────────

type AdminSiteConfigData struct {
	SiteName              string
	Contact               string
	TelegramBotToken      string
	TelegramBotName       string
	MatrixHomeserver      string
	MatrixAccessToken     string
	HeartbeatIntervalMins int
	HasLogo               bool
	HasBanner             bool
	HasFavicon            bool
	Dances                []Dance
	DefaultDanceNames     map[string]bool
	ImpressumTexts        map[string]string
	ImpressumLangs        []string
	ErrorMsg              string
	Success               bool
}

func adminSiteConfigHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		token := getSessionToken(r)
		ac, _ := client.GetAdminConfig(r.Context(), token)
		dances, _ := client.GetDances(r.Context())
		defaultDanceIDs := loadDefaultDanceIDs(db)
		defaultDanceNames := buildSelectedDanceNamesFromIDs(defaultDanceIDs, dances)
		impTexts := make(map[string]string)
		for _, lang := range impressumLangs {
			if v := getSiteSetting(db, "impressum_"+lang); v != "" {
				impTexts[lang] = v
			} else {
				impTexts[lang] = cfg.pagesContent.ImpressumText(lang)
			}
		}
		data := AdminSiteConfigData{
			SiteName:              getSiteSetting(db, "site_name"),
			Contact:               getSiteSetting(db, "contact"),
			TelegramBotToken:      ac.TelegramBotToken,
			TelegramBotName:       ac.TelegramBotName,
			MatrixHomeserver:      ac.MatrixHomeserver,
			MatrixAccessToken:     ac.MatrixAccessToken,
			HeartbeatIntervalMins: ac.HeartbeatIntervalMins,
			HasLogo:               len(findSiteAssetOnDisk(cfg.ImagesDir, "logo")) > 0,
			HasBanner:             len(findSiteAssetOnDisk(cfg.ImagesDir, "banner")) > 0,
			HasFavicon:            len(findSiteAssetOnDisk(cfg.ImagesDir, "favicon")) > 0,
			Dances:                dances,
			DefaultDanceNames:     defaultDanceNames,
			ImpressumTexts:        impTexts,
			ImpressumLangs:        impressumLangs,
			Success:               r.URL.Query().Get("saved") == "1",
		}
		renderTemplate(w, tmpls.adminSiteConfig, tmplData(r, cfg, i18n, i18n.T(r, "admin_site_config_title"), data))
	}
}

func adminSiteConfigSaveHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := getSessionToken(r)

		// Text settings
		siteName := strings.TrimSpace(r.FormValue("site_name"))
		contact := strings.TrimSpace(r.FormValue("contact"))
		_ = setSiteSetting(db, "site_name", siteName)
		_ = setSiteSetting(db, "contact", contact)
		cfg.SiteName = siteName
		cfg.ContactOverride = contact

		// Impressum per language
		if cfg.ImpressumOverride == nil {
			cfg.ImpressumOverride = make(map[string]string)
		}
		for _, lang := range impressumLangs {
			text := strings.TrimSpace(r.FormValue("impressum_" + lang))
			_ = setSiteSetting(db, "impressum_"+lang, text)
			cfg.ImpressumOverride[lang] = text
		}

		// Default dance IDs for new events
		var defaultDanceIDs []int
		for _, v := range r.MultipartForm.Value["default_dance_ids"] {
			if n, err2 := strconv.Atoi(v); err2 == nil {
				defaultDanceIDs = append(defaultDanceIDs, n)
			}
		}
		j, _ := json.Marshal(defaultDanceIDs)
		_ = setSiteSetting(db, "default_dance_ids", string(j))

		// Telegram / Matrix / heartbeat via dansal API
		heartbeatMins, _ := strconv.Atoi(r.FormValue("heartbeat_interval_mins"))
		if heartbeatMins <= 0 {
			heartbeatMins = 5
		}
		ac := AdminConfig{
			TelegramBotToken:      strings.TrimSpace(r.FormValue("telegram_bot_token")),
			TelegramBotName:       strings.TrimSpace(r.FormValue("telegram_bot_name")),
			MatrixHomeserver:      strings.TrimSpace(r.FormValue("matrix_homeserver")),
			MatrixAccessToken:     strings.TrimSpace(r.FormValue("matrix_access_token")),
			HeartbeatIntervalMins: heartbeatMins,
		}
		_ = client.PatchAdminConfig(r.Context(), token, ac)

		// Image uploads: logo/banner accept SVG/AVIF/JPG; favicon also accepts GIF
		for _, key := range []string{"logo", "banner", "favicon"} {
			f, _, err := r.FormFile(key)
			if err != nil {
				continue
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				continue
			}
			mime := detectAssetMIME(data)
			if mime == "" {
				continue
			}
			if key != "favicon" && mime == "image/gif" {
				continue
			}
			if err := saveSiteAssetToDisk(cfg.ImagesDir, key, data); err != nil {
				log.Printf("save site asset %s: %v", key, err)
			}
		}

		http.Redirect(w, r, "/admin/site-config?saved=1", http.StatusSeeOther)
	}
}

func adminSiteConfigMatrixLoginHandler(cfg *Config, tmpls *Templates, db *sql.DB, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
		homeserver := strings.TrimSpace(r.FormValue("matrix_homeserver"))
		username := strings.TrimSpace(r.FormValue("matrix_username"))
		password := r.FormValue("matrix_password")

		if err := client.MatrixLogin(r.Context(), token, homeserver, username, password); err != nil {
			ac, _ := client.GetAdminConfig(r.Context(), token)
			dances, _ := client.GetDances(r.Context())
			defaultDanceIDs := loadDefaultDanceIDs(db)
			defaultDanceNames := buildSelectedDanceNamesFromIDs(defaultDanceIDs, dances)
			impTexts := make(map[string]string)
			for _, lang := range impressumLangs {
				if v := getSiteSetting(db, "impressum_"+lang); v != "" {
					impTexts[lang] = v
				} else {
					impTexts[lang] = cfg.pagesContent.ImpressumText(lang)
				}
			}
			data := AdminSiteConfigData{
				SiteName:              getSiteSetting(db, "site_name"),
				Contact:               getSiteSetting(db, "contact"),
				TelegramBotToken:      ac.TelegramBotToken,
				TelegramBotName:       ac.TelegramBotName,
				MatrixHomeserver:      ac.MatrixHomeserver,
				MatrixAccessToken:     ac.MatrixAccessToken,
				HeartbeatIntervalMins: ac.HeartbeatIntervalMins,
				HasLogo:               len(findSiteAssetOnDisk(cfg.ImagesDir, "logo")) > 0,
				HasBanner:             len(findSiteAssetOnDisk(cfg.ImagesDir, "banner")) > 0,
				HasFavicon:            len(findSiteAssetOnDisk(cfg.ImagesDir, "favicon")) > 0,
				Dances:                dances,
				DefaultDanceNames:     defaultDanceNames,
				ImpressumTexts:        impTexts,
				ImpressumLangs:        impressumLangs,
				ErrorMsg:              err.Error(),
			}
			renderTemplate(w, tmpls.adminSiteConfig, tmplData(r, cfg, i18n, i18n.T(r, "admin_site_config_title"), data))
			return
		}
		http.Redirect(w, r, "/admin/site-config?saved=1", http.StatusSeeOther)
	}
}

var siteAssetExts = []string{".svg", ".avif", ".jpg", ".gif"}

// findSiteAssetOnDisk returns the raw bytes of key.{svg,avif,jpg,gif} from dir, or nil.
func findSiteAssetOnDisk(dir, key string) []byte {
	if dir == "" {
		return nil
	}
	for _, ext := range siteAssetExts {
		if data, err := os.ReadFile(filepath.Join(dir, key+ext)); err == nil {
			return data
		}
	}
	return nil
}

// saveSiteAssetToDisk writes data to dir/key.ext, removing stale format files.
func saveSiteAssetToDisk(dir, key string, data []byte) error {
	if dir == "" {
		return fmt.Errorf("images_dir not configured")
	}
	var ext string
	switch detectAssetMIME(data) {
	case "image/svg+xml":
		ext = ".svg"
	case "image/avif":
		ext = ".avif"
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	default:
		return fmt.Errorf("unsupported format")
	}
	for _, old := range siteAssetExts {
		if old != ext {
			os.Remove(filepath.Join(dir, key+old))
		}
	}
	return os.WriteFile(filepath.Join(dir, key+ext), data, 0o644)
}

// detectAssetMIME returns the MIME type for supported site asset formats
// (SVG, AVIF, JPEG, GIF) or "" if the data is not a recognised format.
func detectAssetMIME(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	// SVG: text-based, look for the <svg element
	s := strings.TrimSpace(string(data[:min(len(data), 512)]))
	if strings.HasPrefix(s, "<svg") || strings.HasPrefix(s, "<?xml") || strings.Contains(s, "<svg") {
		return "image/svg+xml"
	}
	// AVIF: ISO BMFF ftyp box with avif/avis brand
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		end := len(data)
		if end > 128 {
			end = 128
		}
		for i := 8; i+4 <= end; i += 4 {
			if i == 12 {
				continue // minor version, not a brand
			}
			switch string(data[i : i+4]) {
			case "avif", "avis":
				return "image/avif"
			}
		}
	}
	// JPEG: FF D8 magic
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8 {
		return "image/jpeg"
	}
	// GIF: GIF87a or GIF89a magic
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "image/gif"
	}
	return ""
}

type AdminInfoData struct {
	WebVersion   string
	WebBuildTime string
	API          DansalInfo
	OutboundIP   string
	LoadAvg      string
	Heartbeat    HeartbeatStatus
}

func adminInfoHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		token := getSessionToken(r)
		info, _ := client.GetDansalInfo(r.Context())
		heartbeat, _ := client.GetHeartbeatStatus(r.Context(), token)

		outboundIP := outboundIP()
		loadAvg := readLoadAvg()

		data := AdminInfoData{
			WebVersion:   Version,
			WebBuildTime: BuildTime,
			API:          info,
			OutboundIP:   outboundIP,
			LoadAvg:      loadAvg,
			Heartbeat:    heartbeat,
		}
		renderTemplate(w, tmpls.adminInfo, tmplData(r, cfg, i18n, "System info", data))
	}
}

func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func readLoadAvg() string {
	f, err := os.Open("/proc/loadavg")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 3 {
			return strings.Join(fields[:3], " ")
		}
	}
	return ""
}

// ── Import events ─────────────────────────────────────────────────────────────

func adminImportEventsPageHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		title := i18n.T(r, "admin_import_title")
		renderTemplate(w, tmpls.adminEventsImport, tmplData(r, cfg, i18n, title, AdminImportEventsData{}))
	}
}

func adminImportEventsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}

		renderErr := func(msg, feedURL, feedType string) {
			title := i18n.T(r, "admin_import_title")
			renderTemplate(w, tmpls.adminEventsImport, tmplData(r, cfg, i18n, title, AdminImportEventsData{
				Error:   msg,
				FeedURL: feedURL,
				FeedType: feedType,
			}))
		}

		if err := r.ParseMultipartForm(10 << 20); err != nil {
			renderErr("invalid form", "", "ical")
			return
		}

		feedURL := r.FormValue("url")
		feedType := r.FormValue("type")
		if feedType == "" {
			feedType = "ical"
		}
		orgID := r.FormValue("organization_id")

		// Build a new multipart body to forward to the API.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("type", feedType)
		if orgID != "" {
			mw.WriteField("organization_id", orgID)
		}

		if feedURL != "" {
			mw.WriteField("url", feedURL)
		} else {
			file, hdr, err := r.FormFile("file")
			if err != nil {
				renderErr(i18n.T(r, "admin_import_file_label")+": required", feedURL, feedType)
				return
			}
			defer file.Close()
			part, _ := mw.CreateFormFile("file", hdr.Filename)
			io.Copy(part, file)
		}
		mw.Close()

		token := getSessionToken(r)
		events, err := client.PreviewEvents(r.Context(), &buf, mw.FormDataContentType(), token)
		if err != nil {
			renderErr(err.Error(), feedURL, feedType)
			return
		}

		if len(events) == 0 {
			renderErr(i18n.T(r, "admin_import_none_found"), feedURL, feedType)
			return
		}

		if len(events) == 1 {
			e := events[0]
			q := url.Values{}
			q.Set("title", e.Title)
			if e.Description != "" {
				q.Set("description", e.Description)
			}
			if e.URL != "" {
				q.Set("url", e.URL)
			}
			if t, err := time.Parse(time.RFC3339, e.StartTime); err == nil {
				q.Set("date", t.Format("2006-01-02"))
				q.Set("start_time", t.Format("15:04"))
			}
			if e.EndTime != "" {
				if t, err := time.Parse(time.RFC3339, e.EndTime); err == nil {
					q.Set("end_date", t.Format("2006-01-02"))
					q.Set("end_time", t.Format("15:04"))
				}
			}
			if e.Location.Location != "" {
				q.Set("location", e.Location.Location)
			}
			if e.Location.Town != "" {
				q.Set("town", e.Location.Town)
			}
			if e.Location.Country != "" {
				q.Set("country", e.Location.Country)
			}
			if e.HasBall {
				q.Set("has_ball", "1")
			}
			if e.HasWorkshop {
				q.Set("has_workshop", "1")
			}
			if e.HasFestival {
				q.Set("has_festival", "1")
			}
			http.Redirect(w, r, "/admin/events/new?"+q.Encode(), http.StatusSeeOther)
			return
		}

		previewJSON := make([]string, len(events))
		for i, e := range events {
			b, _ := json.Marshal(e)
			previewJSON[i] = string(b)
		}
		title := i18n.T(r, "admin_import_title")
		renderTemplate(w, tmpls.adminEventsImport, tmplData(r, cfg, i18n, title, AdminImportEventsData{
			PreviewEvents: events,
			PreviewJSON:   previewJSON,
			FeedURL:       feedURL,
			FeedType:      feedType,
		}))
	}
}

func adminImportConfirmHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		createdAt := time.Now().UTC().Format(time.RFC3339)

		var selected []json.RawMessage
		for i := 0; ; i++ {
			vals := r.Form["event_"+strconv.Itoa(i)]
			if len(vals) == 0 {
				break
			}
			if len(r.Form["sel_"+strconv.Itoa(i)]) > 0 {
				selected = append(selected, json.RawMessage(vals[0]))
			}
		}

		if len(selected) > 0 {
			client.CreateEventBatch(r.Context(), selected, token)
		}

		q := url.Values{}
		q.Set("created_after", createdAt)
		q.Set("include_past", "1")
		http.Redirect(w, r, "/admin/events?"+q.Encode(), http.StatusSeeOther)
	}
}
