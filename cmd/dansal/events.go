package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
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
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

type Event struct {
	ID                   int              `json:"id"`
	UID                  string           `json:"uid,omitempty"`
	Title                string           `json:"title"`
	Description          string           `json:"description"`
	StartTime            string           `json:"start_time"`
	EndTime              string           `json:"end_time"`
	HasBall              bool             `json:"has_ball"`
	HasWorkshop          bool             `json:"has_workshop"`
	HasFestival          bool             `json:"has_festival"`
	WorkshopDifficulty   string           `json:"workshop_difficulty,omitempty"`
	IsCancelled          bool             `json:"is_cancelled"`
	Tags                 []string         `json:"tags"`
	IsPublished          bool             `json:"is_published"`
	ShortCode            string           `json:"short_code"`
	URL                  string           `json:"url,omitempty"`
	Source               string           `json:"source,omitempty"`
	CreatedAt            string           `json:"created_at"`
	ImageURL             string           `json:"image_url,omitempty"`
	OrganizationID       *int             `json:"organization_id,omitempty"`
	Editable             *bool            `json:"editable,omitempty"`
	Cancelable           *bool            `json:"cancelable,omitempty"`
	CreatedByID          *int             `json:"created_by_id,omitempty"`
	Timetable            []TimetableEntry `json:"timetable,omitempty"`
	Pricing              *Pricing         `json:"pricing,omitempty"`
	Locations            []Location       `json:"locations,omitempty"`
	Musicians            []Musician       `json:"musicians,omitempty"`
	Instructors          []Instructor     `json:"instructors,omitempty"`
	LocationID           *int             `json:"location_id,omitempty"`
	Location             *Location        `json:"location,omitempty"`
	Attributes           map[string]bool  `json:"attributes,omitempty"`
	FloorCondition       string           `json:"floor_condition,omitempty"`
	ContactName          string           `json:"contact_name,omitempty"`
	ContactEmail         string           `json:"contact_email,omitempty"`
	BookingURL           string           `json:"booking_url,omitempty"`
	Availability         string           `json:"availability,omitempty"`
	TicketsTotal         int              `json:"tickets_total,omitempty"`
	BookingEnabled       bool             `json:"booking_enabled,omitempty"`
	Food                 string           `json:"food,omitempty"`
	Drink                string           `json:"drink,omitempty"`
	DanceNames           []string         `json:"dance_names,omitempty"`
	ChangedAt            string           `json:"changed_at,omitempty"`
	ChangedBy            string           `json:"changed_by,omitempty"`
	FetchSourceID        int              `json:"fetch_source_id,omitempty"`
	SeriesID             *int             `json:"series_id,omitempty"`
	NeedsDuplicateReview bool             `json:"needs_duplicate_review,omitempty"`
	DuplicateOfID        *int             `json:"duplicate_of_id,omitempty"`
	TagsJSON             string           `json:"-"`
	PricingJSON          string           `json:"-"`
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
	WorkshopDifficulty string               `json:"workshop_difficulty,omitempty" enum:"beginner,advanced,profi"`
	IsCancelled        bool                 `json:"is_cancelled"`
	IsPublished        bool                 `json:"is_published"`
	Tags               []string             `json:"tags"`
	URL                string               `json:"url"`
	OrganizationID     *int                 `json:"organization_id"`
	LocationID         *int                 `json:"location_id,omitempty"`
	Location           EventLocationRequest `json:"location"`
	Pricing            *Pricing             `json:"pricing"`
	Musicians          []int                `json:"musicians"`
	Instructors        []int                `json:"instructors,omitempty"`
	Dances             []int                `json:"dances,omitempty"`
	BookingURL         string               `json:"booking_url,omitempty"`
	Availability       string               `json:"availability,omitempty"`
	TicketsTotal       int                  `json:"tickets_total,omitempty"`
	BookingEnabled     bool                 `json:"booking_enabled,omitempty"`
	Food               string               `json:"food,omitempty" enum:"sold,potluck,none"`
	Drink              string               `json:"drink,omitempty" enum:"alcohol,soft,none"`
	FloorCondition     string               `json:"floor_condition,omitempty" enum:"parquet,stone,tiles,grass,sand,pavement"`
	Attributes         map[string]bool      `json:"attributes,omitempty"`
	ContactName        string               `json:"contact_name,omitempty"`
	ContactEmail       string               `json:"contact_email,omitempty"`
}

type EventUpdateRequest struct {
	EventWriteRequest
}

// EventMergePatchRequest is the body accepted by PATCH /api/v1/events/{id}
// (Content-Type: application/merge-patch+json — RFC 7396). Every field is a
// pointer: an omitted key leaves the existing value unchanged; a present key
// sets it (an explicit "" clears a plain text field). Array/map fields
// (tags, musicians, instructors, dances, attributes) are replaced wholesale
// when present, never merged element-by-element.
//
// There is no nested "location" field here (unlike EventWriteRequest): PATCH
// only supports repointing an event at an existing location via location_id.
// Creating/updating the location itself is a separate concern — use the
// locations endpoints, or PUT the event with a full location payload.
//
// As with locations (#722), fields that are already optional at the domain
// level (organization_id, location_id, pricing) can't distinguish "omitted"
// from "explicitly cleared" through a single Go pointer — send a full PUT
// for that case.
type EventMergePatchRequest struct {
	Title              *string          `json:"title,omitempty"`
	Description        *string          `json:"description,omitempty"`
	StartTime          *string          `json:"start_time,omitempty"`
	EndTime            *string          `json:"end_time,omitempty"`
	HasBall            *bool            `json:"has_ball,omitempty"`
	HasWorkshop        *bool            `json:"has_workshop,omitempty"`
	HasFestival        *bool            `json:"has_festival,omitempty"`
	WorkshopDifficulty *string          `json:"workshop_difficulty,omitempty" enum:"beginner,advanced,profi"`
	IsCancelled        *bool            `json:"is_cancelled,omitempty"`
	IsPublished        *bool            `json:"is_published,omitempty"`
	Tags               *[]string        `json:"tags,omitempty"`
	URL                *string          `json:"url,omitempty"`
	OrganizationID     *int             `json:"organization_id,omitempty"`
	LocationID         *int             `json:"location_id,omitempty"`
	Pricing            *Pricing         `json:"pricing,omitempty"`
	Musicians          *[]int           `json:"musicians,omitempty"`
	Instructors        *[]int           `json:"instructors,omitempty"`
	Dances             *[]int           `json:"dances,omitempty"`
	BookingURL         *string          `json:"booking_url,omitempty"`
	Availability       *string          `json:"availability,omitempty"`
	TicketsTotal       *int             `json:"tickets_total,omitempty"`
	BookingEnabled     *bool            `json:"booking_enabled,omitempty"`
	Food               *string          `json:"food,omitempty" enum:"sold,potluck,none"`
	Drink              *string          `json:"drink,omitempty" enum:"alcohol,soft,none"`
	FloorCondition     *string          `json:"floor_condition,omitempty" enum:"parquet,stone,tiles,grass,sand,pavement"`
	Attributes         *map[string]bool `json:"attributes,omitempty"`
	ContactName        *string          `json:"contact_name,omitempty"`
	ContactEmail       *string          `json:"contact_email,omitempty"`
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
	Location    string   `json:"location"`
	ShortName   string   `json:"short_name,omitempty"`
	Address     string   `json:"address"`
	Zipcode     string   `json:"zipcode"`
	Town        string   `json:"town"`
	Country     string   `json:"country"`
	CountryCode string   `json:"country_code,omitempty"`
	Region      string   `json:"region,omitempty"`
	Latitude    *float64 `json:"latitude,omitempty"`
	Longitude   *float64 `json:"longitude,omitempty"`
	Eventsite   string   `json:"eventsite"`
	OsmID       *int64   `json:"osm_id,omitempty"`
	OsmType     string   `json:"osm_type,omitempty"`
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

// VocabEntry describes one allowed value in a closed vocabulary, pairing the
// value stored on the record with the i18n key dansal's admin UI uses to
// render it. Clients may translate label_key themselves or fall back to slug.
type VocabEntry struct {
	Slug     string `json:"slug"`
	LabelKey string `json:"label_key"`
}

// Closed vocabularies for event/location fields. "" is always accepted and
// means "inherit from venue" (event floor_condition) or "not set" (all other
// fields, and floor_condition/parking on a location).
var (
	validFoodValues           = map[string]bool{"": true, "sold": true, "potluck": true, "none": true}
	validDrinkValues          = map[string]bool{"": true, "alcohol": true, "soft": true, "none": true}
	validFloorConditionValues = map[string]bool{"": true, "parquet": true, "stone": true, "tiles": true, "grass": true, "sand": true, "pavement": true}
	validParkingValues        = map[string]bool{"": true, "none": true, "free": true, "paid": true}
)

func validFood(s string) bool           { return validFoodValues[s] }
func validDrink(s string) bool          { return validDrinkValues[s] }
func validFloorCondition(s string) bool { return validFloorConditionValues[s] }
func validParking(s string) bool        { return validParkingValues[s] }

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
const eventListSelect = `SELECT e.id, e.uid, e.title, e.description, e.start_time, e.end_time, e.has_ball, e.has_workshop, e.has_festival, e.is_cancelled, COALESCE((SELECT GROUP_CONCAT(et.tag, ',') FROM event_tags et WHERE et.event_id = e.id), ''), e.is_published, COALESCE(e.short_code,''), COALESCE(e.url,''), COALESCE(e.source,''), e.created_at, COALESCE(l.location,''), COALESCE(l.short_name,''), COALESCE(l.address,''), COALESCE(l.zipcode,''), e.organization_id, COALESCE(e.pricing,''), e.location_id, COALESCE(l.town,''), COALESCE(l.country,''), l.latitude, l.longitude, COALESCE(e.workshop_difficulty,''), COALESCE(e.booking_url,''), COALESCE(e.availability,''), COALESCE(e.tickets_total,0), COALESCE(e.booking_enabled,0), COALESCE(dn.dance_names,''), COALESCE(e.changed_at,0), COALESCE(e.changed_by,''), COALESCE(e.fetch_source_id,0), COALESCE(e.food,''), COALESCE(e.drink,''), COALESCE(l.attributes,'{}'), COALESCE(e.attributes,'{}'), COALESCE(NULLIF(e.contact_name,''), o.contact_name, ''), COALESCE(NULLIF(e.contact_email,''), o.contact_email, ''), COALESCE(l.parking,''), COALESCE(l.floor_condition,''), COALESCE(e.floor_condition,''), e.created_by_id, l.osm_id, COALESCE(l.osm_type,''), COALESCE(l.geohash,''), e.series_id, e.needs_duplicate_review, e.duplicate_of_id FROM events e LEFT JOIN locations l ON e.location_id = l.id LEFT JOIN (SELECT ed.event_id, GROUP_CONCAT(d.name,',') AS dance_names FROM event_dances ed JOIN dances d ON d.id=ed.dance_id GROUP BY ed.event_id) dn ON dn.event_id = e.id LEFT JOIN organizations o ON e.organization_id = o.id`

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
	var createdByID, seriesID, duplicateOfID sql.NullInt64
	var needsDuplicateReviewInt int
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
		&createdByID, &loc.OsmID, &loc.OsmType, &loc.Geohash, &seriesID,
		&needsDuplicateReviewInt, &duplicateOfID); err != nil {
		return Event{}, err
	}
	event.NeedsDuplicateReview = needsDuplicateReviewInt == 1
	if duplicateOfID.Valid {
		v := int(duplicateOfID.Int64)
		event.DuplicateOfID = &v
	}
	if seriesID.Valid {
		v := int(seriesID.Int64)
		event.SeriesID = &v
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
		*query += ` AND json_extract(e.pricing,'$.type')='free'`
	}
	// Task D: wheelchair filter
	if q.Get("wheelchair") == "1" {
		*query += ` AND json_extract(e.attributes,'$.wheelchair')=1`
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

// applyListPagination appends "ORDER BY <orderBy> LIMIT ? OFFSET ?" to a list query.
// orderBy is a literal SQL fragment (never user-supplied).
func applyListPagination(r *http.Request, orderBy string, query *string, args *[]any) {
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
	*query += " ORDER BY " + orderBy + " LIMIT ? OFFSET ?"
	*args = append(*args, limit, offset)
}

// GET /api/v1/vocabulary
//
// Empty-string ("") semantics for food/drink/floor_condition/parking:
//   - floor_condition on an event: inherit from the venue's own floor_condition
//   - all other fields, and floor_condition/parking on a location: not set
func getVocabulary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"event_types": []map[string]string{
			{"key": "ball", "label": "Ball"},
			{"key": "workshop", "label": "Workshop"},
			{"key": "festival", "label": "Festival"},
		},
		"workshop_difficulties": []VocabEntry{
			{"beginner", "workshop_difficulty_beginner"},
			{"advanced", "workshop_difficulty_advanced"},
			{"profi", "workshop_difficulty_profi"},
		},
		"pricing_types": []VocabEntry{
			{"free", "evt_free"},
			{"donation", "evt_donation"},
			{"single", "evt_pricing_single"},
			{"multiple", "evt_pricing_multiple"},
		},
		"attributes": []string{"wheelchair", "bar", "kitchen"},
		"osm_types":  []string{"node", "way", "relation"},
		"food": []VocabEntry{
			{"sold", "food_sold"},
			{"potluck", "food_potluck"},
			{"none", "food_none"},
		},
		"drink": []VocabEntry{
			{"alcohol", "drink_alcohol"},
			{"soft", "drink_soft"},
			{"none", "drink_none"},
		},
		"floor_condition": []VocabEntry{
			{"parquet", "floor_parquet"},
			{"stone", "floor_stone"},
			{"tiles", "floor_tiles"},
			{"grass", "floor_grass"},
			{"sand", "floor_sand"},
			{"pavement", "floor_pavement"},
		},
		"parking": []VocabEntry{
			{"none", "parking_none"},
			{"free", "parking_free"},
			{"paid", "parking_paid"},
		},
		"contact_post_types": []VocabEntry{
			{"ride_offer", "board_ride_offer"},
			{"ride_request", "board_ride_request"},
			{"sleep_offer", "board_sleep_offer"},
			{"sleep_request", "board_sleep_request"},
			{"ticket_offer", "board_ticket_offer"},
			{"ticket_request", "board_ticket_request"},
			{"lost_item", "board_lost_item"},
			{"found_item", "board_found_item"},
		},
		"timetable_entry_types": []VocabEntry{
			{"bal", "tt_type_bal"},
			{"workshop", "tt_type_workshop"},
		},
		// price_labels are suggestions only — Price.Label remains free text
		// with no server-side validation against this list.
		"price_labels": []VocabEntry{
			{"normal", "price_label_normal"},
			{"reduced", "price_label_reduced"},
			{"presale", "price_label_presale"},
			{"member", "price_label_member"},
			{"supporter", "price_label_supporter"},
		},
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

// titlesFuzzyOverlap reports whether one title is a case-insensitive substring
// of the other (e.g. "ABGESAGT - Tanzabend" contains "Tanzabend"). Used only
// for the low-confidence duplicate-review tier; titles shorter than 8
// characters are excluded since short substrings would match too broadly.
func titlesFuzzyOverlap(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if len(a) < 8 || len(b) < 8 {
		return false
	}
	return strings.Contains(a, b) || strings.Contains(b, a)
}

// flagDuplicateReview marks two events as needing admin review for a possible
// merge, and notifies admin users. Best-effort: errors are logged, not fatal,
// since the events themselves were already inserted/updated successfully.
func flagDuplicateReview(q querier, newID, candidateID int, title string) {
	if _, err := q.Exec("UPDATE events SET needs_duplicate_review = 1, duplicate_of_id = ? WHERE id = ?", candidateID, newID); err != nil {
		log.Printf("flagDuplicateReview: update new event %d: %v", newID, err)
	}
	if _, err := q.Exec("UPDATE events SET needs_duplicate_review = 1, duplicate_of_id = ? WHERE id = ?", newID, candidateID); err != nil {
		log.Printf("flagDuplicateReview: update candidate event %d: %v", candidateID, err)
	}
	go notifyAdminsDuplicateReview(title)
}

// Outcome values returned by insertEvent, describing what happened to the row.
const (
	outcomeNew       = "new"
	outcomeUpdated   = "updated"
	outcomeUnchanged = "unchanged"
)

// ImportCounts tallies insertEvent outcomes across a batch import run.
type ImportCounts struct {
	New       int
	Updated   int
	Unchanged int
}

// Add tallies a single insertEvent outcome.
func (c *ImportCounts) Add(outcome string) {
	switch outcome {
	case outcomeNew:
		c.New++
	case outcomeUpdated:
		c.Updated++
	case outcomeUnchanged:
		c.Unchanged++
	}
}

// Merge folds another batch's counts into c.
func (c *ImportCounts) Merge(other ImportCounts) {
	c.New += other.New
	c.Updated += other.Updated
	c.Unchanged += other.Unchanged
}

// AllNew reports whether every event in the batch was newly inserted.
func (c ImportCounts) AllNew() bool {
	return c.Updated == 0 && c.Unchanged == 0
}

// insertEvent upserts an event. Returns (id, shortCode, outcome, error) where
// outcome is one of outcomeNew/outcomeUpdated/outcomeUnchanged.
// Deduplication order: UID exact match → URL exact match → title+location+time fuzzy match (±3 h).
// The URL and fuzzy tiers run whenever the previous tier misses, so two feeds that
// publish the same event with different UIDs (or none) converge to a single row.
func insertEvent(q querier, title, description string, startTime, endTime int64, locationID int64, hasBall, hasWorkshop, hasFestival, isCancelled bool, workshopDifficulty, bookingURL string, isPublished bool, organizationID *int, uid, url, source string, sourceLastModified int64, pricing *Pricing, fetchSourceID int, food, drink, floorCondition string, attributes map[string]bool, contactName, contactEmail string, createdByID *int) (int, string, string, error) {
	var existingID int
	var existingShortCode string
	var existingSourceLastModified int64
	var existingChangedAt int64
	var lookupErr error = sql.ErrNoRows
	var uidArg any
	if uid != "" {
		uidArg = uid
	}

	if uid != "" {
		lookupErr = q.QueryRow(
			"SELECT id, short_code, COALESCE(source_last_modified, 0), COALESCE(changed_at, 0) FROM events WHERE uid = ?", uid,
		).Scan(&existingID, &existingShortCode, &existingSourceLastModified, &existingChangedAt)
		if lookupErr != nil && lookupErr != sql.ErrNoRows {
			return 0, "", "", lookupErr
		}
	}

	const threeHours = int64(3 * 60 * 60)

	// URL tier: fires when uid is absent or not found. Constrained to a ±3h
	// window around start_time — otherwise a feed that reuses one generic URL
	// (e.g. its homepage) for every event permanently locks tier 2 onto the
	// first event ever imported with that URL, silently absorbing every later,
	// unrelated event sharing the same URL (#702).
	if lookupErr == sql.ErrNoRows && url != "" {
		lookupErr = q.QueryRow(
			"SELECT id, short_code, COALESCE(source_last_modified, 0), COALESCE(changed_at, 0) FROM events WHERE url = ? AND ABS(start_time - ?) < ?",
			url, startTime, threeHours,
		).Scan(&existingID, &existingShortCode, &existingSourceLastModified, &existingChangedAt)
		if lookupErr != nil && lookupErr != sql.ErrNoRows {
			return 0, "", "", lookupErr
		}
	}

	// Fuzzy fallback: fires when both uid and url lookups missed.
	if lookupErr == sql.ErrNoRows {
		if locationID > 0 {
			// Tier 3: known location + time, no title check. Titles get rewritten
			// over an event's lifetime (placeholder -> lineup -> cancellation
			// notice), so once uid/url have both missed, same venue + same slot
			// is already a strong enough signal to auto-merge on its own.
			lookupErr = q.QueryRow(
				"SELECT id, short_code, COALESCE(source_last_modified, 0), COALESCE(changed_at, 0) FROM events WHERE location_id = ? AND ABS(start_time - ?) < ?",
				locationID, startTime, threeHours,
			).Scan(&existingID, &existingShortCode, &existingSourceLastModified, &existingChangedAt)
		}
		if lookupErr == sql.ErrNoRows {
			// Tier 4: title + time only. Fires when locationID == 0 (feed provided no
			// resolvable location name) or when tier 3 missed (feed location name didn't
			// match the DB name of the event's stored location, e.g. after HTML-entity
			// decoding or a venue rename that caused ensureLocation to create a new row).
			// Title is still required here since, without a location signal, matching
			// on time alone would be far too promiscuous.
			lookupErr = q.QueryRow(
				"SELECT id, short_code, COALESCE(source_last_modified, 0), COALESCE(changed_at, 0) FROM events WHERE title = ? AND ABS(start_time - ?) < ?",
				title, startTime, threeHours,
			).Scan(&existingID, &existingShortCode, &existingSourceLastModified, &existingChangedAt)
		}
	}

	if lookupErr != nil && lookupErr != sql.ErrNoRows {
		return 0, "", "", lookupErr
	}

	// Tier 5: low-confidence review candidate. Fires only when tiers 1-4 all
	// missed (e.g. the venue changed AND the feed regenerated the uid AND the
	// title was rewritten to announce the move). Same originating feed + same
	// time window + fuzzy title overlap is too weak a signal to auto-merge on,
	// so instead of guessing we insert as new and flag both rows for an admin
	// to resolve via the existing merge UI.
	var duplicateReviewCandidateID int
	if lookupErr == sql.ErrNoRows && fetchSourceID > 0 && title != "" {
		rows, err := q.Query(
			"SELECT id, title FROM events WHERE fetch_source_id = ? AND ABS(start_time - ?) < ?",
			fetchSourceID, startTime, threeHours,
		)
		if err == nil {
			for rows.Next() {
				var candID int
				var candTitle string
				if rows.Scan(&candID, &candTitle) == nil && titlesFuzzyOverlap(title, candTitle) {
					duplicateReviewCandidateID = candID
					break
				}
			}
			rows.Close()
		}
	}

	var pricingArg any
	if pricing != nil {
		if b, err := json.Marshal(pricing); err == nil {
			pricingArg = string(b)
		}
	}

	var locIDArg any
	if locationID != 0 {
		locIDArg = locationID
	}

	var orgIDArg any
	if organizationID != nil {
		orgIDArg = *organizationID
	}

	if lookupErr == nil {
		// Skip update when the source tells us nothing has changed since last import.
		if sourceLastModified > 0 && sourceLastModified <= existingSourceLastModified {
			return existingID, existingShortCode, outcomeUnchanged, nil
		}

		var slmArg any
		if sourceLastModified > 0 {
			slmArg = sourceLastModified
		}

		// Protect manual edits from being overwritten by imports (source != "").
		if source != "" && existingChangedAt > 0 {
			if sourceLastModified == 0 || sourceLastModified <= existingChangedAt {
				// Source has no timestamp or is not newer than the manual edit — skip.
				return existingID, existingShortCode, outcomeUnchanged, nil
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
				uid=COALESCE(uid,?),
				title=?,
				description=CASE WHEN ?!='' THEN ? ELSE description END,
				start_time=?, end_time=?,
				location_id=CASE WHEN ?!=0 THEN ? ELSE location_id END,
				is_cancelled=?,
				workshop_difficulty=CASE WHEN ?!='' THEN ? ELSE workshop_difficulty END,
				url=CASE WHEN ? IS NOT NULL THEN ? ELSE url END,
				source_last_modified=?,
				pricing=CASE WHEN ? IS NOT NULL THEN ? ELSE pricing END,
				fetch_source_id=COALESCE(?,fetch_source_id),
				organization_id=COALESCE(organization_id,?)
				WHERE id=?`,
				uidArg,
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
				orgIDArg,
				existingID,
			)
			if err != nil {
				return 0, "", "", err
			}
			return existingID, existingShortCode, outcomeUpdated, nil
		}

		var err error
		if source != "" {
			var fsArg any
			if fetchSourceID > 0 {
				fsArg = fetchSourceID
			}
			_, err = q.Exec(
				"UPDATE events SET uid=COALESCE(uid,?), description=?, start_time=?, end_time=?, location_id=COALESCE(?,location_id), has_ball=?, has_workshop=?, has_festival=?, is_cancelled=?, workshop_difficulty=?, is_published=?, url=?, source_last_modified=?, pricing=?, changed_at=?, changed_by=?, fetch_source_id=COALESCE(?,fetch_source_id), organization_id=COALESCE(organization_id,?) WHERE id=?",
				uidArg, description, startTime, endTime, locIDArg, hasBall, hasWorkshop, hasFestival, isCancelled, workshopDifficulty, isPublished, urlVal(url), slmArg, pricingArg,
				time.Now().UTC().Unix(), "fetch", fsArg, orgIDArg, existingID,
			)
		} else {
			_, err = q.Exec(
				"UPDATE events SET description=?, start_time=?, end_time=?, location_id=COALESCE(?,location_id), has_ball=?, has_workshop=?, has_festival=?, is_cancelled=?, workshop_difficulty=?, is_published=?, url=?, source_last_modified=?, pricing=?, organization_id=COALESCE(organization_id,?) WHERE id=?",
				description, startTime, endTime, locIDArg, hasBall, hasWorkshop, hasFestival, isCancelled, workshopDifficulty, isPublished, urlVal(url), slmArg, pricingArg, orgIDArg, existingID,
			)
		}
		if err != nil {
			return 0, "", "", err
		}
		return existingID, existingShortCode, outcomeUpdated, nil
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
			return 0, "", "", err
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
			uidArg, title, description, startTime, endTime, locIDArg, hasBall, hasWorkshop, hasFestival, isCancelled, workshopDifficulty, isPublished, orgIDArg, shortCode, urlVal(url), sourceArg, slmArg, pricingArg, urlVal(bookingURL), insChangedAt, insChangedBy, insFetchSourceID, food, drink, floorCondition, attrsJSON(attributes), contactName, contactEmail, createdByArg,
		)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "short_code") {
			return 0, "", "", err
		}
	}
	if err != nil {
		return 0, "", "", err
	}
	id, _ := result.LastInsertId()
	syncEventLocationGeohash(int(id))
	if duplicateReviewCandidateID > 0 {
		flagDuplicateReview(q, int(id), duplicateReviewCandidateID, title)
	}
	return int(id), shortCode, outcomeNew, nil
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
// Returns (events, counts, error) tallying how many events were new/updated/unchanged.
func createEventFromRequest(q querier, req EventCreateRequest, locationID int64, isPublished bool, createdByID *int) ([]Event, ImportCounts, error) {
	syncEventTypeTags(&req.EventWriteRequest)
	var createdEvents []Event
	var counts ImportCounts

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
			return nil, counts, fmt.Errorf("start_time: %w", err)
		}
		endTime, err := parseTimeToUnix(entry.endTime)
		if err != nil {
			return nil, counts, fmt.Errorf("end_time: %w", err)
		}

		id, shortCode, outcome, err := insertEvent(q, req.Title, entry.description, startTime, endTime, locationID, req.HasBall, req.HasWorkshop, req.HasFestival, req.IsCancelled, req.WorkshopDifficulty, req.BookingURL, isPublished, req.OrganizationID, req.UID, req.URL, req.Source, req.SourceLastModified, req.Pricing, req.FetchSourceID, req.Food, req.Drink, req.FloorCondition, req.Attributes, req.ContactName, req.ContactEmail, createdByID)
		if err != nil {
			return nil, counts, err
		}
		counts.Add(outcome)

		if len(req.Musicians) > 0 {
			if _, err := q.Exec("DELETE FROM event_musicians WHERE event_id = ?", id); err != nil {
				return nil, counts, err
			}
			if err := batchInsertPairs(q, "event_musicians", "event_id", "musician_id", id, req.Musicians); err != nil {
				return nil, counts, err
			}
		}
		if len(req.Instructors) > 0 {
			if _, err := q.Exec("DELETE FROM event_instructors WHERE event_id = ?", id); err != nil {
				return nil, counts, err
			}
			if err := batchInsertPairs(q, "event_instructors", "event_id", "instructor_id", id, req.Instructors); err != nil {
				return nil, counts, err
			}
		}
		if len(req.Dances) > 0 {
			if _, err := q.Exec("DELETE FROM event_dances WHERE event_id = ?", id); err != nil {
				return nil, counts, err
			}
			if err := batchInsertPairs(q, "event_dances", "event_id", "dance_id", id, req.Dances); err != nil {
				return nil, counts, err
			}
		}
		if err := syncEventTags(q, id, req.Tags); err != nil {
			return nil, counts, err
		}

		event, err := fetchEventByID(q, id)
		if err != nil {
			return nil, counts, err
		}
		event.ShortCode = shortCode
		createdEvents = append(createdEvents, event)
	}

	return createdEvents, counts, nil
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
// editable/cancelable: admin=any; user/publisher=own orgs.
// The org membership set is fetched once to avoid N+1 queries.
func annotateEditable(events []Event, userRole string, userID int) {
	isAdmin := userRole == RoleAdmin
	var memberOrgs map[int]bool
	if userRole == RoleUser || userRole == RolePublisher {
		memberOrgs = userOrgSet(userID)
	}
	for i := range events {
		inOrg := memberOrgs != nil && events[i].OrganizationID != nil && memberOrgs[*events[i].OrganizationID]
		editable := isAdmin || inOrg
		cancelable := editable
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

// batchInsertPairs inserts (leftID, rightVal) rows into a junction table with
// a single multi-row INSERT, avoiding one query per row.
func batchInsertPairs[T any](q querier, table, leftCol, rightCol string, leftID int, rightVals []T) error {
	if len(rightVals) == 0 {
		return nil
	}
	placeholders := make([]string, len(rightVals))
	args := make([]any, 0, len(rightVals)*2)
	for i, v := range rightVals {
		placeholders[i] = "(?, ?)"
		args = append(args, leftID, v)
	}
	_, err := q.Exec("INSERT OR IGNORE INTO "+table+" ("+leftCol+", "+rightCol+") VALUES "+strings.Join(placeholders, ","), args...)
	return err
}

// syncEventTags replaces all event_tags rows for eventID with the given tags.
func syncEventTags(q querier, eventID int, tags []string) error {
	if _, err := q.Exec("DELETE FROM event_tags WHERE event_id = ?", eventID); err != nil {
		return err
	}
	clean := make([]string, 0, len(tags))
	for _, tag := range tags {
		if t := strings.TrimSpace(tag); t != "" {
			clean = append(clean, t)
		}
	}
	return batchInsertPairs(q, "event_tags", "event_id", "tag", eventID, clean)
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

	// Total count ignoring LIMIT/OFFSET, so clients (e.g. the index page) can tell
	// pagination truncated the result. Doesn't account for the in-Go geo radius
	// post-filter below, since that can't be expressed in the count query.
	var totalCount int
	db.QueryRow("SELECT COUNT(*) FROM ("+query+")", args...).Scan(&totalCount)

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
		w.Header().Set("X-Total-Count", strconv.Itoa(totalCount))
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
		body, ok := readBodyOrError(w, r)
		if !ok {
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
		body, ok := readBodyOrError(w, r)
		if !ok {
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
		syncEventTypeTags(&requests[i].EventWriteRequest)
	}

	for _, req := range requests {
		if !validFood(req.Food) {
			writeError(w, "invalid food value", http.StatusBadRequest)
			return
		}
		if !validDrink(req.Drink) {
			writeError(w, "invalid drink value", http.StatusBadRequest)
			return
		}
		if !validFloorCondition(req.FloorCondition) {
			writeError(w, "invalid floor_condition value", http.StatusBadRequest)
			return
		}
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
	var totalCounts ImportCounts
	for i, req := range requests {
		locationID, err := resolveLocationID(tx, req.LocationID, req.Location)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		createdEvents, counts, err := createEventFromRequest(tx, req, locationID, isPublished, &callerID)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		totalCounts.Merge(counts)
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

	if totalCounts.AllNew() {
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

	inOrg := (userRole == RoleUser || userRole == RolePublisher) && event.OrganizationID != nil && isOrgMember(callerID, *event.OrganizationID)
	editable := userRole == RoleAdmin || inOrg
	cancelable := editable
	event.Editable = &editable
	event.Cancelable = &cancelable

	var (
		timetable   []TimetableEntry
		locs        []Location
		musicians   []Musician
		instructors []Instructor
		wg          sync.WaitGroup
	)
	wg.Add(4)
	go func() { defer wg.Done(); timetable, _ = fetchTimetable(event.ID) }()
	go func() { defer wg.Done(); locs, _ = fetchEventLocation(event.ID) }()
	go func() { defer wg.Done(); musicians, _ = fetchEventMusicians(event.ID) }()
	go func() { defer wg.Done(); instructors, _ = fetchEventInstructors(event.ID) }()
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
	if len(instructors) > 0 {
		event.Instructors = instructors
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
	if userRole != RoleAdmin && userRole != RoleUser && userRole != RolePublisher {
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
	if !validFood(req.Food) {
		writeError(w, "invalid food value", http.StatusBadRequest)
		return
	}
	if !validDrink(req.Drink) {
		writeError(w, "invalid drink value", http.StatusBadRequest)
		return
	}
	if !validFloorCondition(req.FloorCondition) {
		writeError(w, "invalid floor_condition value", http.StatusBadRequest)
		return
	}

	var existingOrgID sql.NullInt64
	var existingCreatedBy sql.NullInt64
	if err := db.QueryRow("SELECT organization_id, created_by_id FROM events WHERE id = ?", id).Scan(&existingOrgID, &existingCreatedBy); err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	switch userRole {
	case RoleAdmin:
		// unrestricted
	case RolePublisher:
		if !existingOrgID.Valid || !isOrgMember(callerID, int(existingOrgID.Int64)) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		if req.OrganizationID != nil && !isOrgMember(callerID, *req.OrganizationID) {
			writeError(w, "Forbidden: not a member of the target organisation", http.StatusForbidden)
			return
		}
	default:
		if !existingOrgID.Valid || !isOrgMember(callerID, int(existingOrgID.Int64)) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		if req.OrganizationID == nil || !isOrgMember(callerID, *req.OrganizationID) {
			writeError(w, "Forbidden: not a member of the target organisation", http.StatusForbidden)
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

	syncEventTypeTags(&req.EventWriteRequest)

	locationID, err := resolveLocationID(tx, req.LocationID, req.Location)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var locationIDArg any
	if locationID > 0 {
		locationIDArg = locationID
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

	changedByUser := resolveDisplayName(callerID)

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
		req.Title, req.Description, startTime, endTime, locationIDArg,
		req.HasBall, req.HasWorkshop, req.HasFestival, req.IsCancelled, req.IsPublished,
		req.WorkshopDifficulty, urlVal(req.URL), urlVal(req.BookingURL), orgIDArg, pricingArg,
		req.Availability, req.TicketsTotal, req.BookingEnabled, req.Food, req.Drink, req.FloorCondition, attrsJSON(req.Attributes),
		req.ContactName, req.ContactEmail, time.Now().UTC().Unix(), changedByUser, callerIDArg, id,
	); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if _, err := tx.Exec("DELETE FROM event_musicians WHERE event_id = ?", id); err != nil {
		writeError(w, "failed to update musicians", http.StatusInternalServerError)
		return
	}
	if err := batchInsertPairs(tx, "event_musicians", "event_id", "musician_id", id, req.Musicians); err != nil {
		writeError(w, "failed to update musicians", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec("DELETE FROM event_instructors WHERE event_id = ?", id); err != nil {
		writeError(w, "failed to update instructors", http.StatusInternalServerError)
		return
	}
	if err := batchInsertPairs(tx, "event_instructors", "event_id", "instructor_id", id, req.Instructors); err != nil {
		writeError(w, "failed to update instructors", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec("DELETE FROM event_dances WHERE event_id = ?", id); err != nil {
		writeError(w, "failed to update dances", http.StatusInternalServerError)
		return
	}
	if err := batchInsertPairs(tx, "event_dances", "event_id", "dance_id", id, req.Dances); err != nil {
		writeError(w, "failed to update dances", http.StatusInternalServerError)
		return
	}
	if err := syncEventTags(tx, id, req.Tags); err != nil {
		writeError(w, "failed to update tags", http.StatusInternalServerError)
		return
	}

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
	if instructors, err := fetchEventInstructors(id); err == nil {
		event.Instructors = instructors
	}
	if timetable, err := fetchTimetable(id); err == nil {
		event.Timetable = timetable
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(event)
}

// PATCH /api/v1/events/{id} — partial event update (RFC 7396 JSON Merge Patch)
func patchEvent(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "application/merge-patch+json" {
		writeError(w, "PATCH requires Content-Type: application/merge-patch+json", http.StatusUnsupportedMediaType)
		return
	}
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser && userRole != RolePublisher {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var req EventMergePatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Title != nil && *req.Title == "" {
		writeError(w, "title is required", http.StatusBadRequest)
		return
	}
	if req.Tags != nil {
		if err := validateTags(*req.Tags); err != nil {
			writeError(w, "invalid tag: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Food != nil && !validFood(*req.Food) {
		writeError(w, "invalid food value", http.StatusBadRequest)
		return
	}
	if req.Drink != nil && !validDrink(*req.Drink) {
		writeError(w, "invalid drink value", http.StatusBadRequest)
		return
	}
	if req.FloorCondition != nil && !validFloorCondition(*req.FloorCondition) {
		writeError(w, "invalid floor_condition value", http.StatusBadRequest)
		return
	}

	var (
		title, description, url, workshopDifficulty, bookingURL, availability string
		food, drink, floorCondition, contactName, contactEmail, attrsRaw      string
		startUnix, endUnix                                                    int64
		hasBall, hasWorkshop, hasFestival, isCancelled, isPublished           bool
		bookingEnabled                                                        bool
		ticketsTotal                                                          int
		existingOrgID, existingLocationID                                     sql.NullInt64
		existingCreatedBy                                                     sql.NullInt64
		pricingRaw                                                            sql.NullString
	)
	err = db.QueryRow(`SELECT title, description, start_time, end_time, location_id, organization_id,
		has_ball, has_workshop, has_festival, is_cancelled, is_published, COALESCE(url,''), pricing,
		COALESCE(workshop_difficulty,''), COALESCE(booking_url,''), COALESCE(availability,''), tickets_total, booking_enabled,
		COALESCE(food,''), COALESCE(drink,''), COALESCE(floor_condition,''), COALESCE(attributes,'{}'),
		COALESCE(contact_name,''), COALESCE(contact_email,''), created_by_id
		FROM events WHERE id=?`, id).Scan(
		&title, &description, &startUnix, &endUnix, &existingLocationID, &existingOrgID,
		&hasBall, &hasWorkshop, &hasFestival, &isCancelled, &isPublished, &url, &pricingRaw,
		&workshopDifficulty, &bookingURL, &availability, &ticketsTotal, &bookingEnabled,
		&food, &drink, &floorCondition, &attrsRaw,
		&contactName, &contactEmail, &existingCreatedBy,
	)
	if err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	newOrgID := existingOrgID
	if req.OrganizationID != nil {
		newOrgID = sql.NullInt64{Int64: int64(*req.OrganizationID), Valid: true}
	}
	switch userRole {
	case RoleAdmin:
		// unrestricted
	case RolePublisher:
		if !existingOrgID.Valid || !isOrgMember(callerID, int(existingOrgID.Int64)) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		if req.OrganizationID != nil && !isOrgMember(callerID, *req.OrganizationID) {
			writeError(w, "Forbidden: not a member of the target organisation", http.StatusForbidden)
			return
		}
	default:
		if !existingOrgID.Valid || !isOrgMember(callerID, int(existingOrgID.Int64)) {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		if !newOrgID.Valid || !isOrgMember(callerID, int(newOrgID.Int64)) {
			writeError(w, "Forbidden: not a member of the target organisation", http.StatusForbidden)
			return
		}
	}

	if req.Title != nil {
		title = *req.Title
	}
	if req.Description != nil {
		description = *req.Description
	}
	if req.StartTime != nil {
		startUnix, err = parseTimeToUnix(*req.StartTime)
		if err != nil {
			writeError(w, "invalid start_time: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.EndTime != nil {
		if v, err := parseTimeToUnix(*req.EndTime); err == nil {
			endUnix = v
		} else {
			endUnix = startUnix
		}
	}
	if req.HasBall != nil {
		hasBall = *req.HasBall
	}
	if req.HasWorkshop != nil {
		hasWorkshop = *req.HasWorkshop
	}
	if req.HasFestival != nil {
		hasFestival = *req.HasFestival
	}
	if req.WorkshopDifficulty != nil {
		workshopDifficulty = *req.WorkshopDifficulty
	}
	if req.IsCancelled != nil {
		isCancelled = *req.IsCancelled
	}
	if req.IsPublished != nil {
		isPublished = *req.IsPublished
	}
	if req.URL != nil {
		url = *req.URL
	}
	if req.BookingURL != nil {
		bookingURL = *req.BookingURL
	}
	if req.Availability != nil {
		availability = *req.Availability
	}
	if req.TicketsTotal != nil {
		ticketsTotal = *req.TicketsTotal
	}
	if req.BookingEnabled != nil {
		bookingEnabled = *req.BookingEnabled
	}
	if req.Food != nil {
		food = *req.Food
	}
	if req.Drink != nil {
		drink = *req.Drink
	}
	if req.FloorCondition != nil {
		floorCondition = *req.FloorCondition
	}
	if req.ContactName != nil {
		contactName = *req.ContactName
	}
	if req.ContactEmail != nil {
		contactEmail = *req.ContactEmail
	}
	if req.Attributes != nil {
		attrsRaw = attrsJSON(*req.Attributes)
	}

	var locationIDArg any
	if req.LocationID != nil {
		if *req.LocationID > 0 {
			locationIDArg = *req.LocationID
		}
	} else if existingLocationID.Valid {
		locationIDArg = existingLocationID.Int64
	}

	var pricingArg any
	if req.Pricing != nil {
		if b, err := json.Marshal(req.Pricing); err == nil {
			pricingArg = string(b)
		}
	} else if pricingRaw.Valid && pricingRaw.String != "" {
		pricingArg = pricingRaw.String
	}

	var orgIDArg any
	if newOrgID.Valid {
		orgIDArg = newOrgID.Int64
	}

	// syncEventTypeTags derives tags like "bal-folk"/"workshop"/"festival" from
	// the has_ball/has_workshop/has_festival booleans, so re-run it whenever
	// either the tags or any of those booleans are part of this patch.
	var tagsToSync []string
	syncTags := req.Tags != nil || req.HasBall != nil || req.HasWorkshop != nil || req.HasFestival != nil
	if syncTags {
		if req.Tags != nil {
			tagsToSync = *req.Tags
		} else {
			rows, err := db.Query("SELECT tag FROM event_tags WHERE event_id=?", id)
			if err == nil {
				for rows.Next() {
					var t string
					if rows.Scan(&t) == nil {
						tagsToSync = append(tagsToSync, t)
					}
				}
				rows.Close()
			}
		}
		tmp := EventWriteRequest{HasBall: hasBall, HasWorkshop: hasWorkshop, HasFestival: hasFestival, Tags: tagsToSync}
		syncEventTypeTags(&tmp)
		tagsToSync = tmp.Tags
	}

	changedByUser := resolveDisplayName(callerID)
	var callerIDArg any
	if callerID > 0 {
		callerIDArg = callerID
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`UPDATE events SET title=?, description=?, start_time=?, end_time=?, location_id=?,
		 has_ball=?, has_workshop=?, has_festival=?, is_cancelled=?, is_published=?,
		 workshop_difficulty=?, url=?, booking_url=?, organization_id=?, pricing=?,
		 availability=?, tickets_total=?, booking_enabled=?, food=?, drink=?, floor_condition=?, attributes=?,
		 contact_name=?, contact_email=?, changed_at=?, changed_by=?, changed_by_id=? WHERE id=?`,
		title, description, startUnix, endUnix, locationIDArg,
		hasBall, hasWorkshop, hasFestival, isCancelled, isPublished,
		workshopDifficulty, urlVal(url), urlVal(bookingURL), orgIDArg, pricingArg,
		availability, ticketsTotal, bookingEnabled, food, drink, floorCondition, attrsRaw,
		contactName, contactEmail, time.Now().UTC().Unix(), changedByUser, callerIDArg, id,
	); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if req.Musicians != nil {
		if _, err := tx.Exec("DELETE FROM event_musicians WHERE event_id = ?", id); err != nil {
			writeError(w, "failed to update musicians", http.StatusInternalServerError)
			return
		}
		if err := batchInsertPairs(tx, "event_musicians", "event_id", "musician_id", id, *req.Musicians); err != nil {
			writeError(w, "failed to update musicians", http.StatusInternalServerError)
			return
		}
	}
	if req.Instructors != nil {
		if _, err := tx.Exec("DELETE FROM event_instructors WHERE event_id = ?", id); err != nil {
			writeError(w, "failed to update instructors", http.StatusInternalServerError)
			return
		}
		if err := batchInsertPairs(tx, "event_instructors", "event_id", "instructor_id", id, *req.Instructors); err != nil {
			writeError(w, "failed to update instructors", http.StatusInternalServerError)
			return
		}
	}
	if req.Dances != nil {
		if _, err := tx.Exec("DELETE FROM event_dances WHERE event_id = ?", id); err != nil {
			writeError(w, "failed to update dances", http.StatusInternalServerError)
			return
		}
		if err := batchInsertPairs(tx, "event_dances", "event_id", "dance_id", id, *req.Dances); err != nil {
			writeError(w, "failed to update dances", http.StatusInternalServerError)
			return
		}
	}
	if syncTags {
		if err := syncEventTags(tx, id, tagsToSync); err != nil {
			writeError(w, "failed to update tags", http.StatusInternalServerError)
			return
		}
	}

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
	if instructors, err := fetchEventInstructors(id); err == nil {
		event.Instructors = instructors
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
// admin: any event. user/publisher: own orgs.
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
	case RoleUser, RolePublisher:
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
	if req.Food != nil && !validFood(*req.Food) {
		writeError(w, "invalid food value", http.StatusBadRequest)
		return
	}
	if req.Drink != nil && !validDrink(*req.Drink) {
		writeError(w, "invalid drink value", http.StatusBadRequest)
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
		if role != RoleAdmin {
			var existingOrg int
			if err := db.QueryRow("SELECT COALESCE(organization_id,0) FROM events WHERE id=?", id).Scan(&existingOrg); err != nil {
				continue
			}
			if existingOrg != 0 && !isOrgMember(callerID, existingOrg) {
				continue
			}
		}
		if req.OrgID != nil {
			if *req.OrgID == 0 {
				db.Exec("UPDATE events SET organization_id=NULL WHERE id=?", id)
			} else {
				db.Exec("UPDATE events SET organization_id=? WHERE id=?", *req.OrgID, id)
			}
		}
		if req.AddTags != nil {
			clean := make([]string, 0, len(req.AddTags))
			for _, t := range req.AddTags {
				if t = strings.TrimSpace(t); t != "" {
					clean = append(clean, t)
				}
			}
			batchInsertPairs(db, "event_tags", "event_id", "tag", id, clean)
		}
		if req.AddDances != nil {
			batchInsertPairs(db, "event_dances", "event_id", "dance_id", id, req.AddDances)
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

// POST /api/v1/events/{id}/remove-from-series
func removeEventFromSeries(w http.ResponseWriter, r *http.Request) {
	callerID, role := callerFromRequest(r)
	if role != RoleAdmin && role != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	if role != RoleAdmin && !isOrgMemberOfEvent(callerID, eventID) {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	db.Exec("UPDATE events SET series_id=NULL WHERE id=?", eventID)
	w.WriteHeader(http.StatusNoContent)
}

// ── Sub-resource endpoints (#727) ───────────────────────────────────────────
//
// Additive, REST-idiomatic alternatives to embedding location_id/organization_id/
// musicians[]/instructors[]/dances[] in the event write body. In particular,
// PUT/DELETE .../location and .../organization give clients a way to set or
// clear those two nullable references directly — something PATCH's merge-patch
// semantics can't express (see #721's accepted omit-vs-null limitation).

// EventLocationRefRequest is the body for PUT /api/v1/events/{id}/location.
type EventLocationRefRequest struct {
	LocationID int `json:"location_id"`
}

// EventOrganizationRefRequest is the body for PUT /api/v1/events/{id}/organization.
type EventOrganizationRefRequest struct {
	OrganizationID int `json:"organization_id"`
}

// PUT /api/v1/events/{id}/location — set the event's location.
func setEventLocationRef(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	var req EventLocationRefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LocationID <= 0 {
		writeError(w, "location_id is required", http.StatusBadRequest)
		return
	}
	var exists int
	db.QueryRow("SELECT COUNT(*) FROM locations WHERE id=?", req.LocationID).Scan(&exists)
	if exists == 0 {
		writeError(w, "Location not found", http.StatusNotFound)
		return
	}
	if _, err := db.Exec("UPDATE events SET location_id=? WHERE id=?", req.LocationID, eventID); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	syncEventLocationGeohash(eventID)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/events/{id}/location — clear the event's location.
func unsetEventLocationRef(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	db.Exec("UPDATE events SET location_id=NULL WHERE id=?", eventID)
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/events/{id}/organization — set the event's organization.
// Requires the caller to be a member of the target organization (unless admin).
func setEventOrganizationRef(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	var req EventOrganizationRefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OrganizationID <= 0 {
		writeError(w, "organization_id is required", http.StatusBadRequest)
		return
	}
	if userRole != RoleAdmin && !isOrgMember(callerID, req.OrganizationID) {
		writeError(w, "Forbidden: not a member of the target organisation", http.StatusForbidden)
		return
	}
	var exists int
	db.QueryRow("SELECT COUNT(*) FROM organizations WHERE id=?", req.OrganizationID).Scan(&exists)
	if exists == 0 {
		writeError(w, "Organization not found", http.StatusNotFound)
		return
	}
	if _, err := db.Exec("UPDATE events SET organization_id=? WHERE id=?", req.OrganizationID, eventID); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/events/{id}/organization — clear the event's organization
// (event becomes orphaned; see assignEventOrg to reassign).
func unsetEventOrganizationRef(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	db.Exec("UPDATE events SET organization_id=NULL WHERE id=?", eventID)
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/events/{id}/musicians/{musician_id} — add one musician to the event.
func addEventMusician(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	musicianID, err := strconv.Atoi(r.PathValue("musician_id"))
	if err != nil {
		writeError(w, "invalid musician id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	var exists int
	db.QueryRow("SELECT COUNT(*) FROM musicians WHERE id=?", musicianID).Scan(&exists)
	if exists == 0 {
		writeError(w, "Musician not found", http.StatusNotFound)
		return
	}
	db.Exec("INSERT OR IGNORE INTO event_musicians (event_id, musician_id) VALUES (?,?)", eventID, musicianID)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/events/{id}/musicians/{musician_id} — remove one musician from the event.
func removeEventMusician(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	musicianID, err := strconv.Atoi(r.PathValue("musician_id"))
	if err != nil {
		writeError(w, "invalid musician id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	db.Exec("DELETE FROM event_musicians WHERE event_id=? AND musician_id=?", eventID, musicianID)
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/events/{id}/instructors/{instructor_id} — add one instructor to the event.
func addEventInstructor(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	instructorID, err := strconv.Atoi(r.PathValue("instructor_id"))
	if err != nil {
		writeError(w, "invalid instructor id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	var exists int
	db.QueryRow("SELECT COUNT(*) FROM instructors WHERE id=?", instructorID).Scan(&exists)
	if exists == 0 {
		writeError(w, "Instructor not found", http.StatusNotFound)
		return
	}
	db.Exec("INSERT OR IGNORE INTO event_instructors (event_id, instructor_id) VALUES (?,?)", eventID, instructorID)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/events/{id}/instructors/{instructor_id} — remove one instructor from the event.
func removeEventInstructor(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	instructorID, err := strconv.Atoi(r.PathValue("instructor_id"))
	if err != nil {
		writeError(w, "invalid instructor id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	db.Exec("DELETE FROM event_instructors WHERE event_id=? AND instructor_id=?", eventID, instructorID)
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/events/{id}/dances/{dance_id} — add one dance to the event.
func addEventDance(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	danceID, err := strconv.Atoi(r.PathValue("dance_id"))
	if err != nil {
		writeError(w, "invalid dance id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	var exists int
	db.QueryRow("SELECT COUNT(*) FROM dances WHERE id=?", danceID).Scan(&exists)
	if exists == 0 {
		writeError(w, "Dance not found", http.StatusNotFound)
		return
	}
	db.Exec("INSERT OR IGNORE INTO event_dances (event_id, dance_id) VALUES (?,?)", eventID, danceID)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/events/{id}/dances/{dance_id} — remove one dance from the event.
func removeEventDance(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	danceID, err := strconv.Atoi(r.PathValue("dance_id"))
	if err != nil {
		writeError(w, "invalid dance id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	db.Exec("DELETE FROM event_dances WHERE event_id=? AND dance_id=?", eventID, danceID)
	w.WriteHeader(http.StatusNoContent)
}
