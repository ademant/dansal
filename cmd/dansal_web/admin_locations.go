package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// ── Locations ─────────────────────────────────────────────────────────────────

type AdminLocationsData struct {
	Locations   []Location
	OrgMap      map[int]Organization
	Orgs        []Organization
	IsAdmin     bool
	EditableIDs map[int]bool
	UserOrgs    []Organization
	EventCounts map[int]int
}

type AdminLocationEditData struct {
	Location Location
	UserOrgs []Organization
	ReadOnly bool
	ErrorKey string
}

func parseAliases(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
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
		token := getSessionToken(r)
		eventCounts, _ := client.GetLocationEventCounts(r.Context(), token)
		isAdmin := user.Role == "admin"
		var editableIDs map[int]bool
		var userOrgs []Organization
		if !isAdmin {
			userOrgIDs := getUserOrgIDsFromOrgs(r.Context(), client, user.ID, token, orgs)
			userOrgSet := map[int]bool{}
			for _, id := range userOrgIDs {
				userOrgSet[id] = true
			}
			editableIDs = map[int]bool{}
			for _, loc := range locs {
				for _, oid := range loc.OrganizationIDs {
					if userOrgSet[oid] {
						editableIDs[loc.ID] = true
						break
					}
				}
			}
			for _, o := range orgs {
				if userOrgSet[o.ID] {
					userOrgs = append(userOrgs, o)
				}
			}
		}
		title := i18n.T(r, "admin_locations_title")
		renderTemplate(w, tmpls.adminLocations, tmplData(r, cfg, i18n, title, AdminLocationsData{
			Locations:   locs,
			OrgMap:      buildOrgMap(orgs),
			Orgs:        orgs,
			IsAdmin:     isAdmin,
			EditableIDs: editableIDs,
			UserOrgs:    userOrgs,
			EventCounts: eventCounts,
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
		ids := parseFormIDs(r.Form, "loc_ids")
		if len(ids) > 0 {
			client.BulkAssignLocationOrg(r.Context(), ids, parseFormOptionalInt(r.Form, "organization_id"), getSessionToken(r))
		}
		http.Redirect(w, r, "/admin/locations", http.StatusSeeOther)
	}
}

func adminLocationNewPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		var userOrgs []Organization
		if user.Role != "admin" {
			token := getSessionToken(r)
			orgs, _ := client.GetOrganizations(r.Context())
			userOrgIDs := getUserOrgIDsFromOrgs(r.Context(), client, user.ID, token, orgs)
			userOrgSet := map[int]bool{}
			for _, id := range userOrgIDs {
				userOrgSet[id] = true
			}
			for _, o := range orgs {
				if userOrgSet[o.ID] {
					userOrgs = append(userOrgs, o)
				}
			}
		}
		title := i18n.T(r, "admin_new")
		renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, AdminLocationEditData{UserOrgs: userOrgs}))
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
		loc := Location{
			Location:     strings.TrimSpace(r.FormValue("location")),
			ShortName:    strings.TrimSpace(r.FormValue("short_name")),
			Address:      strings.TrimSpace(r.FormValue("address")),
			Zipcode:      strings.TrimSpace(r.FormValue("zipcode")),
			Town:         strings.TrimSpace(r.FormValue("town")),
			Country:      strings.TrimSpace(r.FormValue("country")),
			CountryCode:  strings.ToUpper(strings.TrimSpace(r.FormValue("country_code"))),
			Region:       strings.TrimSpace(r.FormValue("region")),
			Latitude:     parseLatLng(r.FormValue("latitude")),
			Longitude:    parseLatLng(r.FormValue("longitude")),
			Internetsite: strings.TrimSpace(r.FormValue("internetsite")),
			OsmID:        parseOsmID(r.FormValue("osm_id")),
			OsmType:      strings.TrimSpace(r.FormValue("osm_type")),
			Aliases:      parseAliases(r.FormValue("aliases")),
			NoStreetShoes: r.FormValue("no_street_shoes") == "1",
		}
		token := getSessionToken(r)
		created, err := client.CreateLocation(r.Context(), loc, token)
		if err != nil {
			title := i18n.T(r, "admin_new")
			renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, AdminLocationEditData{
				Location: loc,
				ErrorKey: "admin_save_error",
			}))
			return
		}
		if orgIDStr := r.FormValue("organization_id"); orgIDStr != "" {
			if orgID, err2 := strconv.Atoi(orgIDStr); err2 == nil && orgID > 0 {
				_ = client.BulkAssignLocationOrg(r.Context(), []int{created.ID}, &orgID, token)
			}
		}
		http.Redirect(w, r, "/admin/locations", http.StatusSeeOther)
	}
}

func adminLocationEditPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
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
		loc, err := client.GetLocation(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		readOnly := false
		if user.Role != "admin" {
			userOrgIDs := getUserOrgIDs(r.Context(), client, user.ID, getSessionToken(r))
			userOrgSet := map[int]bool{}
			for _, uid := range userOrgIDs {
				userOrgSet[uid] = true
			}
			editable := false
			for _, oid := range loc.OrganizationIDs {
				if userOrgSet[oid] {
					editable = true
					break
				}
			}
			readOnly = !editable
		}
		title := i18n.T(r, "admin_edit")
		renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, AdminLocationEditData{
			Location: loc,
			ReadOnly: readOnly,
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
		// Fetch existing location to preserve its org assignments.
		existing, err := client.GetLocation(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
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
			OrganizationIDs: existing.OrganizationIDs,
			NotesMd:         strings.TrimSpace(r.FormValue("notes_md")),
			Attributes:      locationAttrsFromForm(r),
			Parking:         r.FormValue("parking"),
			FloorCondition:  r.FormValue("floor_condition"),
			NoStreetShoes:   r.FormValue("no_street_shoes") == "1",
			Aliases:         parseAliases(r.FormValue("aliases")),
		}
		token := getSessionToken(r)
		if err := client.UpdateLocation(r.Context(), id, loc, token); err != nil {
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, AdminLocationEditData{
				Location: loc,
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

// adminLocationMergeHandler merges two or more selected locations into one.
// The newest location (by created_at) is the base; missing fields are filled
// from the others. Events and org assignments are transferred; remaining
// locations are deleted.
func adminLocationMergeHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		ids := parseFormIDs(r.Form, "loc_ids")
		if len(ids) < 2 {
			http.Redirect(w, r, "/admin/locations", http.StatusSeeOther)
			return
		}
		ctx := r.Context()
		token := getSessionToken(r)

		var locs []Location
		for _, id := range ids {
			if loc, err := client.GetLocation(ctx, id); err == nil {
				locs = append(locs, loc)
			}
		}
		if len(locs) < 2 {
			http.Redirect(w, r, "/admin/locations", http.StatusSeeOther)
			return
		}

		// Sort oldest-to-newest: lowest ID is the canonical survivor because
		// external subscribers (iCal, etc.) have that ID bookmarked.
		sort.Slice(locs, func(i, j int) bool {
			if locs[i].CreatedAt != locs[j].CreatedAt {
				return locs[i].CreatedAt < locs[j].CreatedAt
			}
			return locs[i].ID < locs[j].ID
		})
		// Start with the oldest location's data, then walk through the rest
		// oldest-to-newest and overwrite each field with the newer non-empty
		// value. This means the most-recently-set value wins for every field,
		// whether it was entered by a user or imported by fetchurl — while the
		// surviving record always keeps the lowest (canonical) ID.
		base := locs[0]
		orgSet := make(map[int]bool)
		for _, oid := range base.OrganizationIDs {
			orgSet[oid] = true
		}
		aliasSet := make(map[string]bool)
		for _, a := range base.Aliases {
			aliasSet[a] = true
		}
		for _, l := range locs[1:] {
			if l.Location != "" {
				base.Location = l.Location
			}
			if l.ShortName != "" {
				base.ShortName = l.ShortName
			}
			if l.Address != "" {
				base.Address = l.Address
			}
			if l.Zipcode != "" {
				base.Zipcode = l.Zipcode
			}
			if l.Town != "" {
				base.Town = l.Town
			}
			if l.Country != "" {
				base.Country = l.Country
			}
			if l.CountryCode != "" {
				base.CountryCode = l.CountryCode
			}
			if l.Region != "" {
				base.Region = l.Region
			}
			if l.Latitude != nil {
				base.Latitude = l.Latitude
			}
			if l.Longitude != nil {
				base.Longitude = l.Longitude
			}
			if l.Internetsite != "" {
				base.Internetsite = l.Internetsite
			}
			if l.OsmID != nil {
				base.OsmID = l.OsmID
			}
			if l.OsmType != "" {
				base.OsmType = l.OsmType
			}
			if l.NotesMd != "" {
				base.NotesMd = l.NotesMd
			}
			if l.Parking != "" {
				base.Parking = l.Parking
			}
			if l.FloorCondition != "" {
				base.FloorCondition = l.FloorCondition
			}
			// Merge attribute maps: newer value for each key wins.
			for k, v := range l.Attributes {
				if base.Attributes == nil {
					base.Attributes = make(map[string]bool)
				}
				base.Attributes[k] = v
			}
			for _, oid := range l.OrganizationIDs {
				orgSet[oid] = true
			}
			// Collect old names as aliases so feed deduplication still works.
			if l.Location != base.Location {
				aliasSet[l.Location] = true
			}
			for _, a := range l.Aliases {
				aliasSet[a] = true
			}
		}
		mergedOrgs := make([]int, 0, len(orgSet))
		for oid := range orgSet {
			mergedOrgs = append(mergedOrgs, oid)
		}
		base.OrganizationIDs = mergedOrgs
		mergedAliases := make([]string, 0, len(aliasSet))
		for a := range aliasSet {
			if a != base.Location {
				mergedAliases = append(mergedAliases, a)
			}
		}
		base.Aliases = mergedAliases

		// Update base location (applies merged fields + org assignments).
		_ = client.UpdateLocation(ctx, base.ID, base, token)

		// Delete dropped locations. MergeLocation passes ?reassign_to=base.ID so
		// the API reassigns events and event_locations before deleting, satisfying
		// the FK constraint on events.location_id without touching the wrong DB.
		for _, l := range locs[1:] {
			_ = client.MergeLocation(ctx, l.ID, base.ID, token)
		}

		client.invalidateLocations()
		client.invalidateEvents()
		http.Redirect(w, r, "/admin/locations", http.StatusSeeOther)
	}
}

func adminLocationAssignOrgHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		orgID, err := strconv.Atoi(r.FormValue("org_id"))
		if err != nil || orgID == 0 {
			http.Redirect(w, r, "/admin/locations", http.StatusSeeOther)
			return
		}
		_ = client.BulkAssignLocationOrg(r.Context(), []int{id}, &orgID, getSessionToken(r))
		http.Redirect(w, r, "/admin/locations", http.StatusSeeOther)
	}
}
