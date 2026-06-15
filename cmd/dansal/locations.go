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
	ID              int             `json:"id"`
	Location        string          `json:"location"`
	ShortName       string          `json:"short_name,omitempty"`
	Address         string          `json:"address"`
	Zipcode         string          `json:"zipcode"`
	Town            string          `json:"town"`
	Country         string          `json:"country,omitempty"`
	CountryCode     string          `json:"country_code,omitempty"`
	Region          string          `json:"region,omitempty"`
	Latitude        *float64        `json:"latitude,omitempty"`
	Longitude       *float64        `json:"longitude,omitempty"`
	Geohash         string          `json:"geohash,omitempty"`
	Internetsite    string          `json:"internetsite"`
	OsmID           *int64          `json:"osm_id,omitempty"`
	OsmType         string          `json:"osm_type,omitempty"`
	WikidataID      string          `json:"wikidata_id,omitempty"`
	MBPlaceID       string          `json:"mb_place_id,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       int64           `json:"updated_at,omitempty"`
	DistanceKm      *float64        `json:"distance_km,omitempty"`
	OrganizationIDs []int           `json:"organization_ids,omitempty"`
	NotesMd         string          `json:"notes_md,omitempty"`
	Attributes      map[string]bool `json:"attributes,omitempty"`
	Parking         string          `json:"parking,omitempty"`
	FloorCondition  string          `json:"floor_condition,omitempty"`
	NoStreetShoes   bool            `json:"no_street_shoes,omitempty"`
	Aliases         []string        `json:"aliases,omitempty"`
	FutureEventCount int           `json:"future_event_count,omitempty"`
	PastEventCount   int           `json:"past_event_count,omitempty"`
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
	Location        string          `json:"location"`
	ShortName       string          `json:"short_name"`
	Address         string          `json:"address"`
	Zipcode         string          `json:"zipcode"`
	Town            string          `json:"town"`
	Country         string          `json:"country"`
	CountryCode     string          `json:"country_code"`
	Region          string          `json:"region"`
	Latitude        *float64        `json:"latitude,omitempty"`
	Longitude       *float64        `json:"longitude,omitempty"`
	Internetsite    string          `json:"internetsite"`
	OsmID           *int64          `json:"osm_id,omitempty"`
	OsmType         string          `json:"osm_type,omitempty"`
	WikidataID      string          `json:"wikidata_id,omitempty"`
	MBPlaceID       string          `json:"mb_place_id,omitempty"`
	OrganizationIDs []int           `json:"organization_ids,omitempty"`
	NotesMd         string          `json:"notes_md"`
	Attributes      map[string]bool `json:"attributes,omitempty"`
	Parking         string          `json:"parking,omitempty"`
	FloorCondition  string          `json:"floor_condition,omitempty"`
	NoStreetShoes   bool            `json:"no_street_shoes,omitempty"`
	Aliases         []string        `json:"aliases,omitempty"`
}

// locationCols is the shared SELECT column list used by all location queries.
// Must match the scanLocation scan order exactly.
const locationCols = `l.id, l.location, COALESCE(l.short_name,''), l.address, COALESCE(l.zipcode,''),
	l.town, COALESCE(l.country,''), COALESCE(l.country_code,''), COALESCE(l.region,''),
	l.latitude, l.longitude, COALESCE(l.internetsite,''), l.osm_id, COALESCE(l.osm_type,''),
	COALESCE(l.geohash,''), COALESCE(l.wikidata_id,''), COALESCE(l.mb_place_id,''),
	l.created_at, COALESCE(l.updated_at,0), COALESCE(GROUP_CONCAT(lo.organization_id),''), COALESCE(l.notes_md,''),
	COALESCE(l.attributes,'{}'), COALESCE(l.parking,''), COALESCE(l.floor_condition,''), COALESCE(l.no_street_shoes,0), COALESCE(l.aliases,'[]')`

// scanLocation scans a locationCols row into loc. Extra destination pointers
// (e.g. for appended future/past event count columns) can be passed via extra.
func scanLocation(s scanner, loc *Location, extra ...any) error {
	var orgIDsStr, attrsJSON, aliasesJSON string
	dest := []any{&loc.ID, &loc.Location, &loc.ShortName, &loc.Address,
		&loc.Zipcode, &loc.Town, &loc.Country, &loc.CountryCode, &loc.Region,
		&loc.Latitude, &loc.Longitude, &loc.Internetsite, &loc.OsmID, &loc.OsmType,
		&loc.Geohash, &loc.WikidataID, &loc.MBPlaceID,
		&loc.CreatedAt, &loc.UpdatedAt, &orgIDsStr, &loc.NotesMd, &attrsJSON, &loc.Parking, &loc.FloorCondition, &loc.NoStreetShoes, &aliasesJSON}
	if err := s.Scan(append(dest, extra...)...); err != nil {
		return err
	}
	if attrsJSON != "" && attrsJSON != "{}" {
		json.Unmarshal([]byte(attrsJSON), &loc.Attributes)
	}
	if aliasesJSON != "" && aliasesJSON != "[]" {
		json.Unmarshal([]byte(aliasesJSON), &loc.Aliases)
	}
	loc.OrganizationIDs = parseOrgIDs(orgIDsStr)
	if loc.Geohash == "" && loc.Latitude != nil && loc.Longitude != nil {
		loc.Geohash = geohashEncode(*loc.Latitude, *loc.Longitude, 7)
	}
	return nil
}

func attrsJSON(attrs map[string]bool) string {
	if len(attrs) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(attrs)
	return string(b)
}

func aliasesJSON(aliases []string) string {
	if len(aliases) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(aliases)
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
	batchInsertPairs(db, "location_organizations", "location_id", "organization_id", locationID, orgIDs)
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
	ShortName      string          `json:"short_name"`
	Address        string          `json:"address"`
	Zipcode        string          `json:"zipcode"`
	Town           string          `json:"town"`
	Country        string          `json:"country"`
	CountryCode    string          `json:"country_code"`
	Region         string          `json:"region"`
	Latitude       *float64        `json:"latitude,omitempty"`
	Longitude      *float64        `json:"longitude,omitempty"`
	Internetsite   string          `json:"internetsite"`
	NotesMd        string          `json:"notes_md"`
	Attributes     map[string]bool `json:"attributes,omitempty"`
	Parking        string          `json:"parking,omitempty"`
	FloorCondition string          `json:"floor_condition,omitempty"`
	NoStreetShoes  bool            `json:"no_street_shoes,omitempty"`
}

// GET /api/v1/locations - List all locations
func getLocations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Determine if lat/lng/radius geo search is requested (for haversine distance).
	var geoLat, geoLng float64
	var geoRadius float64
	hasGeoRadius := false
	if latStr, lngStr, radStr := q.Get("lat"), q.Get("lng"), q.Get("radius"); latStr != "" && lngStr != "" && radStr != "" {
		lat, latErr := strconv.ParseFloat(latStr, 64)
		lng, lngErr := strconv.ParseFloat(lngStr, 64)
		rad, radErr := strconv.ParseFloat(radStr, 64)
		if latErr == nil && lngErr == nil && radErr == nil && rad > 0 {
			geoLat, geoLng, geoRadius = lat, lng, rad
			hasGeoRadius = true
		}
	}

	withCounts := q.Get("with_event_counts") == "true"

	query := `SELECT ` + locationCols
	if withCounts {
		query += `, COALESCE(ec.future_count,0), COALESCE(ec.past_count,0)`
	}
	query += ` FROM locations l LEFT JOIN location_organizations lo ON l.id=lo.location_id`
	if withCounts {
		query += ` LEFT JOIN (
			SELECT location_id,
				SUM(CASE WHEN start_time > strftime('%s','now') AND is_published=1 THEN 1 ELSE 0 END) AS future_count,
				SUM(CASE WHEN start_time <= strftime('%s','now') AND is_published=1 THEN 1 ELSE 0 END) AS past_count
			FROM events WHERE location_id IS NOT NULL GROUP BY location_id
		) ec ON ec.location_id = l.id`
	}
	var args []any
	where := false

	addWhere := func(clause string, vals ...any) {
		if !where {
			query += " WHERE " + clause
			where = true
		} else {
			query += " AND " + clause
		}
		args = append(args, vals...)
	}

	if country := q.Get("country"); country != "" {
		codes, err := parseCountryCodes(country)
		if err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		placeholders := strings.Repeat("?,", len(codes))
		placeholders = placeholders[:len(placeholders)-1]
		addWhere("l.country_code IN (" + placeholders + ")")
		for _, c := range codes {
			args = append(args, c)
		}
	}

	// Task A: name, town, org_id filters
	if name := q.Get("name"); name != "" {
		addWhere("LOWER(l.location) LIKE LOWER(?)", "%"+name+"%")
	}
	if town := q.Get("town"); town != "" {
		addWhere("l.town LIKE ?", "%"+town+"%")
	}
	if orgIDStr := q.Get("org_id"); orgIDStr != "" {
		if orgID, err := strconv.Atoi(orgIDStr); err == nil {
			addWhere("EXISTS (SELECT 1 FROM location_organizations lo2 WHERE lo2.location_id=l.id AND lo2.organization_id=?)", orgID)
		}
	}

	// Task B: bbox and lat/lng/radius geo filters
	if bboxStr := q.Get("bbox"); bboxStr != "" {
		parts := strings.Split(bboxStr, ",")
		if len(parts) == 4 {
			minLng, e1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
			minLat, e2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			maxLng, e3 := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
			maxLat, e4 := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
			if e1 == nil && e2 == nil && e3 == nil && e4 == nil {
				addWhere("l.latitude BETWEEN ? AND ? AND l.longitude BETWEEN ? AND ? AND l.latitude IS NOT NULL", minLat, maxLat, minLng, maxLng)
			}
		}
	} else if hasGeoRadius {
		minLat, maxLat, minLng, maxLng := geohashRadiusToBBox(geoLat, geoLng, geoRadius)
		addWhere("l.latitude BETWEEN ? AND ? AND l.longitude BETWEEN ? AND ? AND l.latitude IS NOT NULL", minLat, maxLat, minLng, maxLng)
	}

	query += " GROUP BY l.id"

	rows, err := db.Query(query, args...)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	accept := r.Header.Get("Accept")

	locations := []Location{}
	for rows.Next() {
		var location Location
		if withCounts {
			if err := scanLocation(rows, &location, &location.FutureEventCount, &location.PastEventCount); err != nil {
				writeError(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if err := scanLocation(rows, &location); err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if hasGeoRadius && location.Latitude != nil && location.Longitude != nil {
			d := haversineKm(geoLat, geoLng, *location.Latitude, *location.Longitude)
			location.DistanceKm = &d
		}
		locations = append(locations, location)
	}

	if strings.Contains(accept, "application/geo+json") {
		writeLocationGeoJSON(w, locations)
	} else if strings.Contains(accept, "application/atom+xml") {
		writeLocationsAtom(w, r, locations)
	} else {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(locations)
	}
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

		insertGH := ""
		if req.Latitude != nil && req.Longitude != nil {
			insertGH = geohashEncode(*req.Latitude, *req.Longitude, 7)
		}
		result, err := db.Exec(
			"INSERT INTO locations (location, short_name, address, zipcode, town, country, country_code, region, latitude, longitude, internetsite, osm_id, osm_type, geohash, wikidata_id, mb_place_id, notes_md, attributes, parking, floor_condition, no_street_shoes, aliases, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s','now'))",
			req.Location, req.ShortName, req.Address, req.Zipcode, req.Town, req.Country, req.CountryCode, req.Region, req.Latitude, req.Longitude, req.Internetsite, req.OsmID, req.OsmType, insertGH, req.WikidataID, req.MBPlaceID, req.NotesMd, attrsJSON(req.Attributes), req.Parking, req.FloorCondition, req.NoStreetShoes, aliasesJSON(req.Aliases),
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
			Internetsite:    req.Internetsite,
			OsmID:           req.OsmID,
			OsmType:         req.OsmType,
			OrganizationIDs: req.OrganizationIDs,
			NotesMd:         req.NotesMd,
			Attributes:      req.Attributes,
			Parking:         req.Parking,
			FloorCondition:  req.FloorCondition,
		}
		results = append(results, LocationCreateResponse{Location: loc, SimilarLocations: similar})
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(results)
}

// GET /api/v1/locations/{id} - Get a specific location
func getLocation(w http.ResponseWriter, r *http.Request) {
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

	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/geo+json") {
		writeLocationGeoJSONSingle(w, location)
	} else if strings.Contains(accept, "application/atom+xml") {
		writeLocationsAtom(w, r, []Location{location})
	} else {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(location)
	}
}

func writeLocationGeoJSON(w http.ResponseWriter, locations []Location) {
	w.Header().Set("Content-Type", "application/geo+json")
	features := make([]map[string]any, 0, len(locations))
	for _, loc := range locations {
		features = append(features, locationFeature(loc))
	}
	json.NewEncoder(w).Encode(map[string]any{
		"type":     "FeatureCollection",
		"features": features,
	})
}

func writeLocationGeoJSONSingle(w http.ResponseWriter, loc Location) {
	w.Header().Set("Content-Type", "application/geo+json")
	json.NewEncoder(w).Encode(locationFeature(loc))
}

func locationFeature(loc Location) map[string]any {
	var geometry any
	if loc.Latitude != nil && loc.Longitude != nil {
		geometry = map[string]any{
			"type":        "Point",
			"coordinates": []float64{*loc.Longitude, *loc.Latitude},
		}
	}
	return map[string]any{
		"type":       "Feature",
		"geometry":   geometry,
		"properties": loc,
	}
}

func writeLocationsAtom(w http.ResponseWriter, r *http.Request, locations []Location) {
	host := r.Host
	entries := make([]apiFeedEntry, 0, len(locations))
	for _, loc := range locations {
		e := apiFeedEntry{
			Title:   loc.Location,
			ID:      "https://" + host + "/api/v1/locations/" + strconv.Itoa(loc.ID),
			Updated: atomTime(loc.UpdatedAt),
			Summary: loc.Town,
		}
		if loc.Internetsite != "" {
			e.Links = append(e.Links, apiFeedLink{Rel: "alternate", Href: loc.Internetsite})
		}
		if loc.WikidataID != "" {
			e.Links = append(e.Links, apiFeedLink{Rel: "related", Href: "https://www.wikidata.org/wiki/" + loc.WikidataID})
		}
		entries = append(entries, e)
	}
	writeAtom(w, apiFeed{
		XMLNS:   "http://www.w3.org/2005/Atom",
		Title:   "Locations",
		ID:      "https://" + r.Host + "/api/v1/locations",
		Updated: atomTime(0),
		Entries: entries,
	})
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
		Location        string          `json:"location"`
		ShortName       string          `json:"short_name"`
		Address         string          `json:"address"`
		Zipcode         string          `json:"zipcode"`
		Town            string          `json:"town"`
		Country         string          `json:"country"`
		CountryCode     string          `json:"country_code"`
		Region          string          `json:"region"`
		Latitude        *float64        `json:"latitude"`
		Longitude       *float64        `json:"longitude"`
		Internetsite    string          `json:"internetsite"`
		OsmID           *int64          `json:"osm_id"`
		OsmType         string          `json:"osm_type"`
		WikidataID      string          `json:"wikidata_id"`
		MBPlaceID       string          `json:"mb_place_id"`
		OrganizationIDs []int           `json:"organization_ids"`
		NotesMd         string          `json:"notes_md"`
		Attributes      map[string]bool `json:"attributes,omitempty"`
		Parking         string          `json:"parking,omitempty"`
		FloorCondition  string          `json:"floor_condition,omitempty"`
		Aliases         []string        `json:"aliases,omitempty"`
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
	loc.WikidataID = req.WikidataID
	loc.MBPlaceID = req.MBPlaceID
	loc.OrganizationIDs = req.OrganizationIDs
	loc.NotesMd = req.NotesMd
	loc.Attributes = req.Attributes
	loc.Parking = req.Parking
	loc.FloorCondition = req.FloorCondition
	loc.Aliases = req.Aliases

	gh := ""
	if loc.Latitude != nil && loc.Longitude != nil {
		gh = geohashEncode(*loc.Latitude, *loc.Longitude, 7)
		loc.Geohash = gh
	}
	if _, err := db.Exec(
		"UPDATE locations SET location=?, short_name=?, address=?, zipcode=?, town=?, country=?, country_code=?, region=?, latitude=?, longitude=?, internetsite=?, osm_id=?, osm_type=?, geohash=?, wikidata_id=?, mb_place_id=?, notes_md=?, attributes=?, parking=?, floor_condition=?, no_street_shoes=?, aliases=?, updated_at=strftime('%s','now') WHERE id=?",
		loc.Location, loc.ShortName, loc.Address, loc.Zipcode, loc.Town, loc.Country, loc.CountryCode, loc.Region, loc.Latitude, loc.Longitude, loc.Internetsite, loc.OsmID, loc.OsmType, gh, loc.WikidataID, loc.MBPlaceID, loc.NotesMd, attrsJSON(loc.Attributes), loc.Parking, loc.FloorCondition, loc.NoStreetShoes, aliasesJSON(loc.Aliases), loc.ID,
	); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	syncLocationOrgs(loc.ID, loc.OrganizationIDs)

	json.NewEncoder(w).Encode(loc)
}

// POST /api/v1/locations/bulk-assign-org - Assign organization to locations.
// admin: any org, org_id may be nil (clears all). user: must be member of target org, org_id required.
func bulkAssignLocationOrg(w http.ResponseWriter, r *http.Request) {
	callerID, requesterRole := callerFromRequest(r)
	var req struct {
		IDs            []int `json:"ids"`
		OrganizationID *int  `json:"organization_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if requesterRole != RoleAdmin {
		if requesterRole != RoleUser {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		if req.OrganizationID == nil {
			writeError(w, "Forbidden: admin only for unassign-all", http.StatusForbidden)
			return
		}
		if !isOrgMember(callerID, *req.OrganizationID) {
			writeError(w, "Forbidden: not a member of the specified organization", http.StatusForbidden)
			return
		}
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
// POST /api/v1/locations/unassign-org — remove one org from one location.
// admin: any org. user: must be member of the specified org.
func unassignLocationOrg(w http.ResponseWriter, r *http.Request) {
	callerID, requesterRole := callerFromRequest(r)
	var req struct {
		LocationID     int `json:"location_id"`
		OrganizationID int `json:"organization_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LocationID == 0 || req.OrganizationID == 0 {
		writeError(w, "location_id and organization_id are required", http.StatusBadRequest)
		return
	}
	if requesterRole != RoleAdmin {
		if requesterRole != RoleUser {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		if !isOrgMember(callerID, req.OrganizationID) {
			writeError(w, "Forbidden: not a member of the specified organization", http.StatusForbidden)
			return
		}
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
		// Users may not delete a location that still has events assigned.
		var eventCount int
		db.QueryRow("SELECT COUNT(*) FROM events WHERE location_id=?", id).Scan(&eventCount)
		if eventCount > 0 {
			writeError(w, "Cannot delete: location has events assigned", http.StatusConflict)
			return
		}
	} else if reassignTo := r.URL.Query().Get("reassign_to"); reassignTo != "" && reassignTo != id {
		// Admin merge: reassign events and secondary-location rows before deletion
		// so that the FK constraint on events.location_id is satisfied.
		db.Exec("UPDATE events SET location_id=? WHERE location_id=?", reassignTo, id)
		db.Exec("UPDATE OR IGNORE event_locations SET location_id=? WHERE location_id=?", reassignTo, id)
		db.Exec("DELETE FROM event_locations WHERE location_id=?", id)
	}

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

// POST /api/v1/locations/{id}/assign-org — add a location to an organization.
// admin: any org. user: own orgs only.
func assignLocationOrg(w http.ResponseWriter, r *http.Request) {
	callerID, requesterRole := callerFromRequest(r)
	locID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	var req struct {
		OrganizationID int `json:"organization_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OrganizationID == 0 {
		writeError(w, "organization_id is required", http.StatusBadRequest)
		return
	}

	if requesterRole != RoleAdmin {
		if requesterRole != RoleUser {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		if !isOrgMember(callerID, req.OrganizationID) {
			writeError(w, "Forbidden: not a member of the specified organization", http.StatusForbidden)
			return
		}
	}

	var exists int
	if db.QueryRow("SELECT COUNT(*) FROM locations WHERE id=?", locID).Scan(&exists); exists == 0 {
		writeError(w, "Location not found", http.StatusNotFound)
		return
	}
	if db.QueryRow("SELECT COUNT(*) FROM organizations WHERE id=?", req.OrganizationID).Scan(&exists); exists == 0 {
		writeError(w, "Organization not found", http.StatusNotFound)
		return
	}

	if _, err := db.Exec("INSERT OR IGNORE INTO location_organizations (location_id, organization_id) VALUES (?, ?)", locID, req.OrganizationID); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/locations/merge — merge two similar locations into one.
// keep_id survives; merge_id is deleted. Events and org links are migrated.
// user: must be member of an org that owns either location.
func mergeLocations(w http.ResponseWriter, r *http.Request) {
	callerID, requesterRole := callerFromRequest(r)
	if requesterRole != RoleAdmin && requesterRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		KeepID  int `json:"keep_id"`
		MergeID int `json:"merge_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.KeepID == 0 || req.MergeID == 0 {
		writeError(w, "keep_id and merge_id are required", http.StatusBadRequest)
		return
	}
	if req.KeepID == req.MergeID {
		writeError(w, "keep_id and merge_id must differ", http.StatusBadRequest)
		return
	}

	var keep, merge Location
	if err := scanLocation(db.QueryRow(`SELECT `+locationCols+` FROM locations l LEFT JOIN location_organizations lo ON l.id=lo.location_id WHERE l.id=? GROUP BY l.id`, req.KeepID), &keep); err != nil {
		writeError(w, "keep location not found", http.StatusNotFound)
		return
	}
	if err := scanLocation(db.QueryRow(`SELECT `+locationCols+` FROM locations l LEFT JOIN location_organizations lo ON l.id=lo.location_id WHERE l.id=? GROUP BY l.id`, req.MergeID), &merge); err != nil {
		writeError(w, "merge location not found", http.StatusNotFound)
		return
	}

	// Permission: user must be member of an org owning either location.
	if requesterRole == RoleUser {
		memberOfKeep := locationHasOrgMember(keep.ID, callerID)
		memberOfMerge := locationHasOrgMember(merge.ID, callerID)
		if !memberOfKeep && !memberOfMerge {
			writeError(w, "Forbidden: not a member of either location's organization", http.StatusForbidden)
			return
		}
	}

	// Similarity gate: at least one condition must hold.
	similar := false
	if keep.Latitude != nil && keep.Longitude != nil && merge.Latitude != nil && merge.Longitude != nil {
		if haversineKm(*keep.Latitude, *keep.Longitude, *merge.Latitude, *merge.Longitude) < 0.1 {
			similar = true
		}
	}
	if keep.Latitude == nil || keep.Longitude == nil || merge.Latitude == nil || merge.Longitude == nil {
		similar = true
	}
	if !similar {
		keepBase := streetBase(keep.Address)
		mergeBase := streetBase(merge.Address)
		if keepBase != "" && keepBase == mergeBase {
			similar = true
		}
	}
	if !similar {
		kn := strings.ToLower(keep.Location)
		mn := strings.ToLower(merge.Location)
		if strings.Contains(kn, mn) || strings.Contains(mn, kn) {
			similar = true
		}
	}
	if !similar {
		writeError(w, "Locations are not similar enough to merge", http.StatusUnprocessableEntity)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Fill missing fields on keep from merge.
	if keep.Address == "" && merge.Address != "" {
		tx.Exec("UPDATE locations SET address=? WHERE id=?", merge.Address, keep.ID)
	}
	if keep.Zipcode == "" && merge.Zipcode != "" {
		tx.Exec("UPDATE locations SET zipcode=? WHERE id=?", merge.Zipcode, keep.ID)
	}
	if keep.Town == "" && merge.Town != "" {
		tx.Exec("UPDATE locations SET town=? WHERE id=?", merge.Town, keep.ID)
	}
	if keep.Country == "" && merge.Country != "" {
		tx.Exec("UPDATE locations SET country=? WHERE id=?", merge.Country, keep.ID)
	}
	if keep.Latitude == nil && merge.Latitude != nil {
		tx.Exec("UPDATE locations SET latitude=?, longitude=? WHERE id=?", merge.Latitude, merge.Longitude, keep.ID)
	}
	if keep.Internetsite == "" && merge.Internetsite != "" {
		tx.Exec("UPDATE locations SET internetsite=? WHERE id=?", merge.Internetsite, keep.ID)
	}

	// Migrate events.
	tx.Exec("UPDATE events SET location_id=? WHERE location_id=?", keep.ID, merge.ID)

	// Copy org links from merge to keep.
	rows, err := tx.Query("SELECT organization_id FROM location_organizations WHERE location_id=?", merge.ID)
	if err == nil {
		var orgIDs []int
		for rows.Next() {
			var oid int
			rows.Scan(&oid)
			orgIDs = append(orgIDs, oid)
		}
		rows.Close()
		for _, oid := range orgIDs {
			tx.Exec("INSERT OR IGNORE INTO location_organizations (location_id, organization_id) VALUES (?, ?)", keep.ID, oid)
		}
	}

	// Delete the merged location.
	tx.Exec("DELETE FROM locations WHERE id=?", merge.ID)

	if err := tx.Commit(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result := keep
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// GET /api/v1/locations/event-counts — returns a map of location_id → event count.
func locationEventCounts(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`SELECT location_id, COUNT(*) FROM events WHERE location_id IS NOT NULL GROUP BY location_id`)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	counts := map[int]int{}
	for rows.Next() {
		var id, n int
		if err := rows.Scan(&id, &n); err == nil {
			counts[id] = n
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(counts)
}
