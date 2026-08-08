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
	ID             int        `json:"id"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	ActorName      string     `json:"actor_name,omitempty"`
	Website        string     `json:"website,omitempty"`
	Instagram      string     `json:"instagram,omitempty"`
	Mastodon       string     `json:"mastodon,omitempty"`
	Facebook       string     `json:"facebook,omitempty"`
	ContactEmail   string     `json:"contact_email,omitempty"`
	ContactName    string     `json:"contact_name,omitempty"`
	WikidataID     string     `json:"wikidata_id,omitempty"`
	CreatedAt      string     `json:"created_at"`
	UpdatedAt      int64      `json:"updated_at,omitempty"`
	UpdatedBy      string     `json:"updated_by,omitempty"`
	ImageURL       string     `json:"image_url,omitempty"`
	ImageMediaType string     `json:"image_media_type,omitempty"`
	AvatarURL      string     `json:"avatar_url,omitempty"`
	NotesMd        string     `json:"notes_md,omitempty"`
	FetchSourceID  *int       `json:"fetch_source_id,omitempty"`
	ChatLinks      []ChatLink `json:"chat_links,omitempty"`

	FutureEventCount int      `json:"future_event_count,omitempty"`
	PastEventCount   int      `json:"past_event_count,omitempty"`
	Latitude         *float64 `json:"latitude,omitempty"`
	Longitude        *float64 `json:"longitude,omitempty"`
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
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	ActorName    string     `json:"actor_name"`
	Website      string     `json:"website"`
	Instagram    string     `json:"instagram"`
	Mastodon     string     `json:"mastodon"`
	Facebook     string     `json:"facebook"`
	ContactEmail string     `json:"contact_email"`
	ContactName  string     `json:"contact_name"`
	WikidataID   string     `json:"wikidata_id"`
	NotesMd      string     `json:"notes_md"`
	ChatLinks    []ChatLink `json:"chat_links"`
}

type AddMemberRequest struct {
	UserID int `json:"user_id"`
}

// OrganizationMergePatchRequest is the body accepted by PATCH
// /api/v1/organizations/{id} (Content-Type: application/merge-patch+json —
// RFC 7396). Every field is a pointer: an omitted key leaves the existing
// value unchanged; a present key sets it (an explicit "" clears a field).
// As with PUT, name/actor_name may only be changed by admins.
type OrganizationMergePatchRequest struct {
	Name         *string     `json:"name,omitempty"`
	Description  *string     `json:"description,omitempty"`
	ActorName    *string     `json:"actor_name,omitempty"`
	Website      *string     `json:"website,omitempty"`
	Instagram    *string     `json:"instagram,omitempty"`
	Mastodon     *string     `json:"mastodon,omitempty"`
	Facebook     *string     `json:"facebook,omitempty"`
	ContactEmail *string     `json:"contact_email,omitempty"`
	ContactName  *string     `json:"contact_name,omitempty"`
	WikidataID   *string     `json:"wikidata_id,omitempty"`
	NotesMd      *string     `json:"notes_md,omitempty"`
	ChatLinks    *[]ChatLink `json:"chat_links,omitempty"`
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

const orgSelectCols = `id, name, COALESCE(description,''), COALESCE(actor_name,''), COALESCE(website,''), COALESCE(instagram,''), COALESCE(mastodon,''), COALESCE(facebook,''), COALESCE(contact_email,''), COALESCE(contact_name,''), COALESCE(wikidata_id,''), created_at, COALESCE(updated_at,0), COALESCE(notes_md,''), COALESCE(updated_by,''), COALESCE(chat_links,'')`

// scanOrg scans an orgSelectCols row into an Organization. Extra destination
// pointers (e.g. for appended event-count/location columns) can be passed via extra.
func scanOrg(row interface{ Scan(...any) error }, extra ...any) (Organization, error) {
	var o Organization
	var chatLinksJSON string
	dest := []any{&o.ID, &o.Name, &o.Description, &o.ActorName, &o.Website, &o.Instagram, &o.Mastodon, &o.Facebook, &o.ContactEmail, &o.ContactName, &o.WikidataID, &o.CreatedAt, &o.UpdatedAt, &o.NotesMd, &o.UpdatedBy, &chatLinksJSON}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return o, err
	}
	if chatLinksJSON != "" {
		json.Unmarshal([]byte(chatLinksJSON), &o.ChatLinks)
	}
	o.ImageURL = orgImageURL(o.ID)
	o.ImageMediaType = orgImageMediaType(o.ID)
	o.AvatarURL = orgAvatars.url(o.ID)
	return o, nil
}

type orgStatRow struct {
	ID              int `json:"id"`
	EventCount      int `json:"event_count"`
	LocationCount   int `json:"location_count"`
	SourceCount     int `json:"source_count"`
	BoardEntryCount int `json:"board_entry_count"`
}

// GET /api/v1/organizations/stats
// Returns per-org event/location/source/board-entry counts in a single aggregation query.
func getOrganizationStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rows, err := db.Query(`
		SELECT o.id,
			(SELECT COUNT(*) FROM events WHERE organization_id = o.id),
			(SELECT COUNT(*) FROM location_organizations WHERE organization_id = o.id),
			(SELECT COUNT(*) FROM fetch_sources WHERE organization_id = o.id),
			(SELECT COUNT(*) FROM contact_posts cp JOIN events e ON e.id = cp.event_id WHERE e.organization_id = o.id)
		FROM organizations o`)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()
	result := []orgStatRow{}
	for rows.Next() {
		var s orgStatRow
		if err := rows.Scan(&s.ID, &s.EventCount, &s.LocationCount, &s.SourceCount, &s.BoardEntryCount); err != nil {
			writeInternalError(w, err)
			return
		}
		result = append(result, s)
	}
	json.NewEncoder(w).Encode(result)
}

// GET /api/v1/organizations
func getOrganizations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	withCounts := q.Get("with_event_counts") == "true"
	withLocation := q.Get("with_location") == "true"

	query := "SELECT " + orgSelectCols
	if withCounts {
		query += `, COALESCE(ec.future_count,0), COALESCE(ec.past_count,0)`
	}
	if withLocation {
		query += `, loc.avg_lat, loc.avg_lng`
	}
	query += ` FROM organizations`
	if withCounts {
		query += ` LEFT JOIN (
			SELECT organization_id,
				SUM(CASE WHEN start_time > strftime('%s','now') AND is_published=1 THEN 1 ELSE 0 END) AS future_count,
				SUM(CASE WHEN start_time <= strftime('%s','now') AND is_published=1 THEN 1 ELSE 0 END) AS past_count
			FROM events WHERE organization_id IS NOT NULL GROUP BY organization_id
		) ec ON ec.organization_id = organizations.id`
	}
	if withLocation {
		query += ` LEFT JOIN (
			SELECT lo.organization_id, AVG(l.latitude) AS avg_lat, AVG(l.longitude) AS avg_lng
			FROM location_organizations lo JOIN locations l ON l.id = lo.location_id
			WHERE l.latitude IS NOT NULL AND l.longitude IS NOT NULL
			GROUP BY lo.organization_id
		) loc ON loc.organization_id = organizations.id`
	}
	var args []any
	where := false
	addWhere := newWhereAppender(&query, &where, &args)
	if name := q.Get("name"); name != "" {
		addWhere(`LOWER(organizations.name) LIKE LOWER(?) ESCAPE '\'`, "%"+escapeLike(name)+"%")
	}
	applyListPagination(r, "organizations.name ASC", &query, &args)

	rows, err := db.Query(query, args...)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()
	orgs := []Organization{}
	for rows.Next() {
		var futureCount, pastCount int
		var lat, lng *float64
		var extra []any
		if withCounts {
			extra = append(extra, &futureCount, &pastCount)
		}
		if withLocation {
			extra = append(extra, &lat, &lng)
		}
		o, err := scanOrg(rows, extra...)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		if withCounts {
			o.FutureEventCount = futureCount
			o.PastEventCount = pastCount
		}
		if withLocation {
			o.Latitude = lat
			o.Longitude = lng
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
		summary := truncateUTF8(o.Description, 200)
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
	if !decodeJSONBody(w, r, &req) {
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
	chatLinksJSON, _ := json.Marshal(filterChatLinks(req.ChatLinks))
	o, err := scanOrg(db.QueryRow(
		"INSERT INTO organizations (name, description, actor_name, website, instagram, mastodon, facebook, contact_email, contact_name, wikidata_id, notes_md, chat_links, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,strftime('%s','now')) RETURNING "+orgSelectCols,
		req.Name, req.Description, req.ActorName, req.Website, req.Instagram, req.Mastodon, req.Facebook, req.ContactEmail, req.ContactName, req.WikidataID, req.NotesMd, string(chatLinksJSON),
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
		writeInternalError(w, err)
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

// checkActorNameAvailable enforces the actor_name uniqueness/reserved-word
// rules shared by updateOrganization (PUT) and patchOrganization (PATCH)
// (#1012) — writes the error response and returns false on conflict.
// excludeID is the organization's own id so renaming to the name it already
// has doesn't self-conflict.
func checkActorNameAvailable(w http.ResponseWriter, actorName, excludeID string) bool {
	if actorName == "relay" {
		writeError(w, "actor_name 'relay' is reserved", http.StatusConflict)
		return false
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM organizations WHERE actor_name=? AND id!=?", actorName, excludeID).Scan(&n)
	if n > 0 {
		writeError(w, "actor_name already in use", http.StatusConflict)
		return false
	}
	return true
}

// writeOrganizationFields runs the UPDATE shared by updateOrganization (PUT)
// and patchOrganization (PATCH) — both end up replacing the same editable
// columns on o (#1012).
func writeOrganizationFields(id string, o Organization, updatedBy string) error {
	chatLinksJSON, _ := json.Marshal(o.ChatLinks)
	_, err := db.Exec(
		"UPDATE organizations SET name=?, description=?, actor_name=?, website=?, instagram=?, mastodon=?, facebook=?, contact_email=?, contact_name=?, wikidata_id=?, notes_md=?, chat_links=?, updated_at=strftime('%s','now'), updated_by=? WHERE id=?",
		o.Name, o.Description, o.ActorName, o.Website, o.Instagram, o.Mastodon, o.Facebook, o.ContactEmail, o.ContactName, o.WikidataID, o.NotesMd, string(chatLinksJSON), updatedBy, id,
	)
	return err
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
		writeInternalError(w, err)
		return
	}
	var req CreateOrganizationRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if callerRole == RoleAdmin {
		if req.ActorName != "" && !checkActorNameAvailable(w, req.ActorName, id) {
			return
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
	o.ChatLinks = filterChatLinks(req.ChatLinks)
	if err := writeOrganizationFields(id, o, resolveDisplayName(callerID)); err != nil {
		writeError(w, "Failed to update organization", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(o)
}

// PATCH /api/v1/organizations/{id} — partial update (RFC 7396 JSON Merge Patch).
// Same field-level permissions as PUT: admins may change name/actor_name,
// org members with role user may only change description/contact/social fields.
func patchOrganization(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if ct := r.Header.Get("Content-Type"); ct != "application/merge-patch+json" {
		writeError(w, "PATCH requires Content-Type: application/merge-patch+json", http.StatusUnsupportedMediaType)
		return
	}
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
		writeInternalError(w, err)
		return
	}
	var req OrganizationMergePatchRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if callerRole == RoleAdmin {
		if req.ActorName != nil {
			if !checkActorNameAvailable(w, *req.ActorName, id) {
				return
			}
			o.ActorName = *req.ActorName
		}
		if req.Name != nil && *req.Name != "" {
			o.Name = *req.Name
		}
	}
	if req.Description != nil {
		o.Description = *req.Description
	}
	if req.Website != nil {
		o.Website = *req.Website
	}
	if req.Instagram != nil {
		o.Instagram = *req.Instagram
	}
	if req.Mastodon != nil {
		o.Mastodon = *req.Mastodon
	}
	if req.Facebook != nil {
		o.Facebook = *req.Facebook
	}
	if req.ContactEmail != nil {
		o.ContactEmail = *req.ContactEmail
	}
	if req.ContactName != nil {
		o.ContactName = *req.ContactName
	}
	if req.WikidataID != nil {
		o.WikidataID = *req.WikidataID
	}
	if req.NotesMd != nil {
		o.NotesMd = *req.NotesMd
	}
	if req.ChatLinks != nil {
		o.ChatLinks = filterChatLinks(*req.ChatLinks)
	}
	if err := writeOrganizationFields(id, o, resolveDisplayName(callerID)); err != nil {
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
		writeInternalError(w, err)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Organization not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/organizations/{id}/members
// GET /api/v1/organizations/members?org_ids=1,2,3
// Returns all members for the given org IDs in a single query, grouped by org_id.
// Non-admin callers may only request orgs they belong to; unknown IDs are silently dropped.
func getOrganizationMembersBulk(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	callerID, callerRole := callerFromRequest(r)

	raw := r.URL.Query().Get("org_ids")
	if raw == "" {
		json.NewEncoder(w).Encode(map[int][]OrganizationMember{})
		return
	}
	requested := map[int]bool{}
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if id, err := strconv.Atoi(s); err == nil && id > 0 {
			requested[id] = true
		}
	}
	if len(requested) == 0 {
		json.NewEncoder(w).Encode(map[int][]OrganizationMember{})
		return
	}

	// Non-admins may only see orgs they belong to.
	if callerRole != RoleAdmin {
		memberRows, err := db.Query(
			"SELECT organization_id FROM organization_members WHERE user_id = ?", callerID)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		allowed := map[int]bool{}
		for memberRows.Next() {
			var oid int
			memberRows.Scan(&oid)
			allowed[oid] = true
		}
		memberRows.Close()
		for id := range requested {
			if !allowed[id] {
				delete(requested, id)
			}
		}
	}

	if len(requested) == 0 {
		json.NewEncoder(w).Encode(map[int][]OrganizationMember{})
		return
	}

	var ids []any
	var placeholders []string
	for id := range requested {
		ids = append(ids, id)
		placeholders = append(placeholders, "?")
	}
	rows, err := db.Query(`
		SELECT om.organization_id, om.user_id, COALESCE(u.email,''), COALESCE(u.display_name,''), u.role, om.created_at
		FROM organization_members om
		JOIN users u ON om.user_id = u.id
		WHERE om.organization_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY om.organization_id, om.created_at`, ids...)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()
	result := map[int][]OrganizationMember{}
	for rows.Next() {
		var m OrganizationMember
		if err := rows.Scan(&m.OrganizationID, &m.UserID, &m.Email, &m.DisplayName, &m.Role, &m.CreatedAt); err != nil {
			writeInternalError(w, err)
			return
		}
		result[m.OrganizationID] = append(result[m.OrganizationID], m)
	}
	json.NewEncoder(w).Encode(result)
}

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
		writeInternalError(w, err)
		return
	}
	defer rows.Close()
	members := []OrganizationMember{}
	for rows.Next() {
		var m OrganizationMember
		if err := rows.Scan(&m.OrganizationID, &m.UserID, &m.Email, &m.DisplayName, &m.Role, &m.CreatedAt); err != nil {
			writeInternalError(w, err)
			return
		}
		members = append(members, m)
	}
	json.NewEncoder(w).Encode(members)
}

// POST /api/v1/organizations/{id}/members
func addOrganizationMember(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	orgID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "Invalid organization ID", http.StatusBadRequest)
		return
	}
	callerID, callerRole := callerFromRequest(r)
	if callerRole == RolePublisher {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	if callerRole != RoleAdmin && !isOrgMember(callerID, orgID) {
		writeError(w, "Forbidden: you must be a member of this organization", http.StatusForbidden)
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
	orgID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "Invalid organization ID", http.StatusBadRequest)
		return
	}
	callerID, callerRole := callerFromRequest(r)
	if callerRole == RolePublisher {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	if callerRole != RoleAdmin && !isOrgMember(callerID, orgID) {
		writeError(w, "Forbidden: you must be a member of this organization", http.StatusForbidden)
		return
	}
	result, err := db.Exec(
		"DELETE FROM organization_members WHERE organization_id = ? AND user_id = ?",
		orgID, r.PathValue("user_id"),
	)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Member not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
