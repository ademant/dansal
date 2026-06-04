package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	ics "github.com/arran4/golang-ical"
)

// querier is satisfied by both *sql.DB and *sql.Tx, allowing helpers to
// participate in a caller-managed transaction without changing their signature.
type querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

type Event struct {
	ID                 int              `json:"id"`
	UID                string           `json:"uid,omitempty"`
	Title              string           `json:"title"`
	Description        string           `json:"description"`
	StartTime          string           `json:"start_time"`
	EndTime            string           `json:"end_time"`
	HasBall            bool             `json:"has_ball"`
	HasWorkshop        bool             `json:"has_workshop"`
	HasFestival        bool             `json:"has_festival"`
	WorkshopDifficulty string           `json:"workshop_difficulty,omitempty"`
	IsCancelled        bool             `json:"is_cancelled"`
	Tags               []string         `json:"tags"`
	IsPublished        bool             `json:"is_published"`
	ShortCode          string           `json:"short_code"`
	URL                string           `json:"url,omitempty"`
	Source             string           `json:"source,omitempty"`
	CreatedAt          string           `json:"created_at"`
	ImageURL           string           `json:"image_url,omitempty"`
	OrganizationID     *int             `json:"organization_id,omitempty"`
	Editable           *bool            `json:"editable,omitempty"`
	Cancelable         *bool            `json:"cancelable,omitempty"`
	CreatedByID        *int             `json:"created_by_id,omitempty"`
	Timetable          []TimetableEntry `json:"timetable,omitempty"`
	Pricing            *Pricing         `json:"pricing,omitempty"`
	Locations          []Location       `json:"locations,omitempty"`
	Musicians          []Musician       `json:"musicians,omitempty"`
	LocationID         *int             `json:"location_id,omitempty"`
	Location           *Location        `json:"location,omitempty"`
	Attributes         map[string]bool  `json:"attributes,omitempty"`
	FloorCondition     string           `json:"floor_condition,omitempty"`
	ContactName        string           `json:"contact_name,omitempty"`
	ContactEmail       string           `json:"contact_email,omitempty"`
	BookingURL         string           `json:"booking_url,omitempty"`
	Availability       string           `json:"availability,omitempty"`
	TicketsTotal       int              `json:"tickets_total,omitempty"`
	BookingEnabled     bool             `json:"booking_enabled,omitempty"`
	Food               string           `json:"food,omitempty"`
	Drink              string           `json:"drink,omitempty"`
	DanceNames         []string         `json:"dance_names,omitempty"`
	ChangedAt          string           `json:"changed_at,omitempty"`
	ChangedBy          string           `json:"changed_by,omitempty"`
	FetchSourceID      int              `json:"fetch_source_id,omitempty"`
	SeriesID           *int             `json:"series_id,omitempty"`
	TagsJSON           string           `json:"-"`
	PricingJSON        string           `json:"-"`
}

type EventDate struct {
	Description string `json:"description"`
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
}

// EventWriteRequest holds the fields shared by both create and update requests.
type EventWriteRequest struct {
	Title              string               `json:"title"`
	Description        string               `json:"description"`
	StartTime          string               `json:"start_time"`
	EndTime            string               `json:"end_time"`
	HasBall            bool                 `json:"has_ball"`
	HasWorkshop        bool                 `json:"has_workshop"`
	HasFestival        bool                 `json:"has_festival"`
	WorkshopDifficulty string               `json:"workshop_difficulty,omitempty"`
	IsCancelled        bool                 `json:"is_cancelled"`
	IsPublished        bool                 `json:"is_published"`
	Tags               []string             `json:"tags"`
	URL                string               `json:"url"`
	OrganizationID     *int                 `json:"organization_id"`
	LocationID         *int                 `json:"location_id,omitempty"`
	Location           EventLocationRequest `json:"location"`
	Pricing            *Pricing             `json:"pricing"`
	Musicians          []int                `json:"musicians"`
	Dances             []int                `json:"dances,omitempty"`
	BookingURL         string               `json:"booking_url,omitempty"`
	Availability       string               `json:"availability,omitempty"`
	TicketsTotal       int                  `json:"tickets_total,omitempty"`
	BookingEnabled     bool                 `json:"booking_enabled,omitempty"`
	Food               string               `json:"food,omitempty"`
	Drink              string               `json:"drink,omitempty"`
	FloorCondition     string               `json:"floor_condition,omitempty"`
	Attributes         map[string]bool      `json:"attributes,omitempty"`
	ContactName        string               `json:"contact_name,omitempty"`
	ContactEmail       string               `json:"contact_email,omitempty"`
}

type EventUpdateRequest struct {
	EventWriteRequest
}

type EventCreateRequest struct {
	EventWriteRequest
	UID                string      `json:"uid,omitempty"`
	Date               []EventDate `json:"date"`
	Source             string      `json:"source,omitempty"`
	SourceLastModified int64       `json:"source_last_modified,omitempty"`
	FetchSourceID      int         `json:"fetch_source_id,omitempty"`
	DuplicateStatus    string      `json:"duplicate_status,omitempty"`
}

type EventLocationRequest struct {
	Location  string   `json:"location"`
	ShortName string   `json:"short_name,omitempty"`
	Address   string   `json:"address"`
	Zipcode   string   `json:"zipcode"`
	Town      string   `json:"town"`
	Country   string   `json:"country"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	Eventsite string   `json:"eventsite"`
}

// Price is one entry in a multi-tier pricing list.
type Price struct {
	Label  string  `json:"label"`
	Amount float64 `json:"amount"`
}

// Pricing describes the admission cost for an event.
// Type must be one of: "free", "donation", "single", "multiple".
// Amount is used for type "single"; Prices is used for type "multiple".
// Currency is optional (ISO 4217, e.g. "EUR").
type Pricing struct {
	Type     string  `json:"type"`
	Amount   float64 `json:"amount,omitempty"`
	Currency string  `json:"currency,omitempty"`
	Prices   []Price `json:"prices,omitempty"`
}

// resolveLocationID returns the location ID to use for a write request.
// If location_id is supplied directly it is used without a DB round-trip.
// Otherwise ensureLocation does the find-or-create lookup.
func resolveLocationID(q querier, locID *int, loc EventLocationRequest) (int64, error) {
	if locID != nil {
		return int64(*locID), nil
	}
	return ensureLocation(q, loc)
}

// ── package-level state ────────────────────────────────────────────────────

var berlinLoc *time.Location

var timeFormats = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
}

// SELECT used by all event list / single-event queries.
// Dance names are aggregated once via a derived table JOIN rather than a
// correlated subquery, so GROUP_CONCAT runs O(n) total instead of O(n) per row.
const eventListSelect = `SELECT e.id, e.uid, e.title, e.description, e.start_time, e.end_time, e.has_ball, e.has_workshop, e.has_festival, e.is_cancelled, COALESCE((SELECT GROUP_CONCAT(et.tag, ',') FROM event_tags et WHERE et.event_id = e.id), ''), e.is_published, COALESCE(e.short_code,''), COALESCE(e.url,''), COALESCE(e.source,''), e.created_at, COALESCE(l.location,''), COALESCE(l.short_name,''), COALESCE(l.address,''), COALESCE(l.zipcode,''), e.organization_id, COALESCE(e.pricing,''), e.location_id, COALESCE(l.town,''), COALESCE(l.country,''), l.latitude, l.longitude, COALESCE(e.workshop_difficulty,''), COALESCE(e.booking_url,''), COALESCE(e.availability,''), COALESCE(e.tickets_total,0), COALESCE(e.booking_enabled,0), COALESCE(dn.dance_names,''), COALESCE(e.changed_at,0), COALESCE(e.changed_by,''), COALESCE(e.fetch_source_id,0), COALESCE(e.food,''), COALESCE(e.drink,''), COALESCE(l.attributes,'{}'), COALESCE(e.attributes,'{}'), COALESCE(NULLIF(e.contact_name,''), o.contact_name, ''), COALESCE(NULLIF(e.contact_email,''), o.contact_email, ''), COALESCE(l.parking,''), COALESCE(l.floor_condition,''), COALESCE(e.floor_condition,''), e.created_by_id, l.osm_id, COALESCE(l.osm_type,''), COALESCE(l.geohash,'') FROM events e LEFT JOIN locations l ON e.location_id = l.id LEFT JOIN (SELECT ed.event_id, GROUP_CONCAT(d.name,',') AS dance_names FROM event_dances ed JOIN dances d ON d.id=ed.dance_id GROUP BY ed.event_id) dn ON dn.event_id = e.id LEFT JOIN organizations o ON e.organization_id = o.id`

// ── low-level helpers ──────────────────────────────────────────────────────

func epochToLocal(epoch int64) string {
	return time.Unix(epoch, 0).In(berlinLoc).Format(time.RFC3339)
}

func parseTimeToUnix(s string) (int64, error) {
	for _, layout := range timeFormats {
		// RFC3339 carries its own offset; naive layouts have no zone and must be
		// treated as local (Berlin) time to match how events are displayed.
		if layout == time.RFC3339 {
			if t, err := time.Parse(layout, s); err == nil {
				return t.Unix(), nil
			}
		} else {
			if t, err := time.ParseInLocation(layout, s, berlinLoc); err == nil {
				return t.Unix(), nil
			}
		}
	}
	return 0, fmt.Errorf("unrecognised time format: %q", s)
}

// boolParam converts a "true"/"false" query param string to a SQLite integer.
func boolParam(s string) int {
	if s == "true" {
		return 1
	}
	return 0
}

// eventImageURL returns the API path for an event's image if the cache knows one exists.
func eventImageURL(id int) string {
	if hasImage(id) {
		return fmt.Sprintf("/api/v1/images/%d", id)
	}
	return ""
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanEventRow decodes one row from the eventListSelect query.
func scanEventRow(s scanner) (Event, error) {
	var event Event
	var loc Location
	var hasBallInt, hasWorkshopInt, hasFestivalInt, isCancelledInt, isPublishedInt, bookingEnabledInt int
	var locAttrsJSON, evtAttrsJSON string
	var startEpoch, endEpoch, changedAtEpoch int64
	var orgID, locID sql.NullInt64
	var uid sql.NullString
	var danceNamesCSV string
	var locLat, locLng sql.NullFloat64
	var createdByID sql.NullInt64
	if err := s.Scan(&event.ID, &uid, &event.Title, &event.Description, &startEpoch, &endEpoch,
		&hasBallInt, &hasWorkshopInt, &hasFestivalInt, &isCancelledInt, &event.TagsJSON, &isPublishedInt,
		&event.ShortCode, &event.URL, &event.Source, &event.CreatedAt, &loc.Location,
		&loc.ShortName, &loc.Address, &loc.Zipcode, &orgID,
		&event.PricingJSON, &locID, &loc.Town, &loc.Country,
		&locLat, &locLng, &event.WorkshopDifficulty, &event.BookingURL,
		&event.Availability, &event.TicketsTotal, &bookingEnabledInt, &danceNamesCSV,
		&changedAtEpoch, &event.ChangedBy, &event.FetchSourceID, &event.Food, &event.Drink,
		&locAttrsJSON, &evtAttrsJSON,
		&event.ContactName, &event.ContactEmail,
		&loc.Parking, &loc.FloorCondition, &event.FloorCondition,
		&createdByID, &loc.OsmID, &loc.OsmType, &loc.Geohash); err != nil {
		return Event{}, err
	}
	if createdByID.Valid {
		v := int(createdByID.Int64)
		event.CreatedByID = &v
	}
	if changedAtEpoch > 0 {
		event.ChangedAt = epochToLocal(changedAtEpoch)
	}
	if uid.Valid {
		event.UID = uid.String
	}
	event.StartTime = epochToLocal(startEpoch)
	event.EndTime = epochToLocal(endEpoch)
	event.HasBall = hasBallInt == 1
	event.HasWorkshop = hasWorkshopInt == 1
	event.HasFestival = hasFestivalInt == 1
	event.IsCancelled = isCancelledInt == 1
	event.IsPublished = isPublishedInt == 1
	event.BookingEnabled = bookingEnabledInt == 1
	if evtAttrsJSON != "" && evtAttrsJSON != "{}" {
		json.Unmarshal([]byte(evtAttrsJSON), &event.Attributes)
	}
	event.ImageURL = eventImageURL(event.ID)
	if orgID.Valid {
		v := int(orgID.Int64)
		event.OrganizationID = &v
	}
	if locID.Valid {
		id := int(locID.Int64)
		event.LocationID = &id
		loc.ID = id
		if locLat.Valid {
			v := locLat.Float64
			loc.Latitude = &v
		}
		if locLng.Valid {
			v := locLng.Float64
			loc.Longitude = &v
		}
		if locAttrsJSON != "" && locAttrsJSON != "{}" {
			json.Unmarshal([]byte(locAttrsJSON), &loc.Attributes)
		}
		if loc.Geohash == "" && loc.Latitude != nil && loc.Longitude != nil {
			loc.Geohash = geohashEncode(*loc.Latitude, *loc.Longitude, 7)
		}
		event.Location = &loc
	}
	if event.TagsJSON != "" {
		event.Tags = strings.Split(event.TagsJSON, ",")
	}
	if event.PricingJSON != "" {
		var p Pricing
		if json.Unmarshal([]byte(event.PricingJSON), &p) == nil {
			event.Pricing = &p
		}
	}
	if danceNamesCSV != "" {
		event.DanceNames = strings.Split(danceNamesCSV, ",")
	}
	return event, nil
}

// fetchEventByID loads a single event by primary key using the shared eventListSelect query.
func fetchEventByID(q querier, id int) (Event, error) {
	return scanEventRow(q.QueryRow(eventListSelect+" WHERE e.id = ?", id))
}

// ── iCal helpers ───────────────────────────────────────────────────────────

// attachURL returns the canonical event URL from a vevent.
// The iCal URL: property is preferred; ATTACH http(s) values are the fallback.
func attachURL(event *ics.VEvent) string {
	if p := event.GetProperty(ics.ComponentPropertyUrl); p != nil {
		v := strings.TrimSpace(p.Value)
		if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
			return v
		}
	}
	for _, prop := range event.GetProperties(ics.ComponentPropertyAttach) {
		if strings.HasPrefix(prop.Value, "http://") || strings.HasPrefix(prop.Value, "https://") {
			return prop.Value
		}
	}
	return ""
}

// addEventToCalendar appends one Event to an iCal calendar object.
func addEventToCalendar(cal *ics.Calendar, event Event) {
	vevent := cal.AddEvent(fmt.Sprintf("event-%d@go-calendar", event.ID))
	vevent.SetSummary(event.Title)
	if event.Description != "" {
		vevent.SetDescription(event.Description)
	}
	if start, err := time.Parse(time.RFC3339, event.StartTime); err == nil {
		vevent.SetProperty(ics.ComponentPropertyDtStart, start.UTC().Format("20060102T150405Z"))
	}
	if end, err := time.Parse(time.RFC3339, event.EndTime); err == nil {
		vevent.SetProperty(ics.ComponentPropertyDtEnd, end.UTC().Format("20060102T150405Z"))
	}
	if event.Location != nil && event.Location.Location != "" {
		vevent.SetLocation(event.Location.Location)
	}
}

// ── query-building helpers ─────────────────────────────────────────────────

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// parseCountryCodes splits a comma-separated country param and validates each code.
func parseCountryCodes(param string) ([]string, error) {
	if param == "" {
		return nil, nil
	}
	parts := strings.Split(param, ",")
	codes := make([]string, 0, len(parts))
	for _, p := range parts {
		code := strings.TrimSpace(p)
		if !validCountryCode(code) || code == "" {
			return nil, fmt.Errorf("invalid country_code %q: must be 2 uppercase letters (e.g. 'DE')", code)
		}
		codes = append(codes, code)
	}
	return codes, nil
}

// applyEventFilters appends shared WHERE clauses from query parameters.
func applyEventFilters(r *http.Request, query *string, args *[]any) error {
	q := r.URL.Query()

	if title := q.Get("title"); title != "" {
		*query += " AND e.title LIKE ?"
		*args = append(*args, "%"+title+"%")
	}
	if desc := q.Get("description"); desc != "" {
		*query += " AND e.description LIKE ?"
		*args = append(*args, "%"+desc+"%")
	}
	if v := q.Get("start_time_after"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			*query += " AND e.start_time > ?"
			*args = append(*args, n)
		}
	}
	if v := q.Get("start_time_before"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			*query += " AND e.start_time < ?"
			*args = append(*args, n)
		}
	}
	if loc := q.Get("location"); loc != "" {
		*query += " AND l.location LIKE ?"
		*args = append(*args, "%"+loc+"%")
	}
	// ?has_ball, ?has_workshop, ?has_festival are kept as aliases for backward
	// compatibility; they map to their canonical tag equivalents.
	if q.Get("has_ball") == "true" {
		*query += " AND EXISTS (SELECT 1 FROM event_tags et WHERE et.event_id = e.id AND et.tag = 'bal-folk')"
	}
	if q.Get("has_workshop") == "true" {
		*query += " AND EXISTS (SELECT 1 FROM event_tags et WHERE et.event_id = e.id AND et.tag = 'workshop')"
	}
	if q.Get("has_festival") == "true" {
		*query += " AND EXISTS (SELECT 1 FROM event_tags et WHERE et.event_id = e.id AND et.tag = 'festival')"
	}
	if tag := q.Get("tag"); tag != "" {
		*query += " AND EXISTS (SELECT 1 FROM event_tags et WHERE et.event_id = e.id AND et.tag = ?)"
		*args = append(*args, tag)
	}
	if country := q.Get("country"); country != "" {
		codes, err := parseCountryCodes(country)
		if err != nil {
			return err
		}
		placeholders := strings.Repeat("?,", len(codes))
		placeholders = placeholders[:len(placeholders)-1]
		*query += " AND l.country_code IN (" + placeholders + ")"
		for _, c := range codes {
			*args = append(*args, c)
		}
	}
	if v := q.Get("musician_id"); v != "" {
		*query += " AND EXISTS (SELECT 1 FROM event_musicians em WHERE em.event_id = e.id AND em.musician_id = ?)"
		*args = append(*args, v)
	}
	if dance := q.Get("dance"); dance != "" {
		*query += " AND EXISTS (SELECT 1 FROM event_dances ed JOIN dances d ON d.id=ed.dance_id WHERE ed.event_id=e.id AND d.name=?)"
		*args = append(*args, dance)
	}
	if latStr, lonStr, radStr := q.Get("lat"), q.Get("lon"), q.Get("radius_km"); latStr != "" && lonStr != "" && radStr != "" {
		lat, latErr := strconv.ParseFloat(latStr, 64)
		lon, lonErr := strconv.ParseFloat(lonStr, 64)
		radius, radErr := strconv.ParseFloat(radStr, 64)
		if latErr == nil && lonErr == nil && radErr == nil && radius > 0 {
			latDelta := radius / 111.0
			lonDelta := radius / (111.0 * math.Cos(lat*math.Pi/180))
			*query += " AND l.latitude BETWEEN ? AND ? AND l.longitude BETWEEN ? AND ?"
			*args = append(*args, lat-latDelta, lat+latDelta, lon-lonDelta, lon+lonDelta)
		}
	}
	// Task C: bbox geo filter
	if bboxStr := q.Get("bbox"); bboxStr != "" {
		parts := strings.Split(bboxStr, ",")
		if len(parts) == 4 {
			minLng, e1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
			minLat, e2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			maxLng, e3 := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
			maxLat, e4 := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
			if e1 == nil && e2 == nil && e3 == nil && e4 == nil {
				*query += " AND l.latitude BETWEEN ? AND ? AND l.longitude BETWEEN ? AND ? AND l.latitude IS NOT NULL"
				*args = append(*args, minLat, maxLat, minLng, maxLng)
			}
		}
	}
	// Task C: geohash filter
	if ghStr := q.Get("geohash"); ghStr != "" {
		minLat, maxLat, minLng, maxLng := geohashBBox(ghStr)
		*query += " AND l.latitude BETWEEN ? AND ? AND l.longitude BETWEEN ? AND ? AND l.latitude IS NOT NULL"
		*args = append(*args, minLat, maxLat, minLng, maxLng)
	}
	// Task D: type filter (ball, workshop, festival)
	if typeStr := q.Get("type"); typeStr != "" {
		types := strings.Split(typeStr, ",")
		var typeClauses []string
		for _, t := range types {
			switch strings.TrimSpace(t) {
			case "ball":
				typeClauses = append(typeClauses, "e.has_ball=1")
			case "workshop":
				typeClauses = append(typeClauses, "e.has_workshop=1")
			case "festival":
				typeClauses = append(typeClauses, "e.has_festival=1")
			}
		}
		if len(typeClauses) > 0 {
			*query += " AND (" + strings.Join(typeClauses, " OR ") + ")"
		}
	}
	// Task D: dance_id filter
	if v := q.Get("dance_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*query += " AND EXISTS (SELECT 1 FROM event_dances ed WHERE ed.event_id=e.id AND ed.dance_id=?)"
			*args = append(*args, n)
		}
	}
	// Task D: difficulty filter
	if v := q.Get("difficulty"); v != "" {
		*query += " AND e.workshop_difficulty=?"
		*args = append(*args, v)
	}
	// Task D: pricing=free filter
	if v := q.Get("pricing"); v == "free" {
		*query += ` AND e.pricing LIKE '%"type":"free"%'`
	}
	// Task D: wheelchair filter
	if q.Get("wheelchair") == "1" {
		*query += ` AND e.attributes LIKE '%"wheelchair":true%'`
	}
	// Task D: bookable filter
	if q.Get("bookable") == "1" {
		*query += " AND e.booking_enabled=1"
	}
	// Task D: is_cancelled filter
	if q.Get("is_cancelled") == "1" {
		*query += " AND e.is_cancelled=1"
	}
	if v := q.Get("organization_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*query += " AND e.organization_id = ?"
			*args = append(*args, n)
		}
	}
	if v := q.Get("location_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*query += " AND e.location_id = ?"
			*args = append(*args, n)
		}
	}
	if v := q.Get("series_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*query += " AND e.series_id = ?"
			*args = append(*args, n)
		}
	}
	if v := q.Get("created_after"); v != "" {
		*query += " AND e.created_at >= ?"
		*args = append(*args, v)
	}
	if v := q.Get("source"); v != "" {
		*query += " AND e.source = ?"
		*args = append(*args, v)
	}
	return nil
}

// applyPagination appends ORDER BY + LIMIT/OFFSET clauses.
func applyPagination(r *http.Request, query *string, args *[]any) {
	q := r.URL.Query()
	limit, offset := 100, 0
	if l := q.Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}
	if o := q.Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	*query += " ORDER BY e.start_time ASC LIMIT ? OFFSET ?"
	*args = append(*args, limit, offset)
}

// GET /api/v1/vocabulary
func getVocabulary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"event_types": []map[string]string{
			{"key": "ball", "label": "Ball"},
			{"key": "workshop", "label": "Workshop"},
			{"key": "festival", "label": "Festival"},
		},
		"workshop_difficulties": []string{"beginner", "intermediate", "advanced"},
		"pricing_types":         []string{"free", "donation", "single", "multiple"},
		"attributes":            []string{"wheelchair", "bar", "kitchen"},
		"osm_types":             []string{"node", "way", "relation"},
	})
}

// ── event insert / update ──────────────────────────────────────────────────

func generateShortCode() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

// urlVal returns nil when s is empty so the DB column stays NULL.
func urlVal(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// insertEvent upserts an event. Returns (id, shortCode, created, error) where
// created=false means an existing event was updated instead of inserted.
// Deduplication order: UID exact match → URL exact match → title+location+time fuzzy match (±3 h).
// The URL and fuzzy tiers run whenever the previous tier misses, so two feeds that
// publish the same event with different UIDs (or none) converge to a single row.
func insertEvent(q querier, title, description string, startTime, endTime int64, locationID int64, hasBall, hasWorkshop, hasFestival, isCancelled bool, workshopDifficulty, bookingURL string, isPublished bool, organizationID *int, uid, url, source string, sourceLastModified int64, pricing *Pricing, fetchSourceID int, food, drink, floorCondition string, attributes map[string]bool, contactName, contactEmail string, createdByID *int) (int, string, bool, error) {
	var existingID int
	var existingShortCode string
	var existingSourceLastModified int64
	var existingChangedAt int64
	var lookupErr error = sql.ErrNoRows

	if uid != "" {
		lookupErr = q.QueryRow(
			"SELECT id, short_code, COALESCE(source_last_modified, 0), COALESCE(changed_at, 0) FROM events WHERE uid = ?", uid,
		).Scan(&existingID, &existingShortCode, &existingSourceLastModified, &existingChangedAt)
		if lookupErr != nil && lookupErr != sql.ErrNoRows {
			return 0, "", false, lookupErr
		}
	}

	// URL tier: fires when uid is absent or not found.
	if lookupErr == sql.ErrNoRows && url != "" {
		lookupErr = q.QueryRow(
			"SELECT id, short_code, COALESCE(source_last_modified, 0), COALESCE(changed_at, 0) FROM events WHERE url = ?", url,
		).Scan(&existingID, &existingShortCode, &existingSourceLastModified, &existingChangedAt)
		if lookupErr != nil && lookupErr != sql.ErrNoRows {
			return 0, "", false, lookupErr
		}
	}

	// Fuzzy fallback: fires when both uid and url lookups missed.
	if lookupErr == sql.ErrNoRows {
		const threeHours = int64(3 * 60 * 60)
		lookupErr = q.QueryRow(
			"SELECT id, short_code, COALESCE(source_last_modified, 0), COALESCE(changed_at, 0) FROM events WHERE title = ? AND location_id = ? AND ABS(start_time - ?) < ?",
			title, locationID, startTime, threeHours,
		).Scan(&existingID, &existingShortCode, &existingSourceLastModified, &existingChangedAt)
	}

	if lookupErr != nil && lookupErr != sql.ErrNoRows {
		return 0, "", false, lookupErr
	}

	var pricingArg any
	if pricing != nil {
		if b, err := json.Marshal(pricing); err == nil {
			pricingArg = string(b)
		}
	}

	if lookupErr == nil {
		// Skip update when the source tells us nothing has changed since last import.
		if sourceLastModified > 0 && sourceLastModified <= existingSourceLastModified {
			return existingID, existingShortCode, false, nil
		}

		var slmArg any
		if sourceLastModified > 0 {
			slmArg = sourceLastModified
		}

		// Protect manual edits from being overwritten by imports (source != "").
		if source != "" && existingChangedAt > 0 {
			if sourceLastModified == 0 || sourceLastModified <= existingChangedAt {
				// Source has no timestamp or is not newer than the manual edit — skip.
				return existingID, existingShortCode, false, nil
			}
			// Source is newer than the manual edit — do a merge update: only
			// overwrite fields where the source provides non-empty content, and
			// preserve user-set fields (has_ball/workshop/festival, is_published,
			// booking_url) that iCal/RSS sources never populate.
			var fsArg any
			if fetchSourceID > 0 {
				fsArg = fetchSourceID
			}
			_, err := q.Exec(`UPDATE events SET
				title=?,
				description=CASE WHEN ?!='' THEN ? ELSE description END,
				start_time=?, end_time=?,
				location_id=CASE WHEN ?!=0 THEN ? ELSE location_id END,
				is_cancelled=?,
				workshop_difficulty=CASE WHEN ?!='' THEN ? ELSE workshop_difficulty END,
				url=CASE WHEN ? IS NOT NULL THEN ? ELSE url END,
				source_last_modified=?,
				pricing=CASE WHEN ? IS NOT NULL THEN ? ELSE pricing END,
				fetch_source_id=COALESCE(?,fetch_source_id)
				WHERE id=?`,
				title,
				description, description,
				startTime, endTime,
				locationID, locationID,
				isCancelled,
				workshopDifficulty, workshopDifficulty,
				urlVal(url), urlVal(url),
				slmArg,
				pricingArg, pricingArg,
				fsArg,
				existingID,
			)
			if err != nil {
				return 0, "", false, err
			}
			return existingID, existingShortCode, false, nil
		}

		var err error
		if source != "" {
			var fsArg any
			if fetchSourceID > 0 {
				fsArg = fetchSourceID
			}
			_, err = q.Exec(
				"UPDATE events SET description=?, start_time=?, end_time=?, location_id=?, has_ball=?, has_workshop=?, has_festival=?, is_cancelled=?, workshop_difficulty=?, is_published=?, url=?, source_last_modified=?, pricing=?, changed_at=?, changed_by=?, fetch_source_id=COALESCE(?,fetch_source_id) WHERE id=?",
				description, startTime, endTime, locationID, hasBall, hasWorkshop, hasFestival, isCancelled, workshopDifficulty, isPublished, urlVal(url), slmArg, pricingArg,
				time.Now().UTC().Unix(), "fetch", fsArg, existingID,
			)
		} else {
			_, err = q.Exec(
				"UPDATE events SET description=?, start_time=?, end_time=?, location_id=?, has_ball=?, has_workshop=?, has_festival=?, is_cancelled=?, workshop_difficulty=?, is_published=?, url=?, source_last_modified=?, pricing=? WHERE id=?",
				description, startTime, endTime, locationID, hasBall, hasWorkshop, hasFestival, isCancelled, workshopDifficulty, isPublished, urlVal(url), slmArg, pricingArg, existingID,
			)
		}
		if err != nil {
			return 0, "", false, err
		}
		return existingID, existingShortCode, false, nil
	}

	var orgIDArg any
	if organizationID != nil {
		orgIDArg = *organizationID
	}
	var uidArg any
	if uid != "" {
		uidArg = uid
	}
	var slmArg any
	if sourceLastModified > 0 {
		slmArg = sourceLastModified
	}
	// short_code is pre-computed so the INSERT is a single round-trip (no follow-up UPDATE).
	// Retry up to 5 times on the rare collision of the 4-byte random short code.
	var result sql.Result
	var err error
	var shortCode string
	var insChangedAt any
	var insChangedBy string
	var insFetchSourceID any
	if fetchSourceID > 0 {
		insChangedAt = time.Now().UTC().Unix()
		insChangedBy = "fetch"
		insFetchSourceID = fetchSourceID
	}
	for range 5 {
		shortCode, err = generateShortCode()
		if err != nil {
			return 0, "", false, err
		}
		var sourceArg any
		if source != "" {
			sourceArg = source
		}
		var createdByArg any
		if createdByID != nil {
			createdByArg = *createdByID
		}
		result, err = q.Exec(
			"INSERT INTO events (uid, title, description, start_time, end_time, location_id, has_ball, has_workshop, has_festival, is_cancelled, workshop_difficulty, is_published, organization_id, short_code, url, source, source_last_modified, pricing, booking_url, changed_at, changed_by, fetch_source_id, food, drink, floor_condition, attributes, contact_name, contact_email, created_by_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			uidArg, title, description, startTime, endTime, locationID, hasBall, hasWorkshop, hasFestival, isCancelled, workshopDifficulty, isPublished, orgIDArg, shortCode, urlVal(url), sourceArg, slmArg, pricingArg, urlVal(bookingURL), insChangedAt, insChangedBy, insFetchSourceID, food, drink, floorCondition, attrsJSON(attributes), contactName, contactEmail, createdByArg,
		)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "short_code") {
			return 0, "", false, err
		}
	}
	if err != nil {
		return 0, "", false, err
	}
	id, _ := result.LastInsertId()
	syncEventLocationGeohash(int(id))
	return int(id), shortCode, true, nil
}

// syncEventLocationGeohash updates location_geohash on an event from its location's coordinates.
func syncEventLocationGeohash(eventID int) {
	var lat, lng sql.NullFloat64
	if err := db.QueryRow(
		`SELECT l.latitude, l.longitude FROM locations l
		 JOIN events e ON l.id=e.location_id WHERE e.id=?`, eventID,
	).Scan(&lat, &lng); err != nil || !lat.Valid || !lng.Valid {
		return
	}
	gh := geohashEncode(lat.Float64, lng.Float64, 7)
	db.Exec("UPDATE events SET location_geohash=? WHERE id=?", gh, eventID)
}

// createEventFromRequest inserts or updates all events described by req.
// Returns (events, allCreated, error); allCreated=false if any event was updated.
func createEventFromRequest(q querier, req EventCreateRequest, locationID int64, isPublished bool, createdByID *int) ([]Event, bool, error) {
	syncEventTypeTags(&req.EventWriteRequest)
	var createdEvents []Event
	allCreated := true

	type dateEntry struct {
		description, startTime, endTime string
	}

	var entries []dateEntry
	if len(req.Date) > 0 {
		for _, d := range req.Date {
			desc := d.Description
			if desc == "" {
				desc = req.Description
			}
			entries = append(entries, dateEntry{desc, d.StartTime, d.EndTime})
		}
	} else {
		entries = []dateEntry{{req.Description, req.StartTime, req.EndTime}}
	}

	for _, entry := range entries {
		startTime, err := parseTimeToUnix(entry.startTime)
		if err != nil {
			return nil, false, fmt.Errorf("start_time: %w", err)
		}
		endTime, err := parseTimeToUnix(entry.endTime)
		if err != nil {
			return nil, false, fmt.Errorf("end_time: %w", err)
		}

		id, shortCode, created, err := insertEvent(q, req.Title, entry.description, startTime, endTime, locationID, req.HasBall, req.HasWorkshop, req.HasFestival, req.IsCancelled, req.WorkshopDifficulty, req.BookingURL, isPublished, req.OrganizationID, req.UID, req.URL, req.Source, req.SourceLastModified, req.Pricing, req.FetchSourceID, req.Food, req.Drink, req.FloorCondition, req.Attributes, req.ContactName, req.ContactEmail, createdByID)
		if err != nil {
			return nil, false, err
		}
		if !created {
			allCreated = false
		}

		if len(req.Musicians) > 0 {
			q.Exec("DELETE FROM event_musicians WHERE event_id = ?", id)
			for _, musicianID := range req.Musicians {
				q.Exec("INSERT OR IGNORE INTO event_musicians (event_id, musician_id) VALUES (?, ?)", id, musicianID)
			}
		}
		if len(req.Dances) > 0 {
			q.Exec("DELETE FROM event_dances WHERE event_id = ?", id)
			for _, danceID := range req.Dances {
				q.Exec("INSERT OR IGNORE INTO event_dances (event_id, dance_id) VALUES (?, ?)", id, danceID)
			}
		}
		syncEventTags(q, id, req.Tags)

		event, err := fetchEventByID(q, id)
		if err != nil {
			return nil, false, err
		}
		event.ShortCode = shortCode
		createdEvents = append(createdEvents, event)
	}

	return createdEvents, allCreated, nil
}

// userOrgSet returns the set of organization IDs the user is a member of.
func userOrgSet(userID int) map[int]bool {
	rows, err := db.Query("SELECT organization_id FROM organization_members WHERE user_id = ?", userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	orgs := make(map[int]bool)
	for rows.Next() {
		var id int
		rows.Scan(&id)
		orgs[id] = true
	}
	return orgs
}

// annotateEditable sets Editable and Cancelable on each event based on the caller's role.
// editable:   admin=any; user=own orgs; publisher=false
// cancelable: admin=any; user=own orgs; publisher=own created events only
// The org membership set is fetched once to avoid N+1 queries.
func annotateEditable(events []Event, userRole string, userID int) {
	isAdmin := userRole == RoleAdmin
	var memberOrgs map[int]bool
	if userRole == RoleUser {
		memberOrgs = userOrgSet(userID)
	}
	for i := range events {
		inOrg := memberOrgs != nil && events[i].OrganizationID != nil && memberOrgs[*events[i].OrganizationID]
		editable := isAdmin || inOrg
		cancelable := isAdmin || inOrg ||
			(userRole == RolePublisher && events[i].CreatedByID != nil && *events[i].CreatedByID == userID)
		events[i].Editable = &editable
		events[i].Cancelable = &cancelable
	}
}

// fetchEventMusicians returns musicians linked to an event via event_musicians.
func fetchEventMusicians(eventID int) ([]Musician, error) {
	rows, err := db.Query(
		`SELECT m.id, m.bandname, COALESCE(m.short_name,''), COALESCE(m.internetsite,''),
		 COALESCE(m.description,''), COALESCE(m.mbid,''), COALESCE(m.wikidata_id,''),
		 COALESCE(m.discogs_id,''), m.created_at
		 FROM musicians m JOIN event_musicians em ON m.id = em.musician_id
		 WHERE em.event_id = ? ORDER BY m.bandname`,
		eventID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var musicians []Musician
	for rows.Next() {
		var m Musician
		if err := rows.Scan(&m.ID, &m.Bandname, &m.ShortName, &m.Internetsite, &m.Description,
			&m.MBID, &m.WikidataID, &m.DiscogsID, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.ImageURL = musicianImageURL(m.ID)
		musicians = append(musicians, m)
	}
	return musicians, nil
}

// syncEventTypeTags reconciles has_ball / has_workshop / has_festival with the
// tags slice so that either source of truth (explicit booleans from importers,
// or tag slugs from the web UI) produces consistent DB state.
//
// Booleans → tags: if an importer set HasBall=true without including "bal-folk"
// in Tags, we add the tag. Likewise for workshop and festival.
// Tags → booleans: after the tag list is complete the booleans are re-derived
// so that tags are always authoritative.
// The "workshop" catch-all tag is added whenever any specific workshop tag is
// present, enabling a single ?tag=workshop filter later.
func syncEventTypeTags(w *EventWriteRequest) {
	has := func(slug string) bool {
		for _, t := range w.Tags {
			if t == slug {
				return true
			}
		}
		return false
	}
	add := func(slug string) {
		if !has(slug) {
			w.Tags = append(w.Tags, slug)
		}
	}
	// booleans → tags (for importers that set booleans directly)
	if w.HasBall {
		add("bal-folk")
	}
	if w.HasFestival {
		add("festival")
	}
	if w.HasWorkshop && !has("dance-workshop") && !has("musician-workshop") && !has("workshop") {
		add("workshop")
	}
	// tags → booleans (tags are now authoritative)
	w.HasBall = has("bal-folk")
	w.HasFestival = has("festival")
	w.HasWorkshop = has("dance-workshop") || has("musician-workshop") || has("workshop")
	// ensure workshop catch-all is always present when any workshop tag is set
	if w.HasWorkshop {
		add("workshop")
	}
}

// syncEventTags replaces all event_tags rows for eventID with the given tags.
func syncEventTags(q querier, eventID int, tags []string) {
	q.Exec("DELETE FROM event_tags WHERE event_id = ?", eventID)
	for _, tag := range tags {
		if t := strings.TrimSpace(tag); t != "" {
			q.Exec("INSERT OR IGNORE INTO event_tags (event_id, tag) VALUES (?, ?)", eventID, t)
		}
	}
}

// fetchEventLocation returns the primary location for an event as a full
// Location object (including OrganizationIDs via location_organizations).
func fetchEventLocation(eventID int) ([]Location, error) {
	const sel = `SELECT l.id, l.location, COALESCE(l.short_name,''), COALESCE(l.address,''),
		COALESCE(l.zipcode,''), COALESCE(l.town,''), COALESCE(l.country,''),
		COALESCE(l.country_code,''), COALESCE(l.region,''), l.latitude,
		l.longitude, COALESCE(l.internetsite,''), l.osm_id, COALESCE(l.osm_type,''),
		COALESCE(l.geohash,''), COALESCE(l.wikidata_id,''), COALESCE(l.mb_place_id,''),
		l.created_at, COALESCE(GROUP_CONCAT(lo.organization_id),'')
		FROM locations l
		LEFT JOIN location_organizations lo ON l.id=lo.location_id
		JOIN events e ON l.id=e.location_id
		WHERE e.id=? GROUP BY l.id`
	var loc Location
	var orgIDsStr string
	var lat, lng sql.NullFloat64
	err := db.QueryRow(sel, eventID).Scan(
		&loc.ID, &loc.Location, &loc.ShortName, &loc.Address,
		&loc.Zipcode, &loc.Town, &loc.Country, &loc.CountryCode, &loc.Region, &lat, &lng,
		&loc.Internetsite, &loc.OsmID, &loc.OsmType,
		&loc.Geohash, &loc.WikidataID, &loc.MBPlaceID,
		&loc.CreatedAt, &orgIDsStr,
	)
	if err != nil {
		return nil, err
	}
	if lat.Valid {
		v := lat.Float64
		loc.Latitude = &v
	}
	if lng.Valid {
		v := lng.Float64
		loc.Longitude = &v
	}
	if loc.Geohash == "" && loc.Latitude != nil && loc.Longitude != nil {
		loc.Geohash = geohashEncode(*loc.Latitude, *loc.Longitude, 7)
	}
	loc.OrganizationIDs = parseOrgIDs(orgIDsStr)
	return []Location{loc}, nil
}

// ── HTTP handlers ──────────────────────────────────────────────────────────

// GET /api/v1/events
func getEvents(w http.ResponseWriter, r *http.Request) {
	accept := r.Header.Get("Accept")
	callerID, userRole := callerFromRequest(r)
	isAuthorizedAdmin := userRole == RoleUser || userRole == RoleAdmin || userRole == RolePublisher

	// Short-code lookup for public clients (e.g. ?code=a1b2c3d4).
	if !isAuthorizedAdmin {
		if shortCode := r.URL.Query().Get("code"); shortCode != "" {
			w.Header().Set("Content-Type", "application/json")
			event, err := scanEventRow(db.QueryRow(
				eventListSelect+" WHERE e.short_code = ? AND e.is_published = 1 AND (e.suggestion_token IS NULL OR e.suggestion_token = '')", shortCode,
			))
			if err == sql.ErrNoRows {
				writeError(w, "Event not found", http.StatusNotFound)
				return
			} else if err != nil {
				writeError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(event)
			return
		}
	}

	query := eventListSelect + " WHERE (e.suggestion_token IS NULL OR e.suggestion_token = '')"
	args := []any{}

	if !isAuthorizedAdmin {
		query += " AND e.is_published = 1"
		// Cache fingerprint for public clients: count + latest creation time.
		if !strings.Contains(accept, "text/calendar") {
			if checkPublicCacheHeaders(w, r, "SELECT COUNT(*), MAX(created_at) FROM events WHERE is_published = 1") {
				return
			}
		}
	} else if v := r.URL.Query().Get("is_published"); v != "" {
		query += " AND e.is_published = ?"
		args = append(args, boolParam(v))
		// Publisher may only see unpublished events belonging to their own org.
		if userRole == RolePublisher && boolParam(v) == 0 {
			query += " AND e.organization_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)"
			args = append(args, callerID)
		}
	} else if userRole == RolePublisher {
		// No is_published filter supplied: restrict publisher's view of unpublished events to their org.
		query += " AND (e.is_published = 1 OR e.organization_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?))"
		args = append(args, callerID)
	}

	// Exclude past events by default; authorized users can opt in with include_past=true.
	if r.URL.Query().Get("include_past") != "true" {
		query += " AND e.end_time >= ?"
		args = append(args, time.Now().Unix())
	}

	if v := r.URL.Query().Get("end_time_after"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			query += " AND e.end_time > ?"
			args = append(args, n)
		}
	}
	if v := r.URL.Query().Get("end_time_before"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			query += " AND e.end_time < ?"
			args = append(args, n)
		}
	}

	if err := applyEventFilters(r, &query, &args); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	applyPagination(r, &query, &args)

	rows, err := db.Query(query, args...)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		event, err := scanEventRow(rows)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		events = append(events, event)
	}

	annotateEditable(events, userRole, callerID)

	// Post-filter with haversine when a geo radius was requested.
	if latStr, lonStr, radStr := r.URL.Query().Get("lat"), r.URL.Query().Get("lon"), r.URL.Query().Get("radius_km"); latStr != "" && lonStr != "" && radStr != "" {
		lat, latErr := strconv.ParseFloat(latStr, 64)
		lon, lonErr := strconv.ParseFloat(lonStr, 64)
		radius, radErr := strconv.ParseFloat(radStr, 64)
		if latErr == nil && lonErr == nil && radErr == nil && radius > 0 {
			filtered := events[:0]
			for _, ev := range events {
				if ev.Location != nil && ev.Location.Latitude != nil && ev.Location.Longitude != nil {
					if haversineKm(lat, lon, *ev.Location.Latitude, *ev.Location.Longitude) <= radius {
						filtered = append(filtered, ev)
					}
				}
			}
			events = filtered
		}
	}

	if strings.Contains(accept, "text/calendar") {
		cal := ics.NewCalendar()
		cal.SetMethod(ics.MethodPublish)
		for _, event := range events {
			addEventToCalendar(cal, event)
		}
		w.Header().Set("Content-Type", "text/calendar")
		w.Write([]byte(cal.Serialize()))
	} else if strings.Contains(accept, "application/atom+xml") {
		writeEventsAtom(w, r, events)
	} else {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events)
	}
}

func writeEventsAtom(w http.ResponseWriter, r *http.Request, events []Event) {
	host := r.Host
	entries := make([]apiFeedEntry, 0, len(events))
	for _, ev := range events {
		updatedAt := ev.ChangedAt
		if updatedAt == "" {
			updatedAt = ev.CreatedAt
		}
		summary := ev.Description
		if len(summary) > 300 {
			summary = summary[:300]
		}
		e := apiFeedEntry{
			Title:   ev.Title,
			ID:      "https://" + host + "/api/v1/events/" + strconv.Itoa(ev.ID),
			Updated: atomTimeStr(updatedAt),
			Summary: summary,
		}
		href := ev.URL
		if href == "" {
			href = "https://" + host + "/events/" + strconv.Itoa(ev.ID)
		}
		e.Links = append(e.Links, apiFeedLink{Rel: "alternate", Href: href})
		entries = append(entries, e)
	}
	writeAtom(w, apiFeed{
		XMLNS:   "http://www.w3.org/2005/Atom",
		Title:   "Events",
		ID:      "https://" + r.Host + "/api/v1/events",
		Updated: atomTime(0),
		Entries: entries,
	})
}

// POST /api/v1/events
func createEvent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, callerRole := callerFromRequest(r)
	isPublished := callerRole == RoleUser || callerRole == RoleAdmin || callerRole == RolePublisher

	contentType := r.Header.Get("Content-Type")
	var requests []EventCreateRequest
	var vevents []*ics.VEvent

	if contentType == "application/json" {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			status := http.StatusBadRequest
			if errors.As(err, new(*http.MaxBytesError)) {
				status = http.StatusRequestEntityTooLarge
			}
			writeError(w, err.Error(), status)
			return
		}
		var arrayReqs []EventCreateRequest
		if err := json.Unmarshal(body, &arrayReqs); err == nil && len(arrayReqs) > 0 && arrayReqs[0].Title != "" {
			requests = arrayReqs
		} else {
			var singleReq EventCreateRequest
			if err := json.Unmarshal(body, &singleReq); err != nil {
				writeError(w, "Invalid JSON: must be a single event object or array of events", http.StatusBadRequest)
				return
			}
			requests = []EventCreateRequest{singleReq}
		}
	} else if contentType == "text/calendar" {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			status := http.StatusBadRequest
			if errors.As(err, new(*http.MaxBytesError)) {
				status = http.StatusRequestEntityTooLarge
			}
			writeError(w, err.Error(), status)
			return
		}
		cal, err := ics.ParseCalendar(strings.NewReader(string(body)))
		if err != nil {
			writeError(w, "Invalid iCal format", http.StatusBadRequest)
			return
		}
		var icalOrgID *int
		if s := r.URL.Query().Get("organization_id"); s != "" {
			if v, err2 := strconv.Atoi(s); err2 == nil {
				icalOrgID = &v
			}
		}
		for _, event := range cal.Events() {
			startT, err := event.GetStartAt()
			if err != nil {
				continue
			}
			endT := startT
			if et, err := event.GetEndAt(); err == nil {
				endT = et
			} else if p := event.GetProperty(ics.ComponentPropertyDuration); p != nil {
				if d, err := parseICalDuration(p.Value); err == nil {
					endT = startT.Add(d)
				}
			}
			if p := event.GetProperty(ics.ComponentPropertySummary); p == nil || p.Value == "" {
				continue
			}

			orgID := icalOrgID
			if orgID == nil {
				orgID = ensureOrgFromOrganizer(event)
			}
			var isCancelled bool
			if p := event.GetProperty(ics.ComponentPropertyStatus); p != nil {
				isCancelled = p.Value == "CANCELLED"
			}
			baseUID := event.GetProperty(ics.ComponentPropertyUniqueId)
			var baseUIDStr string
			if baseUID != nil {
				baseUIDStr = baseUID.Value
			}

			occs, _ := expandRRuleOccurrences(event, startT, endT)
			if occs == nil {
				occs = [][2]time.Time{{startT, endT}}
			}

			for _, occ := range occs {
				uid := baseUIDStr
				if len(occs) > 1 && !occ[0].Equal(startT) {
					uid = fmt.Sprintf("%s_%d", baseUIDStr, occ[0].UTC().Unix())
				}
				requests = append(requests, EventCreateRequest{
					UID: uid,
					EventWriteRequest: EventWriteRequest{
						Title:          event.GetProperty(ics.ComponentPropertySummary).Value,
						Description:    event.GetProperty(ics.ComponentPropertyDescription).Value,
						StartTime:      occ[0].UTC().Format(time.RFC3339),
						EndTime:        occ[1].UTC().Format(time.RFC3339),
						IsCancelled:    isCancelled,
						Tags:           parseICalCategories(event),
						URL:            attachURL(event),
						OrganizationID: orgID,
						Location: func() EventLocationRequest {
							if apple := parseAppleStructuredLocation(event); apple != nil {
								if apple.Location == "" {
									if p := event.GetProperty(ics.ComponentPropertyLocation); p != nil {
										apple.Location = p.Value
									}
								}
								if apple.Latitude == nil {
									if p := event.GetProperty(ics.ComponentPropertyGeo); p != nil {
										apple.Latitude, apple.Longitude = parseICalGeo(p.Value)
									}
								}
								return *apple
							}
							var loc string
							var lat, lon *float64
							if p := event.GetProperty(ics.ComponentPropertyLocation); p != nil {
								loc = p.Value
							}
							if p := event.GetProperty(ics.ComponentPropertyGeo); p != nil {
								lat, lon = parseICalGeo(p.Value)
							}
							return EventLocationRequest{Location: loc, Latitude: lat, Longitude: lon}
						}(),
					},
				})
				vevents = append(vevents, event)
			}
		}
		if len(requests) == 0 {
			writeError(w, "No events found in iCal file", http.StatusBadRequest)
			return
		}
	} else {
		writeError(w, "Content-Type must be application/json or text/calendar", http.StatusUnsupportedMediaType)
		return
	}

	for i := range requests {
		requests[i].Tags = filterKnownTags(requests[i].Tags)
		syncEventTypeTags(&requests[i].EventWriteRequest)
	}

	if callerRole != RoleAdmin {
		checked := make(map[int]bool)
		for _, req := range requests {
			if req.OrganizationID == nil {
				writeError(w, "organization_id is required", http.StatusBadRequest)
				return
			}
			orgID := *req.OrganizationID
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

	tx, err := db.Begin()
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var allCreatedEvents []Event
	allCreated := true
	for i, req := range requests {
		locationID, err := resolveLocationID(tx, req.LocationID, req.Location)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		createdEvents, created, err := createEventFromRequest(tx, req, locationID, isPublished, &callerID)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !created {
			allCreated = false
		}
		allCreatedEvents = append(allCreatedEvents, createdEvents...)
		if i < len(vevents) {
			for _, ev := range createdEvents {
				attachImagesFromICalEvent(ev.ID, vevents[i])
			}
		}
	}

	if err := tx.Commit(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if allCreated {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	json.NewEncoder(w).Encode(allCreatedEvents)
}

// GET /api/v1/events/{id}
func getEvent(w http.ResponseWriter, r *http.Request) {
	accept := r.Header.Get("Accept")
	callerID, userRole := callerFromRequest(r)
	id := r.PathValue("id")

	// Unauthenticated callers only see published non-suggestion events.
	var query string
	if userRole != "" {
		query = eventListSelect + " WHERE e.id = ?"
	} else {
		query = eventListSelect + " WHERE e.id = ? AND e.is_published = 1 AND (e.suggestion_token IS NULL OR e.suggestion_token = '')"
	}
	event, err := scanEventRow(db.QueryRow(query, id))
	if err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	inOrg := userRole == RoleUser && event.OrganizationID != nil && isOrgMember(callerID, *event.OrganizationID)
	editable := userRole == RoleAdmin || inOrg
	cancelable := editable || (userRole == RolePublisher && event.CreatedByID != nil && *event.CreatedByID == callerID)
	event.Editable = &editable
	event.Cancelable = &cancelable

	var (
		timetable []TimetableEntry
		locs      []Location
		musicians []Musician
		wg        sync.WaitGroup
	)
	wg.Add(3)
	go func() { defer wg.Done(); timetable, _ = fetchTimetable(event.ID) }()
	go func() { defer wg.Done(); locs, _ = fetchEventLocation(event.ID) }()
	go func() { defer wg.Done(); musicians, _ = fetchEventMusicians(event.ID) }()
	wg.Wait()
	if len(timetable) > 0 {
		event.Timetable = timetable
	}
	if len(locs) > 0 {
		event.Locations = locs
	}
	if len(musicians) > 0 {
		event.Musicians = musicians
	}

	if strings.Contains(accept, "text/calendar") {
		cal := ics.NewCalendar()
		cal.SetMethod(ics.MethodPublish)
		addEventToCalendar(cal, event)
		w.Header().Set("Content-Type", "text/calendar")
		w.Write([]byte(cal.Serialize()))
	} else if strings.Contains(accept, "application/atom+xml") {
		writeEventsAtom(w, r, []Event{event})
	} else {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(event)
	}
}

// PUT /api/v1/events/{id} — full event update
func updateEvent(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var req EventUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Title == "" {
		writeError(w, "title is required", http.StatusBadRequest)
		return
	}
	if err := validateTags(req.Tags); err != nil {
		writeError(w, "invalid tag: "+err.Error(), http.StatusBadRequest)
		return
	}

	var existingOrgID sql.NullInt64
	if err := db.QueryRow("SELECT organization_id FROM events WHERE id = ?", id).Scan(&existingOrgID); err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if userRole != RoleAdmin {
		if !existingOrgID.Valid || !isOrgMember(callerID, int(existingOrgID.Int64)) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	startTime, err := parseTimeToUnix(req.StartTime)
	if err != nil {
		writeError(w, "invalid start_time: "+err.Error(), http.StatusBadRequest)
		return
	}
	endTime, err := parseTimeToUnix(req.EndTime)
	if err != nil {
		endTime = startTime
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	req.Tags = filterKnownTags(req.Tags)
	syncEventTypeTags(&req.EventWriteRequest)

	locationID, err := resolveLocationID(tx, req.LocationID, req.Location)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var pricingArg any
	if req.Pricing != nil {
		if b, err := json.Marshal(req.Pricing); err == nil {
			pricingArg = string(b)
		}
	}
	var orgIDArg any
	if req.OrganizationID != nil {
		orgIDArg = *req.OrganizationID
	}

	var changedByUser string
	if err := db.QueryRow(
		"SELECT COALESCE(NULLIF(display_name,''), SUBSTR(email,1,INSTR(email,'@')-1)) FROM users WHERE id = ?", callerID,
	).Scan(&changedByUser); err != nil || changedByUser == "" {
		changedByUser = strconv.Itoa(callerID)
	}

	var callerIDArg any
	if callerID > 0 {
		callerIDArg = callerID
	}
	if _, err := tx.Exec(
		`UPDATE events SET title=?, description=?, start_time=?, end_time=?, location_id=?,
		 has_ball=?, has_workshop=?, has_festival=?, is_cancelled=?, is_published=?,
		 workshop_difficulty=?, url=?, booking_url=?, organization_id=?, pricing=?,
		 availability=?, tickets_total=?, booking_enabled=?, food=?, drink=?, floor_condition=?, attributes=?,
		 contact_name=?, contact_email=?, changed_at=?, changed_by=?, changed_by_id=? WHERE id=?`,
		req.Title, req.Description, startTime, endTime, locationID,
		req.HasBall, req.HasWorkshop, req.HasFestival, req.IsCancelled, req.IsPublished,
		req.WorkshopDifficulty, urlVal(req.URL), urlVal(req.BookingURL), orgIDArg, pricingArg,
		req.Availability, req.TicketsTotal, req.BookingEnabled, req.Food, req.Drink, req.FloorCondition, attrsJSON(req.Attributes),
		req.ContactName, req.ContactEmail, time.Now().UTC().Unix(), changedByUser, callerIDArg, id,
	); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tx.Exec("DELETE FROM event_musicians WHERE event_id = ?", id)
	for _, musicianID := range req.Musicians {
		tx.Exec("INSERT OR IGNORE INTO event_musicians (event_id, musician_id) VALUES (?, ?)", id, musicianID)
	}
	tx.Exec("DELETE FROM event_dances WHERE event_id = ?", id)
	for _, danceID := range req.Dances {
		tx.Exec("INSERT OR IGNORE INTO event_dances (event_id, dance_id) VALUES (?, ?)", id, danceID)
	}
	syncEventTags(tx, id, req.Tags)

	if err := tx.Commit(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	syncEventLocationGeohash(id)

	event, err := fetchEventByID(db, id)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if musicians, err := fetchEventMusicians(id); err == nil {
		event.Musicians = musicians
	}
	if timetable, err := fetchTimetable(id); err == nil {
		event.Timetable = timetable
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(event)
}

// POST /api/v1/events/{id}/publish — set is_published=1.
// Admin/publisher: can publish any event. Regular user: only events assigned to their org.
func publishEvent(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	id := r.PathValue("id")

	if userRole == RoleAdmin {
		result, err := db.Exec("UPDATE events SET is_published=1, suggestion_token=NULL WHERE id=?", id)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if n, _ := result.RowsAffected(); n == 0 {
			writeError(w, "Event not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// User and publisher: must belong to the event's org.
	if userRole == RoleUser || userRole == RolePublisher {
		var orgID sql.NullInt64
		if err := db.QueryRow("SELECT organization_id FROM events WHERE id=? AND is_published=0", id).Scan(&orgID); err == sql.ErrNoRows {
			writeError(w, "Event not found", http.StatusNotFound)
			return
		} else if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !orgID.Valid || !isOrgMember(callerID, int(orgID.Int64)) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		db.Exec("UPDATE events SET is_published=1, suggestion_token=NULL WHERE id=?", id)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	writeError(w, "Forbidden", http.StatusForbidden)
}

// POST /api/v1/events/{id}/assign-org — assign an organisation to an orphaned event
// (organization_id IS NULL). Admin: any event. User/publisher: only orgs they belong to.
func assignEventOrg(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RolePublisher && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	var req struct {
		OrgID int `json:"org_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OrgID == 0 {
		writeError(w, "org_id required", http.StatusBadRequest)
		return
	}

	if userRole != RoleAdmin && !isOrgMember(callerID, req.OrgID) {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	query := "UPDATE events SET organization_id=? WHERE id=? AND organization_id IS NULL"
	if userRole == RoleAdmin {
		query = "UPDATE events SET organization_id=? WHERE id=?"
	}
	result, err := db.Exec(query, req.OrgID, id)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Event not found or already assigned to an organisation", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/events/{id} — admin only.
func deleteEvent(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden: only admins may delete events", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	result, err := db.Exec("DELETE FROM events WHERE id = ?", id)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/events/{id}/cancel — set is_cancelled=1.
// admin: any event. user: own orgs. publisher: own created events only.
func cancelEvent(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	switch userRole {
	case RoleAdmin:
		// unrestricted
	case RoleUser:
		var orgID sql.NullInt64
		if err := db.QueryRow("SELECT organization_id FROM events WHERE id=?", id).Scan(&orgID); err == sql.ErrNoRows {
			writeError(w, "Event not found", http.StatusNotFound)
			return
		} else if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !orgID.Valid || !isOrgMember(callerID, int(orgID.Int64)) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
	case RolePublisher:
		var createdBy sql.NullInt64
		if err := db.QueryRow("SELECT created_by_id FROM events WHERE id=?", id).Scan(&createdBy); err == sql.ErrNoRows {
			writeError(w, "Event not found", http.StatusNotFound)
			return
		} else if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !createdBy.Valid || int(createdBy.Int64) != callerID {
			writeError(w, "Forbidden: publishers may only cancel events they created", http.StatusForbidden)
			return
		}
	default:
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	result, err := db.Exec("UPDATE events SET is_cancelled=1 WHERE id=?", id)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/events/{id}/clone — create an unpublished pre-filled draft from an existing event.
// Timetable times are copied; description and musician links are not.
// start_time and end_time are cleared (user fills before publishing).
func cloneEvent(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	srcID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var req struct {
		TargetOrgID *int `json:"target_org_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	// Fetch the source event.
	src, err := fetchEventByID(db, srcID)
	if err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Determine target org.
	var targetOrgID *int
	if userRole == RoleAdmin {
		targetOrgID = src.OrganizationID
	} else {
		if src.OrganizationID != nil && isOrgMember(callerID, *src.OrganizationID) {
			targetOrgID = src.OrganizationID
		} else {
			// Source org is foreign — resolve from caller's memberships.
			if req.TargetOrgID != nil {
				if !isOrgMember(callerID, *req.TargetOrgID) {
					writeError(w, "Forbidden: not a member of the specified target organization", http.StatusForbidden)
					return
				}
				targetOrgID = req.TargetOrgID
			} else {
				// Auto-assign if caller has exactly one org.
				orgs := userOrgSet(callerID)
				if len(orgs) == 1 {
					for id := range orgs {
						id := id
						targetOrgID = &id
					}
				} else if len(orgs) > 1 {
					writeError(w, "target_org_id is required when you belong to multiple organizations", http.StatusBadRequest)
					return
				}
			}
		}
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Build create request from source, clearing time and publish state.
	cloneReq := EventCreateRequest{
		EventWriteRequest: EventWriteRequest{
			Title:              src.Title,
			Description:        src.Description,
			HasBall:            src.HasBall,
			HasWorkshop:        src.HasWorkshop,
			HasFestival:        src.HasFestival,
			WorkshopDifficulty: src.WorkshopDifficulty,
			BookingURL:         src.BookingURL,
			Food:               src.Food,
			Drink:              src.Drink,
			FloorCondition:     src.FloorCondition,
			Attributes:         src.Attributes,
			ContactName:        src.ContactName,
			ContactEmail:       src.ContactEmail,
			OrganizationID:     targetOrgID,
		},
	}
	if src.Pricing != nil {
		cloneReq.Pricing = src.Pricing
	}
	// Tags.
	cloneReq.Tags = append([]string(nil), src.Tags...)

	// Location: re-use existing location_id directly without creating a new one.
	var locationID int64
	if src.LocationID != nil {
		locationID = int64(*src.LocationID)
	}

	// Insert the clone with no start/end time (zero = not set).
	cloneID, shortCode, _, err := insertEvent(tx,
		cloneReq.Title, cloneReq.Description,
		0, 0, // start_time, end_time cleared
		locationID,
		cloneReq.HasBall, cloneReq.HasWorkshop, cloneReq.HasFestival, false,
		cloneReq.WorkshopDifficulty, cloneReq.BookingURL,
		false, // is_published = 0
		targetOrgID,
		"", cloneReq.URL, "", 0, // uid, url, source, source_last_modified cleared
		cloneReq.Pricing, 0,
		cloneReq.Food, cloneReq.Drink, cloneReq.FloorCondition,
		cloneReq.Attributes, cloneReq.ContactName, cloneReq.ContactEmail,
		&callerID,
	)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Copy tags.
	syncEventTags(tx, cloneID, cloneReq.Tags)

	// Copy timetable times only (no description, no musicians).
	rows, err := tx.Query("SELECT start_time, end_time FROM timetable WHERE event_id=? ORDER BY start_time", srcID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var st, et int64
			if rows.Scan(&st, &et) == nil {
				tx.Exec("INSERT INTO timetable (event_id, start_time, end_time) VALUES (?, ?, ?)", cloneID, st, et)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	event, err := fetchEventByID(db, cloneID)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	event.ShortCode = shortCode

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(event)
}

// GET /api/v1/events.ics — public iCal feed of future published events, filterable by tag and location
func getEventsICS(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()
	tag := r.URL.Query().Get("tag")
	loc := r.URL.Query().Get("location")

	cntQ := "SELECT COUNT(*), MAX(e.created_at) FROM events e LEFT JOIN locations l ON e.location_id = l.id WHERE e.is_published = 1 AND e.start_time >= ?"
	cntArgs := []any{now}
	if tag != "" {
		cntQ += " AND EXISTS (SELECT 1 FROM event_tags et WHERE et.event_id = e.id AND et.tag = ?)"
		cntArgs = append(cntArgs, tag)
	}
	if loc != "" {
		cntQ += " AND l.location LIKE ?"
		cntArgs = append(cntArgs, "%"+loc+"%")
	}
	if checkPublicCacheHeaders(w, r, cntQ, cntArgs...) {
		return
	}

	query := eventListSelect + " WHERE e.is_published = 1 AND e.start_time >= ?"
	args := []any{now}

	if tag != "" {
		query += " AND EXISTS (SELECT 1 FROM event_tags et WHERE et.event_id = e.id AND et.tag = ?)"
		args = append(args, tag)
	}
	if loc != "" {
		query += " AND l.location LIKE ?"
		args = append(args, "%"+loc+"%")
	}

	query += " ORDER BY e.start_time ASC"

	rows, err := db.Query(query, args...)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	for rows.Next() {
		event, err := scanEventRow(rows)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		addEventToCalendar(cal, event)
	}

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=events.ics")
	w.Write([]byte(cal.Serialize()))
}

// GET /api/v1/events/{id}.ics — single-event iCal download
func getEventICS(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	event, err := fetchEventByID(db, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	addEventToCalendar(cal, event)
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="event-%d.ics"`, id))
	w.Write([]byte(cal.Serialize()))
}

// GET /api/v1/events/tag/{tag}.ics — public iCal feed of future published events for a specific tag
func getEventsByTagICS(w http.ResponseWriter, r *http.Request) {
	tag := r.PathValue("tag")
	now := time.Now().Unix()
	if checkPublicCacheHeaders(w, r,
		"SELECT COUNT(*), MAX(created_at) FROM events WHERE is_published = 1 AND start_time >= ? AND EXISTS (SELECT 1 FROM event_tags et WHERE et.event_id = events.id AND et.tag = ?)",
		now, tag) {
		return
	}
	query := eventListSelect + " WHERE e.is_published = 1 AND e.start_time >= ? AND EXISTS (SELECT 1 FROM event_tags et WHERE et.event_id = e.id AND et.tag = ?) ORDER BY e.start_time ASC"
	rows, err := db.Query(query, now, tag)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	for rows.Next() {
		event, err := scanEventRow(rows)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		addEventToCalendar(cal, event)
	}

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+tag+".ics")
	w.Write([]byte(cal.Serialize()))
}

// GET /api/v1/events/town/{town}.ics — public iCal feed of future published events for a specific town
func getEventsByTownICS(w http.ResponseWriter, r *http.Request) {
	town := r.PathValue("town")
	now := time.Now().Unix()
	if checkPublicCacheHeaders(w, r,
		"SELECT COUNT(*), MAX(e.created_at) FROM events e LEFT JOIN locations l ON e.location_id = l.id WHERE e.is_published = 1 AND e.start_time >= ? AND l.town LIKE ?",
		now, "%"+town+"%") {
		return
	}
	query := eventListSelect + " WHERE e.is_published = 1 AND e.start_time >= ? AND l.town LIKE ? ORDER BY e.start_time ASC"
	rows, err := db.Query(query, now, "%"+town+"%")
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	for rows.Next() {
		event, err := scanEventRow(rows)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		addEventToCalendar(cal, event)
	}

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+town+".ics")
	w.Write([]byte(cal.Serialize()))
}

// icsRouter wraps a handler to intercept GET requests whose path ends with ".ics".
// Go's net/http ServeMux requires wildcard segments to span the whole path segment,
// so patterns like {id}.ics are rejected at startup. This wrapper dispatches those
// paths manually before they reach the mux.
func icsRouter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, ".ics") {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		switch {
		case p == "/api/v1/events.ics":
			getEventsICS(w, r)
		case strings.HasPrefix(p, "/api/v1/events/tag/"):
			r.SetPathValue("tag", strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/events/tag/"), ".ics"))
			getEventsByTagICS(w, r)
		case strings.HasPrefix(p, "/api/v1/events/town/"):
			r.SetPathValue("town", strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/events/town/"), ".ics"))
			getEventsByTownICS(w, r)
		case strings.HasPrefix(p, "/api/v1/events/"):
			r.SetPathValue("id", strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/events/"), ".ics"))
			getEventICS(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// checkPublicCacheHeaders runs cntQuery (must SELECT COUNT(*), MAX(created_at))
// and emits ETag/Last-Modified/Cache-Control headers. Returns true and writes
// 304 when the client's cached copy is still fresh; caller must return immediately.
func checkPublicCacheHeaders(w http.ResponseWriter, r *http.Request, cntQuery string, args ...any) bool {
	var n int
	var modStr sql.NullString
	if err := db.QueryRow(cntQuery, args...).Scan(&n, &modStr); err != nil {
		return false
	}
	var lastMod time.Time
	if modStr.Valid && modStr.String != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, modStr.String); err == nil {
				lastMod = t
				break
			}
		}
	}
	etag := fmt.Sprintf(`"%d-%d"`, n, lastMod.Unix())
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", lastMod.UTC().Format(http.TimeFormat))
	w.Header().Set("Cache-Control", "public, max-age=60")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		if t, err := http.ParseTime(ims); err == nil && !lastMod.After(t) {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	return false
}

type Tag struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

func knownTagSlugs() (map[string]bool, error) {
	rows, err := db.Query("SELECT slug FROM tags")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	slugs := make(map[string]bool)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		slugs[s] = true
	}
	return slugs, nil
}

func filterKnownTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	known, err := knownTagSlugs()
	if err != nil {
		return nil
	}
	var result []string
	for _, t := range tags {
		if known[t] {
			result = append(result, t)
		}
	}
	return result
}

func validateTags(tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	known, err := knownTagSlugs()
	if err != nil {
		return err
	}
	for _, t := range tags {
		if !known[t] {
			return fmt.Errorf("unknown tag %q", t)
		}
	}
	return nil
}

// GET /api/v1/tags
func getTags(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rows, err := db.Query("SELECT slug, name, category FROM tags ORDER BY category, name")
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	tags := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.Slug, &t.Name, &t.Category); err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tags = append(tags, t)
	}
	json.NewEncoder(w).Encode(tags)
}

// POST /api/v1/events/bulk-set-attributes — set org, tags, dances, food/drink,
// accessibility attributes, and/or pricing type on multiple events.
// Nil fields are skipped. Tags and dances are additive (existing kept).
func bulkSetEventAttributes(w http.ResponseWriter, r *http.Request) {
	callerID, role := callerFromRequest(r)
	if role != RoleAdmin && role != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		IDs         []int    `json:"ids"`
		OrgID       *int     `json:"org_id"`       // nil = skip; set to apply (can be 0 to unset)
		AddTags     []string `json:"add_tags"`     // nil = skip; additive
		AddDances   []int    `json:"add_dances"`   // nil = skip; additive (dance IDs)
		Food        *string  `json:"food"`         // nil = skip; "" unsets
		Drink       *string  `json:"drink"`        // nil = skip; "" unsets
		Wheelchair  *bool    `json:"wheelchair"`   // nil = skip
		Bar         *bool    `json:"bar"`          // nil = skip
		Kitchen     *bool    `json:"kitchen"`      // nil = skip
		PricingType *string  `json:"pricing_type"` // nil = skip; "free"/"donation"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		writeError(w, "ids required", http.StatusBadRequest)
		return
	}
	// Non-admins may only reassign to an org they belong to, and may not unset the org.
	if role != RoleAdmin && req.OrgID != nil {
		if *req.OrgID == 0 || !isOrgMember(callerID, *req.OrgID) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
	}
	for _, id := range req.IDs {
		if role != RoleAdmin && !isOrgMemberOfEvent(callerID, id) {
			continue
		}
		if req.OrgID != nil {
			if *req.OrgID == 0 {
				db.Exec("UPDATE events SET organization_id=NULL WHERE id=?", id)
			} else {
				db.Exec("UPDATE events SET organization_id=? WHERE id=?", *req.OrgID, id)
			}
		}
		if req.AddTags != nil {
			for _, t := range req.AddTags {
				if t = strings.TrimSpace(t); t != "" {
					db.Exec("INSERT OR IGNORE INTO event_tags (event_id, tag) VALUES (?, ?)", id, t)
				}
			}
		}
		if req.AddDances != nil {
			for _, did := range req.AddDances {
				db.Exec("INSERT OR IGNORE INTO event_dances (event_id, dance_id) VALUES (?, ?)", id, did)
			}
		}
		if req.Food != nil {
			db.Exec("UPDATE events SET food=? WHERE id=?", *req.Food, id)
		}
		if req.Drink != nil {
			db.Exec("UPDATE events SET drink=? WHERE id=?", *req.Drink, id)
		}
		if req.Wheelchair != nil || req.Bar != nil || req.Kitchen != nil {
			var attrsRaw string
			db.QueryRow("SELECT COALESCE(attributes,'{}') FROM events WHERE id=?", id).Scan(&attrsRaw)
			attrs := make(map[string]bool)
			json.Unmarshal([]byte(attrsRaw), &attrs)
			if req.Wheelchair != nil {
				attrs["wheelchair"] = *req.Wheelchair
			}
			if req.Bar != nil {
				attrs["bar"] = *req.Bar
			}
			if req.Kitchen != nil {
				attrs["kitchen"] = *req.Kitchen
			}
			db.Exec("UPDATE events SET attributes=? WHERE id=?", attrsJSON(attrs), id)
		}
		if req.PricingType != nil {
			var pricingRaw string
			db.QueryRow("SELECT COALESCE(pricing,'') FROM events WHERE id=?", id).Scan(&pricingRaw)
			p := Pricing{Type: *req.PricingType}
			if pricingRaw != "" {
				var existing Pricing
				if json.Unmarshal([]byte(pricingRaw), &existing) == nil {
					existing.Type = *req.PricingType
					p = existing
				}
			}
			b, _ := json.Marshal(p)
			db.Exec("UPDATE events SET pricing=? WHERE id=?", string(b), id)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/events/bulk-set-location — set location_id on multiple events.
// admin: unrestricted. user: skips events where caller is not an org member.
func bulkSetEventLocation(w http.ResponseWriter, r *http.Request) {
	callerID, role := callerFromRequest(r)
	if role != RoleAdmin && role != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		IDs        []int `json:"ids"`
		LocationID int   `json:"location_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 || req.LocationID == 0 {
		writeError(w, "ids and location_id are required", http.StatusBadRequest)
		return
	}
	for _, id := range req.IDs {
		if role != RoleAdmin && !isOrgMemberOfEvent(callerID, id) {
			continue
		}
		db.Exec("UPDATE events SET location_id=? WHERE id=?", req.LocationID, id)
	}
	w.WriteHeader(http.StatusNoContent)
}
