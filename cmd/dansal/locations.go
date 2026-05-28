package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

)

type Location struct {
	ID              int      `json:"id"`
	Location        string   `json:"location"`
	ShortName       string   `json:"short_name,omitempty"`
	Address         string   `json:"address"`
	Zipcode         string   `json:"zipcode"`
	Town            string   `json:"town"`
	Country         string   `json:"country,omitempty"`
	CountryCode     string   `json:"country_code,omitempty"`
	Region          string   `json:"region,omitempty"`
	Latitude        *float64 `json:"latitude,omitempty"`
	Longitude       *float64 `json:"longitude,omitempty"`
	Internetsite    string   `json:"internetsite"`
	OsmID                *int64   `json:"osm_id,omitempty"`
	OsmType              string   `json:"osm_type,omitempty"`
	CreatedAt            string   `json:"created_at"`
	OrganizationIDs      []int    `json:"organization_ids,omitempty"`
	NotesMd        string          `json:"notes_md,omitempty"`
	Attributes     map[string]bool `json:"attributes,omitempty"`
	Parking        string          `json:"parking,omitempty"`
	FloorCondition string          `json:"floor_condition,omitempty"`
}

func validCountryCode(code string) bool {
	if code == "" {
		return true
	}
	if len(code) != 2 {
		return false
	}
	for _, c := range code {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

type LocationCreateRequest struct {
	Location        string   `json:"location"`
	ShortName       string   `json:"short_name"`
	Address         string   `json:"address"`
	Zipcode         string   `json:"zipcode"`
	Town            string   `json:"town"`
	Country         string   `json:"country"`
	CountryCode     string   `json:"country_code"`
	Region          string   `json:"region"`
	Latitude        *float64 `json:"latitude,omitempty"`
	Longitude       *float64 `json:"longitude,omitempty"`
	Internetsite         string   `json:"internetsite"`
	OsmID                *int64   `json:"osm_id,omitempty"`
	OsmType              string   `json:"osm_type,omitempty"`
	OrganizationIDs      []int    `json:"organization_ids,omitempty"`
	NotesMd        string          `json:"notes_md"`
	Attributes     map[string]bool `json:"attributes,omitempty"`
	Parking        string          `json:"parking,omitempty"`
	FloorCondition string          `json:"floor_condition,omitempty"`
}

// locationCols is the shared SELECT column list used by all location queries.
// Must match the scanLocation scan order exactly.
const locationCols = `l.id, l.location, COALESCE(l.short_name,''), l.address, COALESCE(l.zipcode,''),
	l.town, COALESCE(l.country,''), COALESCE(l.country_code,''), COALESCE(l.region,''),
	l.latitude, l.longitude, COALESCE(l.internetsite,''), l.osm_id, COALESCE(l.osm_type,''),
	l.created_at, COALESCE(GROUP_CONCAT(lo.organization_id),''), COALESCE(l.notes_md,''),
	COALESCE(l.attributes,'{}'), COALESCE(l.parking,''), COALESCE(l.floor_condition,'')`

func scanLocation(s scanner, loc *Location) error {
	var orgIDsStr, attrsJSON string
	if err := s.Scan(&loc.ID, &loc.Location, &loc.ShortName, &loc.Address,
		&loc.Zipcode, &loc.Town, &loc.Country, &loc.CountryCode, &loc.Region,
		&loc.Latitude, &loc.Longitude, &loc.Internetsite, &loc.OsmID, &loc.OsmType,
		&loc.CreatedAt, &orgIDsStr, &loc.NotesMd, &attrsJSON, &loc.Parking, &loc.FloorCondition); err != nil {
		return err
	}
	if attrsJSON != "" && attrsJSON != "{}" {
		json.Unmarshal([]byte(attrsJSON), &loc.Attributes)
	}
	loc.OrganizationIDs = parseOrgIDs(orgIDsStr)
	return nil
}

func attrsJSON(attrs map[string]bool) string {
	if len(attrs) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(attrs)
	return string(b)
}

func parseOrgIDs(s string) []int {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	ids := make([]int, 0, len(parts))
	for _, p := range parts {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			ids = append(ids, n)
		}
	}
	return ids
}

func syncLocationOrgs(locationID int, orgIDs []int) {
	db.Exec("DELETE FROM location_organizations WHERE location_id = ?", locationID)
	for _, orgID := range orgIDs {
		db.Exec("INSERT OR IGNORE INTO location_organizations (location_id, organization_id) VALUES (?, ?)", locationID, orgID)
	}
}

func locationHasOrgMember(locationID, userID int) bool {
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM location_organizations lo
		JOIN organization_members om ON lo.organization_id = om.organization_id
		WHERE lo.location_id = ? AND om.user_id = ?`, locationID, userID).Scan(&count)
	return count > 0
}

type LocationCreateResponse struct {
	Location         Location   `json:"location"`
	SimilarLocations []Location `json:"similar_locations,omitempty"`
}

// Address parsing — same patterns as dansal_admin fill-location-fields.
var (
	locPatternFull    = regexp.MustCompile(`^[^,]+,\s*(.+?),\s*(\d{5})\s+(.+)$`)
	locPatternNoZip   = regexp.MustCompile(`^[^,]+,\s*(.+?\s+\d+\w*),\s*([A-ZÄÖÜ].+)$`)
	locPatternZipOnly = regexp.MustCompile(`^[^,]+,\s*(\d{5})\s+(.+)$`)
	trailingNr        = regexp.MustCompile(`\s+\d+\w*$`)
)

type locationParsed struct{ street, town string }

func parseLocationNameServer(name string) (locationParsed, bool) {
	if m := locPatternFull.FindStringSubmatch(name); m != nil {
		return locationParsed{street: strings.TrimSpace(m[1]), town: strings.TrimSpace(m[3])}, true
	}
	if m := locPatternNoZip.FindStringSubmatch(name); m != nil {
		return locationParsed{street: strings.TrimSpace(m[1]), town: strings.TrimSpace(m[2])}, true
	}
	if m := locPatternZipOnly.FindStringSubmatch(name); m != nil {
		return locationParsed{town: strings.TrimSpace(m[2])}, true
	}
	return locationParsed{}, false
}

func streetBase(address string) string {
	return strings.TrimSpace(trailingNr.ReplaceAllString(address, ""))
}

// similarLocations returns locations on the same street (ignoring house number)
// in the same town whose name differs from the one being created.
func similarLocations(name, street, town string) []Location {
	if town == "" {
		return nil
	}
	base := streetBase(street)
	var rows *sql.Rows
	var err error
	const cols = `SELECT ` + locationCols + `
		FROM locations l LEFT JOIN location_organizations lo ON l.id=lo.location_id`
	if base != "" {
		rows, err = db.Query(cols+`
			WHERE l.town=? AND l.address!='' AND (l.address=? OR l.address LIKE ?) AND l.location!=?
			GROUP BY l.id`,
			town, base, base+" %", name,
		)
	} else {
		rows, err = db.Query(cols+`
			WHERE l.town=? AND l.location!=?
			GROUP BY l.id`,
			town, name,
		)
	}
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []Location
	for rows.Next() {
		var loc Location
		if err := scanLocation(rows, &loc); err != nil {
			continue
		}
		result = append(result, loc)
	}
	return result
}

type LocationUpdateRequest struct {
	ShortName            string   `json:"short_name"`
	Address              string   `json:"address"`
	Zipcode              string   `json:"zipcode"`
	Town                 string   `json:"town"`
	Country              string   `json:"country"`
	CountryCode          string   `json:"country_code"`
	Region               string   `json:"region"`
	Latitude             *float64 `json:"latitude,omitempty"`
	Longitude            *float64 `json:"longitude,omitempty"`
	Internetsite   string          `json:"internetsite"`
	NotesMd        string          `json:"notes_md"`
	Attributes     map[string]bool `json:"attributes,omitempty"`
	Parking        string          `json:"parking,omitempty"`
	FloorCondition string          `json:"floor_condition,omitempty"`
}

// GET /api/v1/locations - List all locations
func getLocations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	query := `SELECT ` + locationCols + ` FROM locations l LEFT JOIN location_organizations lo ON l.id=lo.location_id`
	var args []any
	if country := r.URL.Query().Get("country"); country != "" {
		codes, err := parseCountryCodes(country)
		if err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		placeholders := strings.Repeat("?,", len(codes))
		placeholders = placeholders[:len(placeholders)-1]
		query += " WHERE l.country_code IN (" + placeholders + ")"
		for _, c := range codes {
			args = append(args, c)
		}
	}
	query += " GROUP BY l.id"

	rows, err := db.Query(query, args...)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	locations := []Location{}
	for rows.Next() {
		var location Location
		if err := scanLocation(rows, &location); err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		locations = append(locations, location)
	}

	json.NewEncoder(w).Encode(locations)
}

// POST /api/v1/locations - Create one or more locations
func createLocation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, requesterRole := callerFromRequest(r)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		if errors.As(err, new(*http.MaxBytesError)) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, err.Error(), status)
		return
	}

	var reqs []LocationCreateRequest
	if json.Unmarshal(body, &reqs) != nil || len(reqs) == 0 || reqs[0].Location == "" {
		var single LocationCreateRequest
		if err := json.Unmarshal(body, &single); err != nil {
			writeError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		reqs = []LocationCreateRequest{single}
	}

	// Validate all items before inserting any.
	for _, req := range reqs {
		if req.Location == "" {
			writeError(w, "location is required", http.StatusBadRequest)
			return
		}
		if !validCountryCode(req.CountryCode) {
			writeError(w, "country_code must be 2 uppercase letters (e.g. 'DE') or empty", http.StatusBadRequest)
			return
		}
	}
	if requesterRole != RoleAdmin {
		if requesterRole != RoleUser && requesterRole != RolePublisher {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		checked := make(map[int]bool)
		for _, req := range reqs {
			if len(req.OrganizationIDs) == 0 {
				writeError(w, "organization_ids is required", http.StatusBadRequest)
				return
			}
			for _, orgID := range req.OrganizationIDs {
				member, seen := checked[orgID]
				if !seen {
					member = isOrgMember(callerID, orgID)
					checked[orgID] = member
				}
				if !member {
					writeError(w, "Forbidden: not a member of the specified organization", http.StatusForbidden)
					return
				}
			}
		}
	}

	results := make([]LocationCreateResponse, 0, len(reqs))
	for _, req := range reqs {
		// Derive street and town for the duplicate-street check.
		// Prefer explicit request fields; fall back to parsing the location name.
		street, town := req.Address, req.Town
		if street == "" || town == "" {
			if parsed, ok := parseLocationNameServer(req.Location); ok {
				if street == "" {
					street = parsed.street
				}
				if town == "" {
					town = parsed.town
				}
			}
		}
		// Check for duplicate OSM place before insert.
		if req.OsmID != nil && req.OsmType != "" {
			var existingID int
			if db.QueryRow("SELECT id FROM locations WHERE osm_type=? AND osm_id=?", req.OsmType, *req.OsmID).Scan(&existingID) == nil {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]any{"error": "location already exists", "existing_id": existingID})
				return
			}
		}

		similar := similarLocations(req.Location, street, town)

		result, err := db.Exec(
			"INSERT INTO locations (location, short_name, address, zipcode, town, country, country_code, region, latitude, longitude, internetsite, osm_id, osm_type, notes_md, attributes, parking, floor_condition) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			req.Location, req.ShortName, req.Address, req.Zipcode, req.Town, req.Country, req.CountryCode, req.Region, req.Latitude, req.Longitude, req.Internetsite, req.OsmID, req.OsmType, req.NotesMd, attrsJSON(req.Attributes), req.Parking, req.FloorCondition,
		)
		if err != nil {
			writeError(w, "Failed to create location", http.StatusInternalServerError)
			return
		}
		id, _ := result.LastInsertId()
		syncLocationOrgs(int(id), req.OrganizationIDs)
		loc := Location{
			ID:              int(id),
			Location:        req.Location,
			ShortName:       req.ShortName,
			Address:         req.Address,
			Zipcode:         req.Zipcode,
			Town:            req.Town,
			Country:         req.Country,
			CountryCode:     req.CountryCode,
			Region:          req.Region,
			Latitude:        req.Latitude,
			Longitude:       req.Longitude,
			Internetsite:         req.Internetsite,
			OsmID:                req.OsmID,
			OsmType:              req.OsmType,
			OrganizationIDs:      req.OrganizationIDs,
			NotesMd:        req.NotesMd,
			Attributes:     req.Attributes,
			Parking:        req.Parking,
			FloorCondition: req.FloorCondition,
		}
		results = append(results, LocationCreateResponse{Location: loc, SimilarLocations: similar})
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(results)
}

// GET /api/v1/locations/{id} - Get a specific location
func getLocation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	id := r.PathValue("id")

	var location Location
	err := scanLocation(db.QueryRow(`SELECT `+locationCols+`
		FROM locations l LEFT JOIN location_organizations lo ON l.id=lo.location_id
		WHERE l.id=? GROUP BY l.id`, id), &location)

	if err == sql.ErrNoRows {
		writeError(w, "Location not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(location)
}

// PATCH /api/v1/locations/{id} - Full update including organization_id
func patchLocation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, requesterRole := callerFromRequest(r)
	id := r.PathValue("id")

	if requesterRole != RoleAdmin {
		if requesterRole != RoleUser && requesterRole != RolePublisher {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		var exists int
		if err := db.QueryRow("SELECT COUNT(*) FROM locations WHERE id=?", id).Scan(&exists); err != nil || exists == 0 {
			writeError(w, "Location not found", http.StatusNotFound)
			return
		}
		idInt, _ := strconv.Atoi(id)
		if !locationHasOrgMember(idInt, callerID) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	var req struct {
		Location             string   `json:"location"`
		ShortName            string   `json:"short_name"`
		Address              string   `json:"address"`
		Zipcode              string   `json:"zipcode"`
		Town                 string   `json:"town"`
		Country              string   `json:"country"`
		CountryCode          string   `json:"country_code"`
		Region               string   `json:"region"`
		Latitude             *float64 `json:"latitude"`
		Longitude            *float64 `json:"longitude"`
		Internetsite         string   `json:"internetsite"`
		OsmID                *int64   `json:"osm_id"`
		OsmType              string   `json:"osm_type"`
		OrganizationIDs      []int    `json:"organization_ids"`
		NotesMd        string          `json:"notes_md"`
		Attributes     map[string]bool `json:"attributes,omitempty"`
		Parking        string          `json:"parking,omitempty"`
		FloorCondition string          `json:"floor_condition,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !validCountryCode(req.CountryCode) {
		writeError(w, "country_code must be 2 uppercase letters (e.g. 'DE') or empty", http.StatusBadRequest)
		return
	}

	var loc Location
	err := scanLocation(db.QueryRow(`SELECT `+locationCols+`
		FROM locations l LEFT JOIN location_organizations lo ON l.id=lo.location_id
		WHERE l.id=? GROUP BY l.id`, id), &loc)
	if err == sql.ErrNoRows {
		writeError(w, "Location not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if req.Location != "" {
		loc.Location = req.Location
	}
	loc.ShortName = req.ShortName
	loc.Address = req.Address
	loc.Zipcode = req.Zipcode
	if req.Town != "" {
		loc.Town = req.Town
	}
	loc.Country = req.Country
	loc.CountryCode = req.CountryCode
	loc.Region = req.Region
	loc.Latitude = req.Latitude
	loc.Longitude = req.Longitude
	loc.Internetsite = req.Internetsite
	loc.OsmID = req.OsmID
	loc.OsmType = req.OsmType
	loc.OrganizationIDs = req.OrganizationIDs
	loc.NotesMd = req.NotesMd
	loc.Attributes = req.Attributes
	loc.Parking = req.Parking
	loc.FloorCondition = req.FloorCondition

	if _, err := db.Exec(
		"UPDATE locations SET location=?, short_name=?, address=?, zipcode=?, town=?, country=?, country_code=?, region=?, latitude=?, longitude=?, internetsite=?, osm_id=?, osm_type=?, notes_md=?, attributes=?, parking=?, floor_condition=? WHERE id=?",
		loc.Location, loc.ShortName, loc.Address, loc.Zipcode, loc.Town, loc.Country, loc.CountryCode, loc.Region, loc.Latitude, loc.Longitude, loc.Internetsite, loc.OsmID, loc.OsmType, loc.NotesMd, attrsJSON(loc.Attributes), loc.Parking, loc.FloorCondition, loc.ID,
	); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	syncLocationOrgs(loc.ID, loc.OrganizationIDs)

	json.NewEncoder(w).Encode(loc)
}

// POST /api/v1/locations/bulk-assign-org - Admin bulk-assign organization to locations
func bulkAssignLocationOrg(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden: admin only", http.StatusForbidden)
		return
	}
	var req struct {
		IDs            []int `json:"ids"`
		OrganizationID *int  `json:"organization_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}
	for _, locID := range req.IDs {
		if req.OrganizationID != nil {
			db.Exec("INSERT OR IGNORE INTO location_organizations (location_id, organization_id) VALUES (?, ?)", locID, *req.OrganizationID)
		} else {
			db.Exec("DELETE FROM location_organizations WHERE location_id=?", locID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/locations/{id} - Delete a location
// POST /api/v1/locations/unassign-org — remove one org from one location
func unassignLocationOrg(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden: admin only", http.StatusForbidden)
		return
	}
	var req struct {
		LocationID     int `json:"location_id"`
		OrganizationID int `json:"organization_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LocationID == 0 || req.OrganizationID == 0 {
		writeError(w, "location_id and organization_id are required", http.StatusBadRequest)
		return
	}
	db.Exec("DELETE FROM location_organizations WHERE location_id=? AND organization_id=?", req.LocationID, req.OrganizationID)
	w.WriteHeader(http.StatusNoContent)
}

func deleteLocation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, requesterRole := callerFromRequest(r)
	id := r.PathValue("id")

	if requesterRole != RoleAdmin {
		if requesterRole != RoleUser {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		var exists int
		if err := db.QueryRow("SELECT COUNT(*) FROM locations WHERE id=?", id).Scan(&exists); err != nil || exists == 0 {
			writeError(w, "Location not found", http.StatusNotFound)
			return
		}
		idInt, _ := strconv.Atoi(id)
		if !locationHasOrgMember(idInt, callerID) {
			writeError(w, "Forbidden: not a member of the location's organization", http.StatusForbidden)
			return
		}
	}

	// Check if location exists
	var locationID int
	err := db.QueryRow("SELECT id FROM locations WHERE id = ?", id).Scan(&locationID)
	if err == sql.ErrNoRows {
		writeError(w, "Location not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Delete location
	result, err := db.Exec("DELETE FROM locations WHERE id = ?", id)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		writeError(w, "Location not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
