package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	ics "github.com/arran4/golang-ical"
)

type Organization struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	ActorName     string `json:"actor_name,omitempty"`
	Website       string `json:"website,omitempty"`
	Instagram     string `json:"instagram,omitempty"`
	Mastodon      string `json:"mastodon,omitempty"`
	Facebook      string `json:"facebook,omitempty"`
	ContactEmail  string `json:"contact_email,omitempty"`
	ContactName   string `json:"contact_name,omitempty"`
	WikidataID    string `json:"wikidata_id,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at,omitempty"`
	ImageURL      string `json:"image_url,omitempty"`
	NotesMd       string `json:"notes_md,omitempty"`
	FetchSourceID *int   `json:"fetch_source_id,omitempty"`
}

type OrganizationMember struct {
	OrganizationID int    `json:"organization_id"`
	UserID         int    `json:"user_id"`
	Email          string `json:"email,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	Role           string `json:"role,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type CreateOrganizationRequest struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ActorName    string `json:"actor_name"`
	Website      string `json:"website"`
	Instagram    string `json:"instagram"`
	Mastodon     string `json:"mastodon"`
	Facebook     string `json:"facebook"`
	ContactEmail string `json:"contact_email"`
	ContactName  string `json:"contact_name"`
	WikidataID   string `json:"wikidata_id"`
	NotesMd      string `json:"notes_md"`
}

type AddMemberRequest struct {
	UserID int `json:"user_id"`
}

// ensureOrgFromOrganizer finds or creates an organization from a vevent's ORGANIZER property.
// Prefers the CN parameter as the org name; falls back to the value with "mailto:" stripped.
// Returns nil when no usable ORGANIZER is present or on any DB error.
func ensureOrgFromOrganizer(vevent *ics.VEvent) *int {
	prop := vevent.GetProperty(ics.ComponentPropertyOrganizer)
	if prop == nil {
		return nil
	}

	name := ""
	if cn := prop.ICalParameters[string(ics.ParameterCn)]; len(cn) > 0 {
		name = strings.TrimSpace(cn[0])
	}
	if name == "" {
		name = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(prop.Value), "mailto:"))
	}
	if name == "" {
		return nil
	}

	var id int
	err := db.QueryRow("SELECT id FROM organizations WHERE name = ?", name).Scan(&id)
	if err == sql.ErrNoRows {
		err = db.QueryRow(
			"INSERT INTO organizations (name) VALUES (?) RETURNING id", name,
		).Scan(&id)
	}
	if err != nil {
		return nil
	}
	return &id
}

// ensureOrgByName finds or creates an organization by name.
// Returns nil when name is empty or on any DB error.
func ensureOrgByName(name string) *int {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	var id int
	err := db.QueryRow("SELECT id FROM organizations WHERE name = ?", name).Scan(&id)
	if err == sql.ErrNoRows {
		err = db.QueryRow("INSERT INTO organizations (name) VALUES (?) RETURNING id", name).Scan(&id)
	}
	if err != nil {
		return nil
	}
	return &id
}

// isOrgMember returns true if userID is a member of orgID.
func isOrgMember(userID, orgID int) bool {
	var n int
	db.QueryRow(
		"SELECT COUNT(*) FROM organization_members WHERE user_id = ? AND organization_id = ?",
		userID, orgID,
	).Scan(&n)
	return n > 0
}

const orgSelectCols = `id, name, COALESCE(description,''), COALESCE(actor_name,''), COALESCE(website,''), COALESCE(instagram,''), COALESCE(mastodon,''), COALESCE(facebook,''), COALESCE(contact_email,''), COALESCE(contact_name,''), COALESCE(wikidata_id,''), created_at, COALESCE(updated_at,0), COALESCE(notes_md,'')`

func scanOrg(row interface{ Scan(...any) error }) (Organization, error) {
	var o Organization
	if err := row.Scan(&o.ID, &o.Name, &o.Description, &o.ActorName, &o.Website, &o.Instagram, &o.Mastodon, &o.Facebook, &o.ContactEmail, &o.ContactName, &o.WikidataID, &o.CreatedAt, &o.UpdatedAt, &o.NotesMd); err != nil {
		return o, err
	}
	o.ImageURL = orgImageURL(o.ID)
	return o, nil
}

type orgStatRow struct {
	ID            int `json:"id"`
	EventCount    int `json:"event_count"`
	LocationCount int `json:"location_count"`
	SourceCount   int `json:"source_count"`
}

// GET /api/v1/organizations/stats
// Returns per-org event/location/source counts in a single aggregation query.
func getOrganizationStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rows, err := db.Query(`
		SELECT o.id,
			(SELECT COUNT(*) FROM events WHERE organization_id = o.id),
			(SELECT COUNT(*) FROM location_organizations WHERE organization_id = o.id),
			(SELECT COUNT(*) FROM fetch_sources WHERE organization_id = o.id)
		FROM organizations o`)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	result := []orgStatRow{}
	for rows.Next() {
		var s orgStatRow
		if err := rows.Scan(&s.ID, &s.EventCount, &s.LocationCount, &s.SourceCount); err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		result = append(result, s)
	}
	json.NewEncoder(w).Encode(result)
}

// GET /api/v1/organizations
func getOrganizations(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT " + orgSelectCols + " FROM organizations ORDER BY id")
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	orgs := []Organization{}
	for rows.Next() {
		o, err := scanOrg(rows)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		orgs = append(orgs, o)
	}
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/atom+xml") {
		writeOrgsAtom(w, r, orgs)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(orgs)
}

func writeOrgsAtom(w http.ResponseWriter, r *http.Request, orgs []Organization) {
	host := r.Host
	entries := make([]apiFeedEntry, 0, len(orgs))
	for _, o := range orgs {
		summary := o.Description
		if len(summary) > 200 {
			summary = summary[:200]
		}
		e := apiFeedEntry{
			Title:   o.Name,
			ID:      "https://" + host + "/api/v1/organizations/" + strconv.Itoa(o.ID),
			Updated: atomTime(o.UpdatedAt),
			Summary: summary,
		}
		if o.Website != "" {
			e.Links = append(e.Links, apiFeedLink{Rel: "alternate", Href: o.Website})
		}
		if o.WikidataID != "" {
			e.Links = append(e.Links, apiFeedLink{Rel: "related", Href: "https://www.wikidata.org/wiki/" + o.WikidataID})
		}
		entries = append(entries, e)
	}
	writeAtom(w, apiFeed{
		XMLNS:   "http://www.w3.org/2005/Atom",
		Title:   "Organizations",
		ID:      "https://" + r.Host + "/api/v1/organizations",
		Updated: atomTime(0),
		Entries: entries,
	})
}

// GET /api/v1/organizations/check-actor-name?name=foo[&exclude_id=123]
func checkActorName(w http.ResponseWriter, r *http.Request) {
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
	excludeID, _ := strconv.Atoi(r.URL.Query().Get("exclude_id"))
	var n int
	if excludeID > 0 {
		db.QueryRow("SELECT COUNT(*) FROM organizations WHERE actor_name=? AND id!=?", name, excludeID).Scan(&n)
	} else {
		db.QueryRow("SELECT COUNT(*) FROM organizations WHERE actor_name=?", name).Scan(&n)
	}
	if n > 0 {
		json.NewEncoder(w).Encode(map[string]any{"available": false, "reason": "taken"})
	} else {
		json.NewEncoder(w).Encode(map[string]any{"available": true})
	}
}

// POST /api/v1/organizations
func createOrganization(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden: only admins and users may create organizations", http.StatusForbidden)
		return
	}
	var req CreateOrganizationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	if req.ActorName != "" {
		if req.ActorName == "relay" {
			writeError(w, "actor_name 'relay' is reserved", http.StatusConflict)
			return
		}
		var n int
		db.QueryRow("SELECT COUNT(*) FROM organizations WHERE actor_name=?", req.ActorName).Scan(&n)
		if n > 0 {
			writeError(w, "actor_name already in use", http.StatusConflict)
			return
		}
	}
	o, err := scanOrg(db.QueryRow(
		"INSERT INTO organizations (name, description, actor_name, website, instagram, mastodon, facebook, contact_email, contact_name, wikidata_id, notes_md, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,strftime('%s','now')) RETURNING "+orgSelectCols,
		req.Name, req.Description, req.ActorName, req.Website, req.Instagram, req.Mastodon, req.Facebook, req.ContactEmail, req.ContactName, req.WikidataID, req.NotesMd,
	))
	if err != nil {
		writeError(w, "Failed to create organization", http.StatusInternalServerError)
		return
	}
	db.Exec("INSERT OR IGNORE INTO organization_members (organization_id, user_id) VALUES (?, ?)", o.ID, callerID)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(o)
}

// GET /api/v1/organizations/{id}
func getOrganization(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	o, err := scanOrg(db.QueryRow("SELECT "+orgSelectCols+" FROM organizations WHERE id = ?", id))
	if err == sql.ErrNoRows {
		writeError(w, "Organization not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var fsID int
	if db.QueryRow("SELECT id FROM fetch_sources WHERE organization_id = ? LIMIT 1", o.ID).Scan(&fsID) == nil {
		o.FetchSourceID = &fsID
	}
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/atom+xml") {
		writeOrgsAtom(w, r, []Organization{o})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(o)
}

// PUT /api/v1/organizations/{id}
// Admins may update all fields. Org members with role user may update
// description, contact_email, and social media fields only.
func updateOrganization(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	orgID, err := strconv.Atoi(id)
	if err != nil {
		writeError(w, "Invalid organization ID", http.StatusBadRequest)
		return
	}
	if callerRole != RoleAdmin && !isOrgMember(callerID, orgID) {
		writeError(w, "Forbidden: you must be a member of this organization", http.StatusForbidden)
		return
	}
	o, err := scanOrg(db.QueryRow("SELECT "+orgSelectCols+" FROM organizations WHERE id = ?", id))
	if err == sql.ErrNoRows {
		writeError(w, "Organization not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var req CreateOrganizationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if callerRole == RoleAdmin {
		if req.ActorName != "" {
			if req.ActorName == "relay" {
				writeError(w, "actor_name 'relay' is reserved", http.StatusConflict)
				return
			}
			var n int
			db.QueryRow("SELECT COUNT(*) FROM organizations WHERE actor_name=? AND id!=?", req.ActorName, id).Scan(&n)
			if n > 0 {
				writeError(w, "actor_name already in use", http.StatusConflict)
				return
			}
		}
		if req.Name != "" {
			o.Name = req.Name
		}
		o.ActorName = req.ActorName
	}
	o.Description = req.Description
	o.Website = req.Website
	o.Instagram = req.Instagram
	o.Mastodon = req.Mastodon
	o.Facebook = req.Facebook
	o.ContactEmail = req.ContactEmail
	o.ContactName = req.ContactName
	o.WikidataID = req.WikidataID
	o.NotesMd = req.NotesMd
	if _, err := db.Exec(
		"UPDATE organizations SET name=?, description=?, actor_name=?, website=?, instagram=?, mastodon=?, facebook=?, contact_email=?, contact_name=?, wikidata_id=?, notes_md=?, updated_at=strftime('%s','now') WHERE id=?",
		o.Name, o.Description, o.ActorName, o.Website, o.Instagram, o.Mastodon, o.Facebook, o.ContactEmail, o.ContactName, o.WikidataID, o.NotesMd, id,
	); err != nil {
		writeError(w, "Failed to update organization", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(o)
}

// DELETE /api/v1/organizations/{id}
func deleteOrganization(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden: only admins may delete organizations", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	result, err := db.Exec("DELETE FROM organizations WHERE id = ?", id)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Organization not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/organizations/{id}/members
func getOrganizationMembers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	id := r.PathValue("id")
	rows, err := db.Query(`
		SELECT om.organization_id, om.user_id, COALESCE(u.email,''), COALESCE(u.display_name,''), u.role, om.created_at
		FROM organization_members om
		JOIN users u ON om.user_id = u.id
		WHERE om.organization_id = ?
		ORDER BY om.created_at`, id)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	members := []OrganizationMember{}
	for rows.Next() {
		var m OrganizationMember
		if err := rows.Scan(&m.OrganizationID, &m.UserID, &m.Email, &m.DisplayName, &m.Role, &m.CreatedAt); err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		members = append(members, m)
	}
	json.NewEncoder(w).Encode(members)
}

// POST /api/v1/organizations/{id}/members
func addOrganizationMember(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden: only admins may manage organization members", http.StatusForbidden)
		return
	}
	orgID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "Invalid organization ID", http.StatusBadRequest)
		return
	}
	var req AddMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == 0 {
		writeError(w, "user_id is required", http.StatusBadRequest)
		return
	}

	var n int
	db.QueryRow("SELECT COUNT(*) FROM organizations WHERE id = ?", orgID).Scan(&n)
	if n == 0 {
		writeError(w, "Organization not found", http.StatusNotFound)
		return
	}
	var targetRole string
	db.QueryRow("SELECT role FROM users WHERE id = ?", req.UserID).Scan(&targetRole)
	if targetRole == "" {
		writeError(w, "User not found", http.StatusNotFound)
		return
	}
	if targetRole == RolePublisher {
		db.QueryRow("SELECT COUNT(*) FROM organization_members WHERE user_id = ?", req.UserID).Scan(&n)
		if n > 0 {
			writeError(w, "Publisher may only belong to one organization", http.StatusConflict)
			return
		}
	}

	if _, err := db.Exec(
		"INSERT OR IGNORE INTO organization_members (organization_id, user_id) VALUES (?, ?)",
		orgID, req.UserID,
	); err != nil {
		writeError(w, "Failed to add member", http.StatusInternalServerError)
		return
	}

	var m OrganizationMember
	db.QueryRow(
		"SELECT organization_id, user_id, created_at FROM organization_members WHERE organization_id = ? AND user_id = ?",
		orgID, req.UserID,
	).Scan(&m.OrganizationID, &m.UserID, &m.CreatedAt)

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(m)
}

// DELETE /api/v1/organizations/{id}/members/{user_id}
func removeOrganizationMember(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden: only admins may manage organization members", http.StatusForbidden)
		return
	}
	result, err := db.Exec(
		"DELETE FROM organization_members WHERE organization_id = ? AND user_id = ?",
		r.PathValue("id"), r.PathValue("user_id"),
	)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Member not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
