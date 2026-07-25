package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	ReturnURL   string
}

type AdminLocationEditData struct {
	Location      Location
	Parent        *Location // set when Location.ParentID is set — the building this room belongs to
	UserOrgs      []Organization
	AssignedOrgs  []Organization
	AvailableOrgs []Organization
	ReadOnly      bool
	ErrorKey      string
	ConflictID    int // set when API returns 409: location with same OSM ID already exists
	ReturnURL     string
	From          string
}

// safeLocationsReturnURL validates that raw is a same-site path under
// /admin/locations, to avoid open-redirect via the "return" parameter.
// Falls back to /admin/locations for anything else (empty, absolute URL,
// protocol-relative URL, or unrelated path).
func safeLocationsReturnURL(raw string) string {
	if raw == "" || raw[0] != '/' || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/\\") {
		return "/admin/locations"
	}
	if raw != "/admin/locations" && !strings.HasPrefix(raw, "/admin/locations/") && !strings.HasPrefix(raw, "/admin/locations?") {
		return "/admin/locations"
	}
	return raw
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
			userOrgIDs := getUserOrgIDs(r.Context(), client, user.ID, token)
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
			ReturnURL:   r.URL.RequestURI(),
		}))
	}
}

func adminLocationMaintenanceHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
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
		title := i18n.T(r, "admin_loc_maintenance_title")
		renderTemplate(w, tmpls.adminLocationsMaintenance, tmplData(r, cfg, i18n, title, AdminLocationsData{
			Locations:   locs,
			OrgMap:      buildOrgMap(orgs),
			Orgs:        orgs,
			IsAdmin:     true,
			EventCounts: eventCounts,
			ReturnURL:   r.URL.RequestURI(),
		}))
	}
}

func adminLocationUpdateJSONHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if user.Role != "admin" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"forbidden"}`))
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid id"}`))
			return
		}
		existing, err := client.GetLocation(r.Context(), id)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not found"}`))
			return
		}
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"bad request"}`))
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
			OsmID:           existing.OsmID,
			OsmType:         existing.OsmType,
			OrganizationIDs: existing.OrganizationIDs,
			NotesMd:         strings.TrimSpace(r.FormValue("notes_md")),
			Attributes:      existing.Attributes,
			Parking:         existing.Parking,
			FloorCondition:  existing.FloorCondition,
			NoStreetShoes:   existing.NoStreetShoes,
			Aliases:         existing.Aliases,
			Capacity:        existing.Capacity,
			SizeSqm:         existing.SizeSqm,
		}
		if err := validateURLDomain(r.Context(), loc.Internetsite); err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			b, _ := json.Marshal(map[string]string{"error": err.Error()})
			w.Write(b)
			return
		}
		token := getSessionToken(r)
		if err := client.UpdateLocation(r.Context(), id, loc, token); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			b, _ := json.Marshal(map[string]string{"error": err.Error()})
			w.Write(b)
			return
		}
		w.Write([]byte(`{"ok":true}`))
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
		http.Redirect(w, r, safeReferer(r, "/admin/locations"), http.StatusSeeOther)
	}
}

// newLocationUserOrgs returns the orgs selectable when creating a location:
// all orgs for admin, the caller's own orgs for everyone else.
func newLocationUserOrgs(r *http.Request, user *SessionUser, client *DansalClient) []Organization {
	allOrgs, _ := client.GetOrganizations(r.Context())
	if user.Role == "admin" {
		return allOrgs
	}
	token := getSessionToken(r)
	userOrgIDs := getUserOrgIDs(r.Context(), client, user.ID, token)
	set := make(map[int]bool, len(userOrgIDs))
	for _, id := range userOrgIDs {
		set[id] = true
	}
	var out []Organization
	for _, o := range allOrgs {
		if set[o.ID] {
			out = append(out, o)
		}
	}
	return out
}

func adminLocationNewPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		title := i18n.T(r, "admin_new")
		renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, AdminLocationEditData{
			UserOrgs: newLocationUserOrgs(r, user, client),
		}))
	}
}

func adminLocationCreateHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		loc := Location{
			Location:      strings.TrimSpace(r.FormValue("location")),
			ShortName:     strings.TrimSpace(r.FormValue("short_name")),
			Address:       strings.TrimSpace(r.FormValue("address")),
			Zipcode:       strings.TrimSpace(r.FormValue("zipcode")),
			Town:          strings.TrimSpace(r.FormValue("town")),
			Country:       strings.TrimSpace(r.FormValue("country")),
			CountryCode:   strings.ToUpper(strings.TrimSpace(r.FormValue("country_code"))),
			Region:        strings.TrimSpace(r.FormValue("region")),
			Latitude:      parseLatLng(r.FormValue("latitude")),
			Longitude:     parseLatLng(r.FormValue("longitude")),
			Internetsite:  strings.TrimSpace(r.FormValue("internetsite")),
			OsmID:         parseOsmID(r.FormValue("osm_id")),
			OsmType:       strings.TrimSpace(r.FormValue("osm_type")),
			Aliases:       parseAliases(r.FormValue("aliases")),
			NoStreetShoes: r.FormValue("no_street_shoes") == "1",
			Capacity:      parseFormOptionalInt(r.Form, "capacity"),
			SizeSqm:       parseFormOptionalInt(r.Form, "size_sqm"),
		}
		// Parse multi-value organization_ids checkboxes. Non-admin callers must
		// include these so the API can authorize the create request.
		for _, idStr := range r.Form["organization_ids"] {
			if orgID, err2 := strconv.Atoi(idStr); err2 == nil && orgID > 0 {
				loc.OrganizationIDs = append(loc.OrganizationIDs, orgID)
			}
		}
		if err := validateURLDomain(r.Context(), loc.Internetsite); err != nil {
			title := i18n.T(r, "admin_new")
			renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, AdminLocationEditData{
				Location: loc,
				ErrorKey: "url_domain_not_found",
				UserOrgs: newLocationUserOrgs(r, user, client),
			}))
			return
		}
		token := getSessionToken(r)
		_, err := client.CreateLocation(r.Context(), loc, token)
		if err != nil {
			title := i18n.T(r, "admin_new")
			data := AdminLocationEditData{
				Location: loc,
				UserOrgs: newLocationUserOrgs(r, user, client),
				ErrorKey: "admin_save_error",
			}
			var conflictErr *LocationConflictError
			if errors.As(err, &conflictErr) {
				data.ErrorKey = ""
				data.ConflictID = conflictErr.ExistingID
			}
			renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, data))
			return
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
		assignedSet := map[int]bool{}
		for _, oid := range loc.OrganizationIDs {
			assignedSet[oid] = true
		}
		allOrgs, _ := client.GetOrganizations(r.Context())
		var assignedOrgs, availableOrgs []Organization
		for _, o := range allOrgs {
			if assignedSet[o.ID] {
				assignedOrgs = append(assignedOrgs, o)
			} else {
				availableOrgs = append(availableOrgs, o)
			}
		}
		sort.Slice(assignedOrgs, func(i, j int) bool { return assignedOrgs[i].Name < assignedOrgs[j].Name })
		sort.Slice(availableOrgs, func(i, j int) bool { return availableOrgs[i].Name < availableOrgs[j].Name })

		var parent *Location
		if loc.ParentID != nil {
			if p, err := client.GetLocation(r.Context(), *loc.ParentID); err == nil {
				parent = &p
			}
		}

		title := i18n.T(r, "admin_edit")
		renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, AdminLocationEditData{
			Location:      loc,
			Parent:        parent,
			ReadOnly:      readOnly,
			ReturnURL:     safeLocationsReturnURL(r.URL.Query().Get("return")),
			From:          safeReturnPath(r.URL.Query().Get("from")),
			AssignedOrgs:  assignedOrgs,
			AvailableOrgs: availableOrgs,
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
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
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
			OrganizationIDs: existing.OrganizationIDs,
			NotesMd:         strings.TrimSpace(r.FormValue("notes_md")),
			Attributes:      locationAttrsFromForm(r),
			Parking:         r.FormValue("parking"),
			FloorCondition:  r.FormValue("floor_condition"),
			NoStreetShoes:   r.FormValue("no_street_shoes") == "1",
			Aliases:         existing.Aliases,
			Capacity:        parseFormOptionalInt(r.Form, "capacity"),
			SizeSqm:         parseFormOptionalInt(r.Form, "size_sqm"),
		}
		returnURL := safeLocationsReturnURL(r.FormValue("return"))
		from := safeReturnPath(r.FormValue("from"))
		if err := validateURLDomain(r.Context(), loc.Internetsite); err != nil {
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, AdminLocationEditData{
				Location:  loc,
				ErrorKey:  "url_domain_not_found",
				ReturnURL: returnURL,
				From:      from,
			}))
			return
		}
		token := getSessionToken(r)
		if err := client.UpdateLocation(r.Context(), id, loc, token); err != nil {
			title := i18n.T(r, "admin_edit")
			data := AdminLocationEditData{
				Location:  loc,
				ErrorKey:  "admin_save_error",
				ReturnURL: returnURL,
				From:      from,
			}
			var conflictErr *LocationConflictError
			if errors.As(err, &conflictErr) {
				data.ErrorKey = ""
				data.ConflictID = conflictErr.ExistingID
			}
			renderTemplate(w, tmpls.adminLocationEdit, tmplData(r, cfg, i18n, title, data))
			return
		}
		if loc.ParentID == nil {
			if r.FormValue("remove_site_plan") == "1" {
				_ = client.DeleteLocationSitePlan(r.Context(), id, token)
			} else if file, header, ferr := r.FormFile("site_plan"); ferr == nil {
				data, _ := io.ReadAll(file)
				file.Close()
				if uerr := client.UploadLocationSitePlan(r.Context(), id, data, header.Filename, token); uerr != nil {
					log.Printf("upload location site plan error: %v", uerr)
				}
			}
		}
		target := returnURL
		if from != "" {
			target = from
		}
		if p := safeReturnPath(target); p != "" {
			target = p
		} else {
			target = "/admin/locations"
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
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
		target := safeLocationsReturnURL(r.FormValue("return"))
		if p := safeReturnPath(target); p != "" {
			target = p
		} else {
			target = "/admin/locations"
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
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

		// Survivor is always the location with the lowest ID so external
		// subscribers (iCal feeds, bookmarked URLs) keep their reference stable.
		sort.Slice(locs, func(i, j int) bool { return locs[i].ID < locs[j].ID })
		// Base keeps all its existing fields. Values from the other locations
		// only fill in fields that are blank on the base — never overwrite.
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
			if base.Location == "" {
				base.Location = l.Location
			}
			if base.ShortName == "" {
				base.ShortName = l.ShortName
			}
			if base.Address == "" {
				base.Address = l.Address
			}
			if base.Zipcode == "" {
				base.Zipcode = l.Zipcode
			}
			if base.Town == "" {
				base.Town = l.Town
			}
			if base.Country == "" {
				base.Country = l.Country
			}
			if base.CountryCode == "" {
				base.CountryCode = l.CountryCode
			}
			if base.Region == "" {
				base.Region = l.Region
			}
			if base.Latitude == nil {
				base.Latitude = l.Latitude
			}
			if base.Longitude == nil {
				base.Longitude = l.Longitude
			}
			if base.Internetsite == "" {
				base.Internetsite = l.Internetsite
			}
			if base.OsmID == nil {
				base.OsmID = l.OsmID
			}
			if base.OsmType == "" {
				base.OsmType = l.OsmType
			}
			if base.NotesMd == "" {
				base.NotesMd = l.NotesMd
			}
			if base.Parking == "" {
				base.Parking = l.Parking
			}
			if base.FloorCondition == "" {
				base.FloorCondition = l.FloorCondition
			}
			// Attribute map: base value wins per key; only add keys base lacks.
			for k, v := range l.Attributes {
				if base.Attributes == nil {
					base.Attributes = make(map[string]bool)
				}
				if _, exists := base.Attributes[k]; !exists {
					base.Attributes[k] = v
				}
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
		http.Redirect(w, r, safeReferer(r, "/admin/locations"), http.StatusSeeOther)
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
			http.Redirect(w, r, fmt.Sprintf("/admin/locations/%d/edit", id), http.StatusSeeOther)
			return
		}
		token := getSessionToken(r)
		if r.FormValue("action") == "remove" {
			_ = client.UnassignLocationOrg(r.Context(), id, orgID, token)
		} else {
			_ = client.BulkAssignLocationOrg(r.Context(), []int{id}, &orgID, token)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/locations/%d/edit", id), http.StatusSeeOther)
	}
}

func adminLocationRoomCreateHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
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
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			http.Redirect(w, r, fmt.Sprintf("/admin/locations/%d/edit", id), http.StatusSeeOther)
			return
		}
		floorCondition := r.FormValue("floor_condition")
		token := getSessionToken(r)
		capacity := parseFormOptionalInt(r.Form, "capacity")
		sizeSqm := parseFormOptionalInt(r.Form, "size_sqm")
		_, _ = client.CreateLocationChild(r.Context(), id, name, floorCondition, capacity, sizeSqm, token)
		client.invalidateLocations()
		http.Redirect(w, r, fmt.Sprintf("/admin/locations/%d/edit", id), http.StatusSeeOther)
	}
}

func adminLocationRoomDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		locID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		roomID, err := strconv.Atoi(r.PathValue("room_id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		token := getSessionToken(r)
		_ = client.DeleteLocationChild(r.Context(), roomID, token)
		client.invalidateLocations()
		http.Redirect(w, r, fmt.Sprintf("/admin/locations/%d/edit", locID), http.StatusSeeOther)
	}
}

// POST /admin/locations/{room_id}/plan-position — saves a room's dragged
// position on its building's site-plan image (#877). Called via fetch() from
// the drag-and-drop JS on the building's edit page, not a full page submit.
func adminLocationPlanPositionHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		roomID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid id"}`))
			return
		}
		var req struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.X < 0 || req.X > 1 || req.Y < 0 || req.Y > 1 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"x/y must be between 0 and 1"}`))
			return
		}
		token := getSessionToken(r)
		if err := client.UpdateLocationPlanPosition(r.Context(), roomID, req.X, req.Y, token); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			b, _ := json.Marshal(map[string]string{"error": err.Error()})
			w.Write(b)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}
}
