package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	ics "github.com/arran4/golang-ical"
)

type FetchURLRequest struct {
	URL            string   `json:"url"`
	Type           string   `json:"type"`
	Tags           []string `json:"tags"`
	Organization   string   `json:"organization,omitempty"`   // find-or-create by name
	OrganizationID *int     `json:"organization_id,omitempty"` // takes precedence over Organization
}

type FetchSource struct {
	ID             int      `json:"id"`
	URL            string   `json:"url"`
	Type           string   `json:"type"`
	Tags           []string `json:"tags"`
	DanceIDs       []int    `json:"dance_ids,omitempty"`
	OrganizationID *int     `json:"organization_id,omitempty"`
	LastFetchedAt  string   `json:"last_fetched_at,omitempty"`
	LastResult     string   `json:"last_result,omitempty"`
	CreatedAt      string   `json:"created_at"`
	TemplateID     *int     `json:"template_id,omitempty"`
	TemplateMode   string   `json:"template_mode,omitempty"`
}

// fetchSourceCols is the SELECT column list for fetch_sources rows.
const fetchSourceCols = "id, url, type, tags, COALESCE((SELECT GROUP_CONCAT(dance_id) FROM fetch_source_dances WHERE fetch_source_id = id),''), organization_id, last_fetched_at, last_result, created_at, template_id, template_mode"

// templateImportData mirrors the JSON stored in event_templates.data.
type templateImportData struct {
	URL             string    `json:"url"`
	BookingURL      string    `json:"booking_url"`
	HasBall         bool      `json:"has_ball"`
	HasWorkshop     bool      `json:"has_workshop"`
	HasFestival     bool      `json:"has_festival"`
	WorkshopDiff    string    `json:"workshop_difficulty"`
	OrgID           int       `json:"org_id"`
	LocID           int       `json:"loc_id"`
	PricingType     string    `json:"pricing_type"`
	PricingAmount   float64   `json:"pricing_amount"`
	PricingCurrency string    `json:"pricing_currency"`
	PricingLines    []Price   `json:"pricing_lines"`
	Tags            []string  `json:"tags"`
	DanceIDs        []int     `json:"dance_ids"`
	Food            string    `json:"food"`
	Drink           string    `json:"drink"`
	TicketsTotal    int       `json:"tickets_total"`
	BookingEnabled  bool      `json:"booking_enabled"`
}

type uaTransport struct{ rt http.RoundTripper }

func (t uaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", "dansal/1.0 (calendar feed importer; +https://github.com/ademant/dansal)")
	}
	return t.rt.RoundTrip(req)
}

var fetchClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: uaTransport{rt: http.DefaultTransport},
}

// safeClient is used for user-supplied URLs; it blocks requests to private,
// loopback, and link-local addresses to prevent SSRF.
var safeClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: uaTransport{rt: &http.Transport{DialContext: safeDialContext}},
}

var privateRanges = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8",    // loopback
		"::1/128",        // IPv6 loopback
		"10.0.0.0/8",     // private
		"172.16.0.0/12",  // private
		"192.168.0.0/16", // private
		"169.254.0.0/16", // link-local (AWS metadata etc.)
		"fe80::/10",      // IPv6 link-local
		"fc00::/7",       // IPv6 unique-local
		"0.0.0.0/8",      // unspecified
		"100.64.0.0/10",  // shared address space (RFC 6598)
	}
	var nets []*net.IPNet
	for _, c := range cidrs {
		_, n, _ := net.ParseCIDR(c)
		nets = append(nets, n)
	}
	return nets
}()

func isPrivateIP(ip net.IP) bool {
	for _, n := range privateRanges {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if isPrivateIP(ip.IP) {
			return nil, fmt.Errorf("request to private/internal address denied")
		}
	}
	// Dial the first resolved address explicitly so we connect to what we checked.
	return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// ensureLocation returns the id of an existing location matching loc.Location,
// or inserts a new one. Returns 0 with no error when loc.Location is empty.
func ensureLocation(q querier, loc EventLocationRequest) (int64, error) {
	if loc.Location == "" {
		return 0, nil
	}
	var id int64
	err := q.QueryRow("SELECT id FROM locations WHERE location = ?", loc.Location).Scan(&id)
	if err == sql.ErrNoRows && loc.Address != "" {
		// Gancio's iCal export stores LOCATION as "name - address". When the
		// same data arrives via the JSON API (name and address separate), the
		// exact lookup above misses. Try the composite format as a fallback so
		// that JSON imports match locations originally created from the iCal feed.
		composite := loc.Location + " - " + loc.Address
		err = q.QueryRow("SELECT id FROM locations WHERE location = ?", composite).Scan(&id)
	}
	if err == nil {
		// Update coordinates whenever the feed supplies them — not only when
		// the DB has NULL. This lets corrected geodata flow in from the source.
		if loc.Latitude != nil || loc.Longitude != nil {
			q.Exec("UPDATE locations SET latitude=?, longitude=? WHERE id=?",
				loc.Latitude, loc.Longitude, id)
		}
		// For text fields keep the backfill-only policy to preserve manual edits.
		if loc.ShortName != "" || loc.Address != "" || loc.Town != "" || loc.Zipcode != "" || loc.Country != "" {
			q.Exec(`UPDATE locations SET
				short_name = COALESCE(NULLIF(short_name,''), ?),
				address    = COALESCE(NULLIF(address,''),    ?),
				town       = COALESCE(NULLIF(town,''),       ?),
				zipcode    = COALESCE(NULLIF(zipcode,''),    ?),
				country    = COALESCE(NULLIF(country,''),    ?)
				WHERE id = ?`,
				loc.ShortName, loc.Address, loc.Town, loc.Zipcode, loc.Country, id)
		}
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	result, err := q.Exec(
		"INSERT INTO locations (location, short_name, address, zipcode, town, country, latitude, longitude, internetsite) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		loc.Location, loc.ShortName, loc.Address, loc.Zipcode, loc.Town, loc.Country, loc.Latitude, loc.Longitude, loc.Eventsite,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// parseICalGeo splits a GEO property value ("lat;lon") into its components.
func parseICalGeo(s string) (lat, lon *float64) {
	if i := strings.IndexByte(s, ';'); i >= 0 {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s[:i]), 64); err == nil {
			v := f
			lat = &v
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(s[i+1:]), 64); err == nil {
			v := f
			lon = &v
		}
	}
	return
}

// parseGeoURI parses a "geo:lat,lon" URI into its components.
func parseGeoURI(s string) (lat, lon *float64) {
	s = strings.TrimPrefix(strings.ToLower(s), "geo:")
	if i := strings.IndexByte(s, ','); i >= 0 {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s[:i]), 64); err == nil {
			v := f
			lat = &v
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(s[i+1:]), 64); err == nil {
			v := f
			lon = &v
		}
	}
	return
}

// icalUnescapeText unescapes iCal text-value escaping: \, \; \\ \n \N.
func icalUnescapeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case ',':
				b.WriteByte(',')
			case ';':
				b.WriteByte(';')
			case '\\':
				b.WriteByte('\\')
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// looksLikeZipCode returns true for purely numeric strings of 4–10 digits.
func looksLikeZipCode(s string) bool {
	if len(s) < 4 || len(s) > 10 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// fillAddressFromApple parses Apple's X-ADDRESS string (already unescaped, comma-separated)
// into the structured fields of loc.  Typical format: "street, city[, state, zip, country]".
func fillAddressFromApple(addr string, loc *EventLocationRequest) {
	if addr == "" {
		return
	}
	parts := strings.Split(addr, ", ")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	n := len(parts)
	if n >= 1 {
		loc.Address = parts[0]
	}
	if n >= 2 {
		loc.Town = parts[1]
	}
	// Scan remaining parts for zip code; treat the last non-zip part as country.
	for i := 2; i < n; i++ {
		if looksLikeZipCode(parts[i]) {
			loc.Zipcode = parts[i]
		}
	}
	if n >= 3 && !looksLikeZipCode(parts[n-1]) {
		loc.Country = parts[n-1]
	}
}

// parseAppleStructuredLocation extracts location data from the non-standard
// X-APPLE-STRUCTURED-LOCATION property.  Returns nil when the property is absent.
func parseAppleStructuredLocation(vevent *ics.VEvent) *EventLocationRequest {
	prop := vevent.GetProperty(ics.ComponentProperty("X-APPLE-STRUCTURED-LOCATION"))
	if prop == nil {
		return nil
	}
	loc := &EventLocationRequest{}
	if v := prop.ICalParameters["X-TITLE"]; len(v) > 0 {
		loc.Location = icalUnescapeText(v[0])
	}
	if v := prop.ICalParameters["X-ADDRESS"]; len(v) > 0 {
		fillAddressFromApple(icalUnescapeText(v[0]), loc)
	}
	if lat, lon := parseGeoURI(prop.Value); lat != nil {
		loc.Latitude = lat
		loc.Longitude = lon
	}
	if loc.Location == "" && loc.Address == "" {
		return nil
	}
	return loc
}

// parseICalDuration parses an RFC 5545 DURATION value (e.g. "PT1H30M", "P1D")
// into a time.Duration.
func parseICalDuration(s string) (time.Duration, error) {
	orig := s
	neg := false
	s = strings.TrimPrefix(s, "+")
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	if !strings.HasPrefix(s, "P") {
		return 0, fmt.Errorf("invalid DURATION %q", orig)
	}
	s = s[1:]

	var total time.Duration
	for len(s) > 0 {
		if s[0] == 'T' {
			s = s[1:]
			continue
		}
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == 0 || i >= len(s) {
			return 0, fmt.Errorf("invalid DURATION %q", orig)
		}
		n, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, err
		}
		unit := s[i]
		s = s[i+1:]
		switch unit {
		case 'W':
			total += time.Duration(n) * 7 * 24 * time.Hour
		case 'D':
			total += time.Duration(n) * 24 * time.Hour
		case 'H':
			total += time.Duration(n) * time.Hour
		case 'M':
			total += time.Duration(n) * time.Minute
		case 'S':
			total += time.Duration(n) * time.Second
		default:
			return 0, fmt.Errorf("unknown DURATION unit %q in %q", string(unit), orig)
		}
	}
	if neg {
		total = -total
	}
	return total, nil
}

// parseICalCategories extracts all CATEGORIES values from a vevent,
// splitting comma-separated entries and deduplicating.
func parseICalCategories(event *ics.VEvent) []string {
	seen := make(map[string]bool)
	var tags []string
	for _, prop := range event.GetProperties(ics.ComponentPropertyCategories) {
		for _, cat := range strings.Split(prop.Value, ",") {
			cat = strings.TrimSpace(cat)
			if cat != "" && !seen[cat] {
				seen[cat] = true
				tags = append(tags, cat)
			}
		}
	}
	return tags
}


func scanFetchSource(s scanner) (FetchSource, error) {
	var src FetchSource
	var tagsJSON, danceIDsCSV string
	var lastFetched, lastResult sql.NullString
	var orgID, templateID sql.NullInt64
	var templateMode sql.NullString
	if err := s.Scan(&src.ID, &src.URL, &src.Type, &tagsJSON, &danceIDsCSV, &orgID, &lastFetched, &lastResult, &src.CreatedAt, &templateID, &templateMode); err != nil {
		return FetchSource{}, err
	}
	if tagsJSON != "" {
		json.Unmarshal([]byte(tagsJSON), &src.Tags)
	}
	if danceIDsCSV != "" {
		for _, part := range strings.Split(danceIDsCSV, ",") {
			if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				src.DanceIDs = append(src.DanceIDs, id)
			}
		}
	}
	if orgID.Valid {
		id := int(orgID.Int64)
		src.OrganizationID = &id
	}
	if lastFetched.Valid {
		src.LastFetchedAt = epochStrToRFC3339(lastFetched.String)
	}
	if lastResult.Valid {
		src.LastResult = lastResult.String
	}
	if templateID.Valid {
		id := int(templateID.Int64)
		src.TemplateID = &id
	}
	if templateMode.Valid {
		src.TemplateMode = templateMode.String
	}
	return src, nil
}

// upsertFetchSource inserts or updates a fetch source by URL, returning its id.
func upsertFetchSource(rawURL, typ string, tags []string, orgID *int) (int64, error) {
	tagsJSON, _ := json.Marshal(tags)
	var id int64
	err := db.QueryRow("SELECT id FROM fetch_sources WHERE url = ?", rawURL).Scan(&id)
	if err == sql.ErrNoRows {
		var orgVal any
		if orgID != nil {
			orgVal = *orgID
		}
		result, err := db.Exec(
			"INSERT INTO fetch_sources (url, type, tags, organization_id) VALUES (?, ?, ?, ?)",
			rawURL, typ, string(tagsJSON), orgVal,
		)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	}
	if err != nil {
		return 0, err
	}
	var orgVal any
	if orgID != nil {
		orgVal = *orgID
	}
	_, err = db.Exec("UPDATE fetch_sources SET type = ?, tags = ?, organization_id = ? WHERE id = ?", typ, string(tagsJSON), orgVal, id)
	return id, err
}

func setFetchSourceDances(id int, danceIDs []int) error {
	if _, err := db.Exec("DELETE FROM fetch_source_dances WHERE fetch_source_id = ?", id); err != nil {
		return err
	}
	for _, danceID := range danceIDs {
		if _, err := db.Exec("INSERT OR IGNORE INTO fetch_source_dances (fetch_source_id, dance_id) VALUES (?, ?)", id, danceID); err != nil {
			return err
		}
	}
	return nil
}

// GET /api/v1/fetchurl
func getFetchSources(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole == RolePublisher {
		writeError(w, "Forbidden: publishers may not access fetch sources", http.StatusForbidden)
		return
	}

	var rows *sql.Rows
	var err error
	if callerRole == RoleAdmin {
		rows, err = db.Query("SELECT " + fetchSourceCols + " FROM fetch_sources ORDER BY id ASC")
	} else {
		rows, err = db.Query(`
			SELECT `+fetchSourceCols+` FROM fetch_sources
			WHERE organization_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)
			ORDER BY id ASC`, callerID)
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var sources []FetchSource
	for rows.Next() {
		src, err := scanFetchSource(rows)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sources = append(sources, src)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sources)
}

// GET /api/v1/fetchurl/{id}
func getFetchSource(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole == RolePublisher {
		writeError(w, "Forbidden: publishers may not access fetch sources", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	src, err := scanFetchSource(db.QueryRow(
		"SELECT "+fetchSourceCols+" FROM fetch_sources WHERE id = ?", id,
	))
	if err == sql.ErrNoRows {
		writeError(w, "Fetch source not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if callerRole != RoleAdmin {
		if src.OrganizationID == nil || !isOrgMember(callerID, *src.OrganizationID) {
			writeError(w, "Forbidden: not a member of this source's organization", http.StatusForbidden)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(src)
}

// PATCH /api/v1/fetchurl/{id}
func patchFetchSource(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole == RolePublisher {
		writeError(w, "Forbidden: publishers may not modify fetch sources", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	src, err := scanFetchSource(db.QueryRow(
		"SELECT "+fetchSourceCols+" FROM fetch_sources WHERE id = ?", id,
	))
	if err == sql.ErrNoRows {
		writeError(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if callerRole != RoleAdmin {
		if src.OrganizationID == nil || !isOrgMember(callerID, *src.OrganizationID) {
			writeError(w, "Forbidden: not a member of this source's organization", http.StatusForbidden)
			return
		}
	}

	var req struct {
		Type           string   `json:"type"`
		Tags           []string `json:"tags"`
		DanceIDs       []int    `json:"dance_ids"`
		OrganizationID *int     `json:"organization_id"`
		TemplateID     *int     `json:"template_id"`
		TemplateMode   string   `json:"template_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}

	if req.Type != "" {
		src.Type = req.Type
	}
	if req.Tags != nil {
		src.Tags = req.Tags
	}
	src.DanceIDs = req.DanceIDs
	src.OrganizationID = req.OrganizationID
	src.TemplateID = req.TemplateID
	src.TemplateMode = req.TemplateMode

	tagsJSON, _ := json.Marshal(src.Tags)
	var orgVal any
	if src.OrganizationID != nil {
		orgVal = *src.OrganizationID
	}
	var tplVal any
	if src.TemplateID != nil {
		tplVal = *src.TemplateID
	}
	if _, err := db.Exec(
		"UPDATE fetch_sources SET type = ?, tags = ?, organization_id = ?, template_id = ?, template_mode = ? WHERE id = ?",
		src.Type, string(tagsJSON), orgVal, tplVal, src.TemplateMode, src.ID,
	); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := setFetchSourceDances(src.ID, src.DanceIDs); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(src)
}

// importFromSource dispatches to the correct importer based on src.Type.
func importFromSource(src FetchSource) ([]Event, bool, error) {
	// Defense-in-depth: re-validate scheme even though the URL was validated at
	// storage time, guarding against any future path that bypasses the handler check.
	if u, err := url.Parse(src.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, false, fmt.Errorf("fetch URL must use http or https: %q", src.URL)
	}
	switch src.Type {
	case "folkdance-json":
		return importFromFolkdanceJSON(src)
	case "gancio-json":
		return importFromGancioJSON(src)
	case "rss":
		return importFromRSSSource(src)
	default:
		return importFromICalSource(src)
	}
}

// loadTemplateForSource loads and parses the template associated with a fetch source.
// Returns nil, nil when no template is set.
func loadTemplateForSource(templateID int) (*templateImportData, error) {
	var dataJSON string
	err := db.QueryRow("SELECT data FROM event_templates WHERE id = ?", templateID).Scan(&dataJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var td templateImportData
	if err := json.Unmarshal([]byte(dataJSON), &td); err != nil {
		return nil, err
	}
	return &td, nil
}

// locationRequestByID returns an EventLocationRequest populated from a stored location.
func locationRequestByID(id int) (EventLocationRequest, bool) {
	var loc EventLocationRequest
	err := db.QueryRow(
		`SELECT COALESCE(location,''), COALESCE(short_name,''), COALESCE(address,''), COALESCE(zipcode,''),
		 COALESCE(town,''), COALESCE(country,''), latitude, longitude
		 FROM locations WHERE id = ?`, id,
	).Scan(&loc.Location, &loc.ShortName, &loc.Address, &loc.Zipcode,
		&loc.Town, &loc.Country, &loc.Latitude, &loc.Longitude)
	if err != nil {
		return EventLocationRequest{}, false
	}
	return loc, true
}

// applyTemplateToRequest applies template data to an EventCreateRequest.
// mode "fetch_master": template fills in only zero/empty fields.
// mode "template_master": template overrides all fields.
func applyTemplateToRequest(req *EventCreateRequest, td templateImportData, mode string) {
	override := mode == "template_master"

	if td.OrgID != 0 && (override || req.OrganizationID == nil) {
		id := td.OrgID
		req.OrganizationID = &id
	}
	if td.LocID != 0 && (override || req.Location.Location == "") {
		if loc, ok := locationRequestByID(td.LocID); ok {
			req.Location = loc
		}
	}
	if td.URL != "" && (override || req.URL == "") {
		req.URL = td.URL
	}
	if td.BookingURL != "" && (override || req.BookingURL == "") {
		req.BookingURL = td.BookingURL
	}
	if override || (!req.HasBall && !req.HasWorkshop && !req.HasFestival) {
		req.HasBall = td.HasBall
		req.HasWorkshop = td.HasWorkshop
		req.HasFestival = td.HasFestival
	}
	if td.WorkshopDiff != "" && (override || req.WorkshopDifficulty == "") {
		req.WorkshopDifficulty = td.WorkshopDiff
	}
	if td.Food != "" && (override || req.Food == "") {
		req.Food = td.Food
	}
	if td.Drink != "" && (override || req.Drink == "") {
		req.Drink = td.Drink
	}
	if len(td.Tags) > 0 && (override || len(req.Tags) == 0) {
		if override {
			req.Tags = td.Tags
		} else {
			seen := make(map[string]bool)
			for _, t := range req.Tags {
				seen[t] = true
			}
			for _, t := range td.Tags {
				if !seen[t] {
					req.Tags = append(req.Tags, t)
				}
			}
		}
	}
	if len(td.DanceIDs) > 0 && (override || len(req.Dances) == 0) {
		req.Dances = td.DanceIDs
	}
	if td.PricingType != "" && (override || req.Pricing == nil) {
		req.Pricing = &Pricing{
			Type:     td.PricingType,
			Amount:   td.PricingAmount,
			Currency: td.PricingCurrency,
			Prices:   td.PricingLines,
		}
	}
}

// parseICalToRequests converts a parsed iCal calendar to EventCreateRequests
// without touching the database. Used by the preview endpoint.
func parseICalToRequests(cal *ics.Calendar, src FetchSource) []EventCreateRequest {
	var reqs []EventCreateRequest
	now := time.Now().UTC()

	for _, vevent := range cal.Events() {
		prop := func(p ics.ComponentProperty) string {
			if v := vevent.GetProperty(p); v != nil {
				return v.Value
			}
			return ""
		}

		startT, err := vevent.GetStartAt()
		if err != nil {
			continue
		}
		endT := startT
		if et, err := vevent.GetEndAt(); err == nil {
			endT = et
		} else if durStr := prop(ics.ComponentPropertyDuration); durStr != "" {
			if d, err := parseICalDuration(durStr); err == nil {
				endT = startT.Add(d)
			}
		}

		title := prop(ics.ComponentPropertySummary)
		if title == "" {
			continue
		}

		tags := parseICalCategories(vevent)
		seen := make(map[string]bool)
		for _, t := range tags {
			seen[t] = true
		}
		for _, t := range src.Tags {
			if !seen[t] {
				tags = append(tags, t)
			}
		}
		tags = filterKnownTags(tags)

		baseUID := prop(ics.ComponentPropertyUniqueId)
		sourceLastModified := icalLastModified(vevent)

		occs, _ := expandRRuleOccurrences(vevent, startT, endT)
		if occs == nil {
			occs = [][2]time.Time{{startT, endT}}
		}

		for _, occ := range occs {
			if occ[1].Before(now) {
				continue
			}
			uid := baseUID
			if len(occs) > 1 && !occ[0].Equal(startT) {
				uid = fmt.Sprintf("%s_%d", baseUID, occ[0].UTC().Unix())
			}

			var loc EventLocationRequest
			if apple := parseAppleStructuredLocation(vevent); apple != nil {
				if apple.Location == "" {
					apple.Location = prop(ics.ComponentPropertyLocation)
				}
				if apple.Latitude == nil {
					apple.Latitude, apple.Longitude = parseICalGeo(prop(ics.ComponentPropertyGeo))
				}
				loc = *apple
			} else {
				lat, lon := parseICalGeo(prop(ics.ComponentPropertyGeo))
				loc = EventLocationRequest{
					Location:  prop(ics.ComponentPropertyLocation),
					Latitude:  lat,
					Longitude: lon,
				}
			}

			reqs = append(reqs, EventCreateRequest{
				UID:                uid,
				Title:              title,
				Description:        prop(ics.ComponentPropertyDescription),
				StartTime:          occ[0].UTC().Format(time.RFC3339),
				EndTime:            occ[1].UTC().Format(time.RFC3339),
				IsCancelled:        prop(ics.ComponentPropertyStatus) == "CANCELLED",
				Tags:               tags,
				URL:                attachURL(vevent),
				Source:             src.URL,
				OrganizationID:     src.OrganizationID,
				Dances:             src.DanceIDs,
				Location:           loc,
				SourceLastModified: sourceLastModified,
				FetchSourceID:      src.ID,
			})
		}
	}
	return reqs
}

// importFromICalSource fetches an iCal URL and imports its events into the DB.
func importFromICalSource(src FetchSource) ([]Event, bool, error) {
	resp, err := fetchClient.Get(src.URL)
	if err != nil {
		return nil, false, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("remote returned %d", resp.StatusCode)
	}

	cal, err := ics.ParseCalendar(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("parse iCal: %w", err)
	}

	db.Exec("UPDATE fetch_sources SET last_fetched_at = ? WHERE id = ?", time.Now().UTC().Unix(), src.ID)

	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	var allEvents []Event
	allCreated := true
	now := time.Now().UTC()

	for _, vevent := range cal.Events() {
		prop := func(p ics.ComponentProperty) string {
			if v := vevent.GetProperty(p); v != nil {
				return v.Value
			}
			return ""
		}

		startT, err := vevent.GetStartAt()
		if err != nil {
			continue
		}
		endT := startT
		if et, err := vevent.GetEndAt(); err == nil {
			endT = et
		} else if durStr := prop(ics.ComponentPropertyDuration); durStr != "" {
			if d, err := parseICalDuration(durStr); err == nil {
				endT = startT.Add(d)
			}
		}

		title := prop(ics.ComponentPropertySummary)
		if title == "" {
			continue
		}

		tags := parseICalCategories(vevent)
		seen := make(map[string]bool)
		for _, t := range tags {
			seen[t] = true
		}
		for _, t := range src.Tags {
			if !seen[t] {
				tags = append(tags, t)
			}
		}
		tags = filterKnownTags(tags)

		baseUID := prop(ics.ComponentPropertyUniqueId)
		sourceLastModified := icalLastModified(vevent)

		// Expand RRULE if present; fall back to the single base occurrence.
		occs, _ := expandRRuleOccurrences(vevent, startT, endT)
		if occs == nil {
			occs = [][2]time.Time{{startT, endT}}
		}

		for _, occ := range occs {
			if occ[1].Before(now) {
				continue
			}
			// Recurring occurrences after the base get a timestamp-qualified UID
			// so each instance deduplicates independently across re-imports.
			uid := baseUID
			if len(occs) > 1 && !occ[0].Equal(startT) {
				uid = fmt.Sprintf("%s_%d", baseUID, occ[0].UTC().Unix())
			}

			orgID := src.OrganizationID
			if orgID == nil {
				orgID = ensureOrgFromOrganizer(vevent)
			}
			eventReq := EventCreateRequest{
				UID:                uid,
				Title:              title,
				Description:        prop(ics.ComponentPropertyDescription),
				StartTime:          occ[0].UTC().Format(time.RFC3339),
				EndTime:            occ[1].UTC().Format(time.RFC3339),
				IsCancelled:        prop(ics.ComponentPropertyStatus) == "CANCELLED",
				Tags:               tags,
				URL:                attachURL(vevent),
				Source:             src.URL,
				OrganizationID:     orgID,
				Dances:             src.DanceIDs,
				Location: func() EventLocationRequest {
				if apple := parseAppleStructuredLocation(vevent); apple != nil {
					if apple.Location == "" {
						apple.Location = prop(ics.ComponentPropertyLocation)
					}
					if apple.Latitude == nil {
						apple.Latitude, apple.Longitude = parseICalGeo(prop(ics.ComponentPropertyGeo))
					}
					return *apple
				}
				lat, lon := parseICalGeo(prop(ics.ComponentPropertyGeo))
				return EventLocationRequest{
					Location:  prop(ics.ComponentPropertyLocation),
					Latitude:  lat,
					Longitude: lon,
				}
			}(),
				SourceLastModified: sourceLastModified,
				FetchSourceID:      src.ID,
			}

			if src.TemplateID != nil {
				if td, err := loadTemplateForSource(*src.TemplateID); err == nil && td != nil {
					applyTemplateToRequest(&eventReq, *td, src.TemplateMode)
				}
			}

			locationID, err := ensureLocation(tx, eventReq.Location)
			if err != nil {
				return nil, false, err
			}

			events, created, err := createEventFromRequest(tx, eventReq, locationID, true, nil)
			if err != nil {
				return nil, false, err
			}
			if !created {
				allCreated = false
			}
			for _, ev := range events {
				attachImagesFromICalEvent(ev.ID, vevent)
			}
			allEvents = append(allEvents, events...)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return allEvents, allCreated, nil
}

// POST /api/v1/fetchurl
func fetchURL(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole == RolePublisher {
		writeError(w, "Forbidden: publishers may not create fetch sources", http.StatusForbidden)
		return
	}
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req FetchURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if !strings.Contains(req.URL, "://") {
		req.URL = "https://" + req.URL
	}
	parsed, err := url.Parse(req.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		writeError(w, "URL must use http or https scheme", http.StatusBadRequest)
		return
	}

	if req.Type == "" {
		req.Type = detectFetchType(req.URL)
	}
	if !validFetchType(req.Type) {
		writeError(w, "Unsupported type; use 'ical', 'rss', or 'folkdance-json'", http.StatusBadRequest)
		return
	}

	if req.OrganizationID == nil && req.Organization != "" {
		req.OrganizationID = ensureOrgByName(req.Organization)
	}
	if callerRole != RoleAdmin {
		if req.OrganizationID == nil || !isOrgMember(callerID, *req.OrganizationID) {
			writeError(w, "Forbidden: organization_id must be one you belong to", http.StatusForbidden)
			return
		}
	}

	sourceID, err := upsertFetchSource(req.URL, req.Type, req.Tags, req.OrganizationID)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	src := FetchSource{ID: int(sourceID), URL: req.URL, Type: req.Type, Tags: req.Tags, OrganizationID: req.OrganizationID}
	allEvents, allCreated, err := importFromSource(src)
	if err != nil {
		db.Exec("UPDATE fetch_sources SET last_result = ? WHERE id = ?", "error: "+err.Error(), src.ID)
		writeError(w, err.Error(), http.StatusBadGateway)
		return
	}
	db.Exec("UPDATE fetch_sources SET last_result = ? WHERE id = ?", fmt.Sprintf("%d", len(allEvents)), src.ID)

	w.Header().Set("Content-Type", "application/json")
	if allCreated && len(allEvents) > 0 {
		w.WriteHeader(http.StatusCreated)
	}
	json.NewEncoder(w).Encode(allEvents)
}

type BulkSourceResult struct {
	URL        string `json:"url"`
	SourceID   int    `json:"source_id"`
	Events     int    `json:"events"`
	AllCreated bool   `json:"all_created"`
	Error      string `json:"error,omitempty"`
}

// DELETE /api/v1/fetchurl/{id}
func deleteFetchSource(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole == RolePublisher {
		writeError(w, "Forbidden: publishers may not delete fetch sources", http.StatusForbidden)
		return
	}
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if callerRole != RoleAdmin {
		src, err := scanFetchSource(db.QueryRow("SELECT "+fetchSourceCols+" FROM fetch_sources WHERE id = ?", id))
		if err == sql.ErrNoRows {
			writeError(w, "Fetch source not found", http.StatusNotFound)
			return
		} else if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if src.OrganizationID == nil || !isOrgMember(callerID, *src.OrganizationID) {
			writeError(w, "Forbidden: not a member of this source's organization", http.StatusForbidden)
			return
		}
	}
	result, err := db.Exec("DELETE FROM fetch_sources WHERE id = ?", id)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Fetch source not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/fetchurl/bulk-delete
func bulkDeleteFetchSources(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole == RolePublisher {
		writeError(w, "Forbidden: publishers may not delete fetch sources", http.StatusForbidden)
		return
	}
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		IDs []int `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		writeError(w, "ids required", http.StatusBadRequest)
		return
	}
	memberOrgs := userOrgSet(callerID)
	for _, id := range req.IDs {
		if callerRole != RoleAdmin {
			src, err := scanFetchSource(db.QueryRow("SELECT "+fetchSourceCols+" FROM fetch_sources WHERE id = ?", id))
			if err != nil || src.OrganizationID == nil || !memberOrgs[*src.OrganizationID] {
				continue
			}
		}
		db.Exec("DELETE FROM fetch_sources WHERE id = ?", id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/fetchurl/bulk-fetch
func bulkFetchURLsByIDs(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole == RolePublisher {
		writeError(w, "Forbidden: publishers may not access fetch sources", http.StatusForbidden)
		return
	}
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		IDs []int `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		writeError(w, "ids required", http.StatusBadRequest)
		return
	}

	placeholders := make([]string, len(req.IDs))
	args := make([]any, len(req.IDs))
	for i, id := range req.IDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := "SELECT " + fetchSourceCols + " FROM fetch_sources WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := db.Query(query, args...)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	memberOrgs := userOrgSet(callerID)
	var sources []FetchSource
	for rows.Next() {
		src, err := scanFetchSource(rows)
		if err != nil {
			continue
		}
		if callerRole != RoleAdmin {
			if src.OrganizationID == nil || !memberOrgs[*src.OrganizationID] {
				continue
			}
		}
		sources = append(sources, src)
	}

	results := make([]BulkSourceResult, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(i int, src FetchSource) {
			defer wg.Done()
			events, allCreated, err := importFromSource(src)
			if err != nil {
				results[i] = BulkSourceResult{URL: src.URL, SourceID: src.ID, Error: err.Error()}
				return
			}
			results[i] = BulkSourceResult{URL: src.URL, SourceID: src.ID, Events: len(events), AllCreated: allCreated}
		}(i, src)
	}
	wg.Wait()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// POST /api/v1/fetchurl/bulk-assign-org
func bulkAssignFetchSourceOrg(w http.ResponseWriter, r *http.Request) {
	userRole := r.Header.Get("X-User-Role")
	if userRole != RoleAdmin {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		IDs            []int `json:"ids"`
		OrganizationID *int  `json:"organization_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		writeError(w, "ids required", http.StatusBadRequest)
		return
	}
	for _, id := range req.IDs {
		db.Exec("UPDATE fetch_sources SET organization_id = ? WHERE id = ?", req.OrganizationID, id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/fetchurl/{id}/fetch
func fetchURLByID(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole == RolePublisher {
		writeError(w, "Forbidden: publishers may not trigger fetch sources", http.StatusForbidden)
		return
	}
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	src, err := scanFetchSource(db.QueryRow(
		"SELECT "+fetchSourceCols+" FROM fetch_sources WHERE id = ?", id,
	))
	if err == sql.ErrNoRows {
		writeError(w, "Fetch source not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if callerRole != RoleAdmin {
		if src.OrganizationID == nil || !isOrgMember(callerID, *src.OrganizationID) {
			writeError(w, "Forbidden: not a member of this source's organization", http.StatusForbidden)
			return
		}
	}

	if !validFetchType(src.Type) {
		writeError(w, "Unsupported type; use 'ical', 'rss', or 'folkdance-json'", http.StatusBadRequest)
		return
	}

	allEvents, allCreated, err := importFromSource(src)
	if err != nil {
		db.Exec("UPDATE fetch_sources SET last_result = ? WHERE id = ?", "error: "+err.Error(), src.ID)
		writeError(w, err.Error(), http.StatusBadGateway)
		return
	}
	db.Exec("UPDATE fetch_sources SET last_result = ? WHERE id = ?", fmt.Sprintf("%d", len(allEvents)), src.ID)

	w.Header().Set("Content-Type", "application/json")
	if allCreated && len(allEvents) > 0 {
		w.WriteHeader(http.StatusCreated)
	}
	json.NewEncoder(w).Encode(allEvents)
}
