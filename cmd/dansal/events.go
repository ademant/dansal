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

	"github.com/ademant/dansal/internal/strutil"
)

// querier is satisfied by both *sql.DB and *sql.Tx, allowing helpers to
// participate in a caller-managed transaction without changing their signature.
type querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

type Event struct {
	ID                     int              `json:"id"`
	UID                    string           `json:"uid,omitempty"`
	Title                  string           `json:"title"`
	Description            string           `json:"description"`
	StartTime              string           `json:"start_time"`
	EndTime                string           `json:"end_time"`
	HasBall                bool             `json:"has_ball"`
	HasWorkshop            bool             `json:"has_workshop"`
	HasFestival            bool             `json:"has_festival"`
	WorkshopDifficulty     string           `json:"workshop_difficulty,omitempty"`
	IsCancelled            bool             `json:"is_cancelled"`
	Tags                   []string         `json:"tags"`
	IsPublished            bool             `json:"is_published"`
	ShortCode              string           `json:"short_code"`
	URL                    string           `json:"url,omitempty"`
	Source                 string           `json:"source,omitempty"`
	CreatedAt              string           `json:"created_at"`
	ImageURL               string           `json:"image_url,omitempty"`
	ImageAIGenerated       bool             `json:"image_ai_generated,omitempty"`
	OrganizationID         *int             `json:"organization_id,omitempty"`
	Editable               *bool            `json:"editable,omitempty"`
	Cancelable             *bool            `json:"cancelable,omitempty"`
	Deletable              *bool            `json:"deletable,omitempty"`
	CreatedByID            *int             `json:"created_by_id,omitempty"`
	Timetable              []TimetableEntry `json:"timetable,omitempty"`
	Pricing                *Pricing         `json:"pricing,omitempty"`
	Locations              []Location       `json:"locations,omitempty"`
	Musicians              []Musician       `json:"musicians,omitempty"`
	Instructors            []Instructor     `json:"instructors,omitempty"`
	LocationID             *int             `json:"location_id,omitempty"`
	Location               *Location        `json:"location,omitempty"`
	Attributes             map[string]bool  `json:"attributes,omitempty"`
	FloorCondition         string           `json:"floor_condition,omitempty"`
	ContactName            string           `json:"contact_name,omitempty"`
	ContactEmail           string           `json:"contact_email,omitempty"`
	BookingURL             string           `json:"booking_url,omitempty"`
	Availability           string           `json:"availability,omitempty"`
	TicketsTotal           int              `json:"tickets_total,omitempty"`
	BookingEnabled         bool             `json:"booking_enabled,omitempty"`
	Food                   string           `json:"food,omitempty"`
	Drink                  string           `json:"drink,omitempty"`
	DanceNames             []string         `json:"dance_names,omitempty"`
	ChangedAt              string           `json:"changed_at,omitempty"`
	ChangedBy              string           `json:"changed_by,omitempty"`
	FetchSourceID          int              `json:"fetch_source_id,omitempty"`
	SeriesID               *int             `json:"series_id,omitempty"`
	SeriesImageURL         string           `json:"series_image_url,omitempty"`
	SeriesImageAIGenerated bool             `json:"series_image_ai_generated,omitempty"`
	NeedsDuplicateReview   bool             `json:"needs_duplicate_review,omitempty"`
	DuplicateOfID          *int             `json:"duplicate_of_id,omitempty"`
	PreviousStartTime      string           `json:"previous_start_time,omitempty"`
	SuggesterEmail         string           `json:"suggester_email,omitempty"`
	SuggesterName          string           `json:"suggester_name,omitempty"`
	PendingEditJSON        string           `json:"pending_edit_json,omitempty"`
	PendingEditSubmittedAt string           `json:"pending_edit_submitted_at,omitempty"`
	EmailVerified          bool             `json:"email_verified"`
	TagsJSON               string           `json:"-"`
	PricingJSON            string           `json:"-"`
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
	ImageAIGenerated   bool                 `json:"image_ai_generated,omitempty"`
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
	validFloorConditionValues = map[string]bool{"": true, "parquet": true, "stone": true, "tiles": true, "grass": true, "sand": true, "pavement": true, "carpet": true}
	validParkingValues        = map[string]bool{"": true, "none": true, "free": true, "paid": true}
)

func validFood(s string) bool           { return validFoodValues[s] }
func validDrink(s string) bool          { return validDrinkValues[s] }
func validFloorCondition(s string) bool { return validFloorConditionValues[s] }
func validParking(s string) bool        { return validParkingValues[s] }

// touchEvent stamps changed_at/changed_by on an event after a sub-resource mutation.
func touchEvent(eventID, callerID int) {
	db.Exec("UPDATE events SET changed_at=?, changed_by=? WHERE id=?",
		time.Now().UTC().Unix(), resolveDisplayName(callerID), eventID)
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

func epochToLocal(epoch int64) string {
	return time.Unix(epoch, 0).In(berlinLoc).Format(time.RFC3339)
}

// parseTimeToUnix converts a time string to a Unix epoch. RFC3339 strings
// carry their own offset; naive layouts have no zone and are treated as local
// (Berlin) time to match how events are displayed. The layouts come from
// strutil.TimeLayouts (shared with the web frontend, #1035).
func parseTimeToUnix(s string) (int64, error) {
	for _, layout := range strutil.TimeLayouts {
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

// SELECT used by all event list / single-event queries.
// Dance names are aggregated once via a derived table JOIN rather than a
// correlated subquery, so GROUP_CONCAT runs O(n) total instead of O(n) per row.
const eventListSelect = `SELECT e.id, e.uid, e.title, e.description, e.start_time, e.end_time, e.has_ball, e.has_workshop, e.has_festival, e.is_cancelled, COALESCE((SELECT GROUP_CONCAT(et.tag, ',') FROM event_tags et WHERE et.event_id = e.id), ''), e.is_published, COALESCE(e.short_code,''), COALESCE(e.url,''), COALESCE(e.source,''), e.created_at, COALESCE(l.location,''), COALESCE(l.short_name,''), COALESCE(NULLIF(l.address,''), lp.address, ''), COALESCE(l.zipcode,''), e.organization_id, COALESCE(json(e.pricing),''), e.location_id, COALESCE(NULLIF(l.town,''), lp.town, ''), COALESCE(NULLIF(l.country,''), lp.country, ''), COALESCE(l.latitude, lp.latitude), COALESCE(l.longitude, lp.longitude), COALESCE(e.workshop_difficulty,''), COALESCE(e.booking_url,''), COALESCE(e.availability,''), COALESCE(e.tickets_total,0), COALESCE(e.booking_enabled,0), COALESCE(dn.dance_names,''), COALESCE(e.changed_at,0), COALESCE(e.changed_by,''), COALESCE(e.fetch_source_id,0), COALESCE(e.food,''), COALESCE(e.drink,''), COALESCE(l.attributes,'{}'), COALESCE(json(e.attributes),'{}'), COALESCE(NULLIF(e.contact_name,''), o.contact_name, ''), COALESCE(NULLIF(e.contact_email,''), o.contact_email, ''), COALESCE(l.parking,''), COALESCE(l.floor_condition,''), COALESCE(e.floor_condition,''), e.created_by_id, l.osm_id, COALESCE(l.osm_type,''), COALESCE(l.geohash,''), e.series_id, e.needs_duplicate_review, e.duplicate_of_id, l.parent_id, e.previous_start_time, COALESCE(e.suggester_email,''), COALESCE(e.suggester_name,''), COALESCE(e.pending_edit_json,''), COALESCE(e.pending_edit_submitted_at,0), COALESCE(e.image_ai_generated,0), e.email_verified, COALESCE((SELECT image_ai_generated FROM event_series WHERE id = e.series_id), 0) FROM events e LEFT JOIN locations l ON e.location_id = l.id LEFT JOIN (SELECT ed.event_id, GROUP_CONCAT(d.name,',') AS dance_names FROM event_dances ed JOIN dances d ON d.id=ed.dance_id GROUP BY ed.event_id) dn ON dn.event_id = e.id LEFT JOIN locations lp ON l.parent_id = lp.id LEFT JOIN organizations o ON e.organization_id = o.id`

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

// rescheduleThresholdSeconds matches the ±3h dedup tolerance used elsewhere
// (see threeHours in insertEvent): a start_time shift smaller than this is
// treated as a minor time correction, not a genuine reschedule (#927).
const rescheduleThresholdSeconds = int64(3 * 60 * 60)

// isReschedule reports whether a start_time change on an existing event
// should be recorded as a reschedule (events.previous_start_time / JSON-LD
// EventRescheduled). Only fires for events that were already published and
// not cancelled before the edit, and only for shifts >= rescheduleThresholdSeconds.
func isReschedule(oldStart, newStart int64, wasPublished, wasCancelled bool) bool {
	if !wasPublished || wasCancelled || oldStart <= 0 || newStart == oldStart {
		return false
	}
	diff := newStart - oldStart
	if diff < 0 {
		diff = -diff
	}
	return diff >= rescheduleThresholdSeconds
}

// scanEventRow decodes one row from the eventListSelect query.
func scanEventRow(s scanner) (Event, error) {
	var event Event
	var loc Location
	var hasBallInt, hasWorkshopInt, hasFestivalInt, isCancelledInt, isPublishedInt, bookingEnabledInt, imageAIGeneratedInt, emailVerifiedInt, seriesImageAIGeneratedInt int
	var locAttrsJSON, evtAttrsJSON string
	var startEpoch, endEpoch, changedAtEpoch int64
	var orgID, locID sql.NullInt64
	var uid sql.NullString
	var danceNamesCSV string
	var locLat, locLng sql.NullFloat64
	var createdByID, seriesID, duplicateOfID, locParentID, previousStartTime sql.NullInt64
	var needsDuplicateReviewInt int
	var pendingEditSubmittedEpoch int64
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
		&needsDuplicateReviewInt, &duplicateOfID, &locParentID, &previousStartTime,
		&event.SuggesterEmail, &event.SuggesterName, &event.PendingEditJSON, &pendingEditSubmittedEpoch,
		&imageAIGeneratedInt, &emailVerifiedInt, &seriesImageAIGeneratedInt); err != nil {
		return Event{}, err
	}
	if previousStartTime.Valid {
		event.PreviousStartTime = epochToLocal(previousStartTime.Int64)
	}
	if pendingEditSubmittedEpoch > 0 {
		event.PendingEditSubmittedAt = epochToLocal(pendingEditSubmittedEpoch)
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
	event.ImageAIGenerated = imageAIGeneratedInt == 1
	event.EmailVerified = emailVerifiedInt == 1
	event.SeriesImageAIGenerated = seriesImageAIGeneratedInt == 1
	if evtAttrsJSON != "" && evtAttrsJSON != "{}" {
		json.Unmarshal([]byte(evtAttrsJSON), &event.Attributes)
	}
	event.ImageURL = eventImageURL(event.ID)
	if event.SeriesID != nil {
		event.SeriesImageURL = seriesImageURL(*event.SeriesID)
	}
	if orgID.Valid {
		v := int(orgID.Int64)
		event.OrganizationID = &v
	}
	if locID.Valid {
		id := int(locID.Int64)
		event.LocationID = &id
		loc.ID = id
		if locParentID.Valid {
			v := int(locParentID.Int64)
			loc.ParentID = &v
		}
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
		// A room (loc.ParentID set) inherits address/coordinates from its
		// building at read time (#687) — same rule as getLocation().
		if loc.ParentID != nil {
			resolvedLocation(&loc)
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
		*query += ` AND e.title LIKE ? ESCAPE '\'`
		*args = append(*args, "%"+escapeLike(title)+"%")
	}
	if desc := q.Get("description"); desc != "" {
		*query += ` AND e.description LIKE ? ESCAPE '\'`
		*args = append(*args, "%"+escapeLike(desc)+"%")
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
		*query += ` AND l.location LIKE ? ESCAPE '\'`
		*args = append(*args, "%"+escapeLike(loc)+"%")
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
		*query += " AND (EXISTS (SELECT 1 FROM event_musicians em WHERE em.event_id = e.id AND em.musician_id = ?) OR EXISTS (SELECT 1 FROM timetable_entries t WHERE t.event_id = e.id AND t.musician_id = ?))"
		*args = append(*args, v, v)
	}
	if v := q.Get("instructor_id"); v != "" {
		*query += " AND (EXISTS (SELECT 1 FROM event_instructors ei WHERE ei.event_id = e.id AND ei.instructor_id = ?) OR EXISTS (SELECT 1 FROM timetable_entries t WHERE t.event_id = e.id AND t.instructor_id = ?))"
		*args = append(*args, v, v)
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
		parts := strings.Split(v, ",")
		ids := make([]int, 0, len(parts))
		for _, p := range parts {
			if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
				ids = append(ids, n)
			}
		}
		if len(ids) > 0 {
			placeholders := strings.Repeat("?,", len(ids))
			placeholders = placeholders[:len(placeholders)-1]
			*query += " AND e.location_id IN (" + placeholders + ")"
			for _, n := range ids {
				*args = append(*args, n)
			}
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
	// ?town=Berlin — case-insensitive match on the location's town field.
	// Inherits parent location town (lp.town) when the primary location is a room.
	if town := q.Get("town"); town != "" {
		*query += " AND LOWER(COALESCE(NULLIF(l.town,''), lp.town, '')) = LOWER(?)"
		*args = append(*args, town)
	}
	return nil
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
	Failed    int
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
	c.Failed += other.Failed
}

// AllNew reports whether every event in the batch was newly inserted.
func (c ImportCounts) AllNew() bool {
	return c.Updated == 0 && c.Unchanged == 0
}

// EventInput is insertEvent's upsert payload. Replaces insertEvent's former
// 28 positional parameters — two call sites passed ~30 positional args each,
// which was easy to get subtly wrong (e.g. transposing two strings) with no
// compiler help (#1008).
type EventInput struct {
	Title, Description                string
	StartTime, EndTime                int64
	LocationID                        int64
	HasBall, HasWorkshop, HasFestival bool
	IsCancelled                       bool
	WorkshopDifficulty                string
	BookingURL                        string
	IsPublished                       bool
	OrganizationID                    *int
	UID, URL, Source                  string
	SourceLastModified                int64
	Pricing                           *Pricing
	FetchSourceID                     int
	Food, Drink, FloorCondition       string
	Attributes                        map[string]bool
	ContactName, ContactEmail         string
	CreatedByID                       *int
	ImageAIGenerated                  bool
}

// insertEvent upserts an event. Returns (id, shortCode, outcome, error) where
// outcome is one of outcomeNew/outcomeUpdated/outcomeUnchanged.
// Deduplication runs findExistingEvent's shared 5-tier hierarchy (#1005),
// also used by previewDuplicateStatus so the two paths can't drift.
func insertEvent(q querier, in EventInput) (int, string, string, error) {
	title, description := in.Title, in.Description
	startTime, endTime := in.StartTime, in.EndTime
	locationID := in.LocationID
	hasBall, hasWorkshop, hasFestival := in.HasBall, in.HasWorkshop, in.HasFestival
	isCancelled := in.IsCancelled
	workshopDifficulty, bookingURL := in.WorkshopDifficulty, in.BookingURL
	isPublished := in.IsPublished
	organizationID := in.OrganizationID
	uid, url, source := in.UID, in.URL, in.Source
	sourceLastModified := in.SourceLastModified
	pricing := in.Pricing
	fetchSourceID := in.FetchSourceID
	food, drink, floorCondition := in.Food, in.Drink, in.FloorCondition
	attributes := in.Attributes
	contactName, contactEmail := in.ContactName, in.ContactEmail
	createdByID := in.CreatedByID
	imageAIGenerated := in.ImageAIGenerated

	var uidArg any
	if uid != "" {
		uidArg = uid
	}

	existing, tier, err := findExistingEvent(q, title, url, &startTime, locationID, uid, fetchSourceID)
	if err != nil {
		return 0, "", "", err
	}
	existingID := existing.ID
	existingShortCode := existing.ShortCode
	existingSourceLastModified := existing.SourceLastModified
	existingChangedAt := existing.ChangedAt
	existingStartTime := existing.StartTime
	existingIsPublished := existing.IsPublished
	existingIsCancelled := existing.IsCancelled

	// Tier 5 is a low-confidence review hint, not a match: proceed as if
	// nothing was found, but remember the candidate to flag after insert.
	var duplicateReviewCandidateID int
	lookupErr := sql.ErrNoRows
	if tier == TierFuzzyReview {
		duplicateReviewCandidateID = existing.ID
	} else if tier != TierNone {
		lookupErr = nil
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

		// previous_start_time (#927): only set when this update is a genuine
		// reschedule of an already-published, non-cancelled event; nil otherwise
		// so the COALESCE below leaves any earlier recorded value untouched.
		var previousStartTimeArg any
		if isReschedule(existingStartTime, startTime, existingIsPublished, existingIsCancelled) {
			previousStartTimeArg = existingStartTime
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
				pricing=CASE WHEN ? IS NOT NULL THEN jsonb(?) ELSE pricing END,
				fetch_source_id=COALESCE(?,fetch_source_id),
				organization_id=COALESCE(organization_id,?),
				previous_start_time=COALESCE(?,previous_start_time),
				email_verified=1
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
				previousStartTimeArg,
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
				"UPDATE events SET uid=COALESCE(uid,?), description=?, start_time=?, end_time=?, location_id=COALESCE(?,location_id), has_ball=?, has_workshop=?, has_festival=?, is_cancelled=?, workshop_difficulty=?, is_published=?, url=?, source_last_modified=?, pricing=jsonb(?), changed_at=?, changed_by=?, fetch_source_id=COALESCE(?,fetch_source_id), organization_id=COALESCE(organization_id,?), previous_start_time=COALESCE(?,previous_start_time), email_verified=1 WHERE id=?",
				uidArg, description, startTime, endTime, locIDArg, hasBall, hasWorkshop, hasFestival, isCancelled, workshopDifficulty, isPublished, urlVal(url), slmArg, pricingArg,
				time.Now().UTC().Unix(), "fetch", fsArg, orgIDArg, previousStartTimeArg, existingID,
			)
		} else {
			_, err = q.Exec(
				"UPDATE events SET description=?, start_time=?, end_time=?, location_id=COALESCE(?,location_id), has_ball=?, has_workshop=?, has_festival=?, is_cancelled=?, workshop_difficulty=?, is_published=?, url=?, source_last_modified=?, pricing=jsonb(?), organization_id=COALESCE(organization_id,?), previous_start_time=COALESCE(?,previous_start_time), email_verified=1 WHERE id=?",
				description, startTime, endTime, locIDArg, hasBall, hasWorkshop, hasFestival, isCancelled, workshopDifficulty, isPublished, urlVal(url), slmArg, pricingArg, orgIDArg, previousStartTimeArg, existingID,
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
	var shortCode string
	insChangedAt := time.Now().UTC().Unix()
	insChangedBy := "admin"
	var insFetchSourceID any
	if fetchSourceID > 0 {
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
			"INSERT INTO events (uid, title, description, start_time, end_time, location_id, has_ball, has_workshop, has_festival, is_cancelled, workshop_difficulty, is_published, organization_id, short_code, url, source, source_last_modified, pricing, booking_url, changed_at, changed_by, fetch_source_id, food, drink, floor_condition, attributes, contact_name, contact_email, created_by_id, image_ai_generated, email_verified) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, jsonb(?), ?, ?, ?, ?, ?, ?, ?, jsonb(?), ?, ?, ?, ?, 1)",
			uidArg, title, description, startTime, endTime, locIDArg, hasBall, hasWorkshop, hasFestival, isCancelled, workshopDifficulty, isPublished, orgIDArg, shortCode, urlVal(url), sourceArg, slmArg, pricingArg, urlVal(bookingURL), insChangedAt, insChangedBy, insFetchSourceID, food, drink, floorCondition, attrsJSON(attributes), contactName, contactEmail, createdByArg, imageAIGenerated,
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
	if locationID != 0 {
		q.Exec("INSERT OR IGNORE INTO event_locations (event_id, location_id) VALUES (?,?)", int(id), locationID)
	}
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

		id, shortCode, outcome, err := insertEvent(q, EventInput{
			Title:              req.Title,
			Description:        entry.description,
			StartTime:          startTime,
			EndTime:            endTime,
			LocationID:         locationID,
			HasBall:            req.HasBall,
			HasWorkshop:        req.HasWorkshop,
			HasFestival:        req.HasFestival,
			IsCancelled:        req.IsCancelled,
			WorkshopDifficulty: req.WorkshopDifficulty,
			BookingURL:         req.BookingURL,
			IsPublished:        isPublished,
			OrganizationID:     req.OrganizationID,
			UID:                req.UID,
			URL:                req.URL,
			Source:             req.Source,
			SourceLastModified: req.SourceLastModified,
			Pricing:            req.Pricing,
			FetchSourceID:      req.FetchSourceID,
			Food:               req.Food,
			Drink:              req.Drink,
			FloorCondition:     req.FloorCondition,
			Attributes:         req.Attributes,
			ContactName:        req.ContactName,
			ContactEmail:       req.ContactEmail,
			CreatedByID:        createdByID,
			ImageAIGenerated:   req.ImageAIGenerated,
		})
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

// annotateEditable sets Editable, Cancelable and Deletable on each event based
// on the caller's role.
// editable/cancelable: admin=any; user/publisher=own orgs.
// deletable: admin=any; user/publisher=own orgs, and only within
// eventDeletionDeadline (see deleteEvent).
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
		deletable := isAdmin || (inOrg && withinDeletionDeadline(events[i]))
		events[i].Editable = &editable
		events[i].Cancelable = &cancelable
		events[i].Deletable = &deletable
	}
}

// withinDeletionDeadline reports whether event's CreatedAt/StartTime are
// still inside the non-admin deletion window (see eventDeletionDeadline).
func withinDeletionDeadline(event Event) bool {
	createdUnix, err := parseTimeToUnix(event.CreatedAt)
	if err != nil {
		return false
	}
	startUnix, err := parseTimeToUnix(event.StartTime)
	if err != nil {
		return false
	}
	deadline := eventDeletionDeadline(time.Unix(createdUnix, 0), time.Unix(startUnix, 0))
	return !time.Now().After(deadline)
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

// attachMusiciansToEvents batch-fetches musicians for all events in the slice
// with a single SQL query and attaches them in place. Used by getEvents when
// ?with_musicians=true is set — O(1) queries for an N-event list.
func attachMusiciansToEvents(events []Event) {
	if len(events) == 0 {
		return
	}
	ids := make([]any, len(events))
	idx := make(map[int]int, len(events)) // event ID → slice index
	for i, e := range events {
		ids[i] = e.ID
		idx[e.ID] = i
	}
	placeholders := make([]byte, 0, len(ids)*2)
	for i := range ids {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
	}
	rows, err := db.Query(
		`SELECT em.event_id, m.id, m.bandname, COALESCE(m.short_name,'')
		 FROM musicians m JOIN event_musicians em ON m.id = em.musician_id
		 WHERE em.event_id IN (`+string(placeholders)+`) ORDER BY em.event_id, m.bandname`,
		ids...,
	)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var eventID int
		var m Musician
		if err := rows.Scan(&eventID, &m.ID, &m.Bandname, &m.ShortName); err != nil {
			continue
		}
		if i, ok := idx[eventID]; ok {
			events[i].Musicians = append(events[i].Musicians, m)
		}
	}
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

// fetchEventLocations returns all locations assigned to an event via event_locations,
// including the primary. Used by getEvent to populate Event.Locations.
func fetchEventLocations(eventID int) ([]Location, error) {
	const sel = `SELECT l.id, l.location, COALESCE(l.short_name,''), COALESCE(l.address,''),
		COALESCE(l.zipcode,''), COALESCE(l.town,''), COALESCE(l.country,''),
		COALESCE(l.country_code,''), COALESCE(l.region,''), l.latitude,
		l.longitude, COALESCE(l.internetsite,''), l.osm_id, COALESCE(l.osm_type,''),
		COALESCE(l.geohash,''), COALESCE(l.wikidata_id,''), COALESCE(l.mb_place_id,''),
		l.created_at, COALESCE(GROUP_CONCAT(lo.organization_id),'')
		FROM locations l
		LEFT JOIN location_organizations lo ON l.id=lo.location_id
		JOIN event_locations el ON l.id=el.location_id
		WHERE el.event_id=? GROUP BY l.id`
	rows, err := db.Query(sel, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var locs []Location
	for rows.Next() {
		var loc Location
		var orgIDsStr string
		var lat, lng sql.NullFloat64
		if err := rows.Scan(
			&loc.ID, &loc.Location, &loc.ShortName, &loc.Address,
			&loc.Zipcode, &loc.Town, &loc.Country, &loc.CountryCode, &loc.Region, &lat, &lng,
			&loc.Internetsite, &loc.OsmID, &loc.OsmType,
			&loc.Geohash, &loc.WikidataID, &loc.MBPlaceID,
			&loc.CreatedAt, &orgIDsStr,
		); err != nil {
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
		locs = append(locs, loc)
	}
	return locs, rows.Err()
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
				eventListSelect+" WHERE e.short_code = ? AND e.is_published = 1 AND e.email_verified = 1", shortCode,
			))
			if err == sql.ErrNoRows {
				writeError(w, "Event not found", http.StatusNotFound)
				return
			} else if err != nil {
				writeInternalError(w, err)
				return
			}
			json.NewEncoder(w).Encode(event)
			return
		}
	}

	query := eventListSelect + " WHERE e.email_verified = 1"
	args := []any{}

	// Admin-only escape hatch (#982): email-unverified events (e.g. events
	// imported between two server restarts, before the startup backfill runs)
	// are otherwise invisible to every admin view since the base guard above
	// requires email_verified=1.
	if isAuthorizedAdmin {
		if v := r.URL.Query().Get("email_verified"); v == "false" {
			query = eventListSelect + " WHERE e.email_verified = 0"
		}
	}

	if !isAuthorizedAdmin {
		query += " AND e.is_published = 1"
		// Cache fingerprint for public clients: count + latest creation time.
		if !strings.Contains(accept, "text/calendar") {
			if checkPublicCacheHeaders(w, r, "SELECT COUNT(*), MAX(COALESCE(datetime(changed_at,'unixepoch'), created_at)) FROM events WHERE is_published = 1") {
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

	applyListPagination(r, "e.start_time ASC", &query, &args)

	rows, err := db.Query(query, args...)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		event, err := scanEventRow(rows)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		events = append(events, event)
	}

	annotateEditable(events, userRole, callerID)

	// Batch-attach musicians when the caller opts in (?with_musicians=true).
	// A single query fetches all musicians for all events in the result set —
	// O(1) extra query regardless of list length.
	if r.URL.Query().Get("with_musicians") == "true" && len(events) > 0 {
		attachMusiciansToEvents(events)
	}

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
		summary := truncateUTF8(ev.Description, 300)
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

	// syncEventTypeTags is not called here — createEventFromRequest already
	// calls it per-request (#1015), and nothing between here and that call
	// depends on the synced tags/booleans.
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
		writeInternalError(w, err)
		return
	}
	defer tx.Rollback()

	var allCreatedEvents []Event
	var totalCounts ImportCounts
	for i, req := range requests {
		locationID, err := resolveLocationID(tx, req.LocationID, req.Location)
		if err != nil {
			writeInternalError(w, err)
			return
		}

		createdEvents, counts, err := createEventFromRequest(tx, req, locationID, isPublished, &callerID)
		if err != nil {
			writeInternalError(w, err)
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
		writeInternalError(w, err)
		return
	}

	if totalCounts.AllNew() {
		if len(allCreatedEvents) == 1 {
			w.Header().Set("Location", fmt.Sprintf("/api/v1/events/%d", allCreatedEvents[0].ID))
		}
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
		query = eventListSelect + " WHERE e.id = ? AND e.is_published = 1 AND e.email_verified = 1"
	}
	event, err := scanEventRow(db.QueryRow(query, id))
	if err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}

	// Only the single-event fetch needs the building's rooms/site-plan (#885,
	// for showing which room each timetable slot is in) — list queries don't
	// pay for this extra per-row query.
	attachSitePlanData(event.Location)

	inOrg := (userRole == RoleUser || userRole == RolePublisher) && event.OrganizationID != nil && isOrgMember(callerID, *event.OrganizationID)
	editable := userRole == RoleAdmin || inOrg
	cancelable := editable
	deletable := userRole == RoleAdmin || (inOrg && withinDeletionDeadline(event))
	event.Editable = &editable
	event.Cancelable = &cancelable
	event.Deletable = &deletable

	var (
		timetable   []TimetableEntry
		locs        []Location
		musicians   []Musician
		instructors []Instructor
		wg          sync.WaitGroup
	)
	wg.Add(4)
	go func() { defer wg.Done(); timetable, _ = fetchTimetable(event.ID) }()
	go func() { defer wg.Done(); locs, _ = fetchEventLocations(event.ID) }()
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

	var changedAtEpoch int64
	db.QueryRow("SELECT COALESCE(changed_at,0) FROM events WHERE id=?", event.ID).Scan(&changedAtEpoch)
	w.Header().Set("ETag", weakEtag(changedAtEpoch))

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

// requireExistingOrgMember writes a 403 and returns false unless callerID is
// a member of the org referenced by existingOrgID (existingOrgID being unset
// is always denied — an org-less event isn't "your org's event"). This is
// the single check duplicated (with only variable names differing) across
// event/location/image/suggestion handlers; requireEventOrg builds on it for
// handlers that also need to validate a target org (#1007).
func requireExistingOrgMember(w http.ResponseWriter, callerID int, existingOrgID sql.NullInt64) bool {
	if !existingOrgID.Valid || !isOrgMember(callerID, int(existingOrgID.Int64)) {
		writeError(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// requireEventOrg enforces the standard event-mutation access rule that
// recurred, copy-pasted, across updateEvent/patchEvent and others (#1007):
// admin is unrestricted; publisher/user must belong to the event's current
// org (requireExistingOrgMember), and must also belong to targetOrgID — the
// org the caller wants the event to end up in after this request.
// requireTarget controls whether targetOrgID == nil itself is a denial
// (PUT: yes, an org must always be specified by non-admins; PATCH: no, a
// caller resolves targetOrgID to the existing org before calling when the
// request omits organization_id, so nil there only means "event has no
// org", already caught by requireExistingOrgMember above).
func requireEventOrg(w http.ResponseWriter, role string, callerID int, existingOrgID sql.NullInt64, targetOrgID *int, requireTarget bool) bool {
	if role == RoleAdmin {
		return true
	}
	if !requireExistingOrgMember(w, callerID, existingOrgID) {
		return false
	}
	if targetOrgID == nil {
		if !requireTarget {
			return true
		}
		writeError(w, "Forbidden: not a member of the target organisation", http.StatusForbidden)
		return false
	}
	if !isOrgMember(callerID, *targetOrgID) {
		writeError(w, "Forbidden: not a member of the target organisation", http.StatusForbidden)
		return false
	}
	return true
}

// PUT /api/v1/events/{id} — full event update
func updateEvent(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser && userRole != RolePublisher {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var req EventWriteRequest
	if !decodeJSONBody(w, r, &req) {
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
	var existingStartTime, existingChangedAt int64
	var existingIsPublished, existingIsCancelled bool
	if err := db.QueryRow("SELECT organization_id, created_by_id, start_time, is_published, is_cancelled, COALESCE(changed_at,0) FROM events WHERE id = ?", id).Scan(
		&existingOrgID, &existingCreatedBy, &existingStartTime, &existingIsPublished, &existingIsCancelled, &existingChangedAt,
	); err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}
	if !requireEventOrg(w, userRole, callerID, existingOrgID, req.OrganizationID, userRole != RolePublisher) {
		return
	}
	if checkIfMatchConflict(w, r, existingChangedAt) {
		return
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
		writeInternalError(w, err)
		return
	}
	defer tx.Rollback()

	syncEventTypeTags(&req)

	locationID, err := resolveLocationID(tx, req.LocationID, req.Location)
	if err != nil {
		writeInternalError(w, err)
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
	var previousStartTimeArg any
	if isReschedule(existingStartTime, startTime, existingIsPublished, existingIsCancelled) {
		previousStartTimeArg = existingStartTime
	}

	if _, err := tx.Exec(
		`UPDATE events SET title=?, description=?, start_time=?, end_time=?, location_id=?,
		 has_ball=?, has_workshop=?, has_festival=?, is_cancelled=?, is_published=?,
		 workshop_difficulty=?, url=?, booking_url=?, organization_id=?, pricing=jsonb(?),
		 availability=?, tickets_total=?, booking_enabled=?, food=?, drink=?, floor_condition=?, attributes=jsonb(?),
		 contact_name=?, contact_email=?, image_ai_generated=?, changed_at=?, changed_by=?, changed_by_id=?,
		 previous_start_time=COALESCE(?,previous_start_time) WHERE id=?`,
		req.Title, req.Description, startTime, endTime, locationIDArg,
		req.HasBall, req.HasWorkshop, req.HasFestival, req.IsCancelled, req.IsPublished,
		req.WorkshopDifficulty, urlVal(req.URL), urlVal(req.BookingURL), orgIDArg, pricingArg,
		req.Availability, req.TicketsTotal, req.BookingEnabled, req.Food, req.Drink, req.FloorCondition, attrsJSON(req.Attributes),
		req.ContactName, req.ContactEmail, req.ImageAIGenerated, time.Now().UTC().Unix(), changedByUser, callerIDArg,
		previousStartTimeArg, id,
	); err != nil {
		writeInternalError(w, err)
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
		writeInternalError(w, err)
		return
	}
	syncEventLocationGeohash(id)

	event, err := fetchEventByID(db, id)
	if err != nil {
		writeInternalError(w, err)
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
	id, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var req EventMergePatchRequest
	if !decodeJSONBody(w, r, &req) {
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
		existingChangedAt                                                     int64
	)
	err = db.QueryRow(`SELECT title, description, start_time, end_time, location_id, organization_id,
		has_ball, has_workshop, has_festival, is_cancelled, is_published, COALESCE(url,''), json(pricing),
		COALESCE(workshop_difficulty,''), COALESCE(booking_url,''), COALESCE(availability,''), tickets_total, booking_enabled,
		COALESCE(food,''), COALESCE(drink,''), COALESCE(floor_condition,''), COALESCE(json(attributes),'{}'),
		COALESCE(contact_name,''), COALESCE(contact_email,''), created_by_id, COALESCE(changed_at,0)
		FROM events WHERE id=?`, id).Scan(
		&title, &description, &startUnix, &endUnix, &existingLocationID, &existingOrgID,
		&hasBall, &hasWorkshop, &hasFestival, &isCancelled, &isPublished, &url, &pricingRaw,
		&workshopDifficulty, &bookingURL, &availability, &ticketsTotal, &bookingEnabled,
		&food, &drink, &floorCondition, &attrsRaw,
		&contactName, &contactEmail, &existingCreatedBy, &existingChangedAt,
	)
	if err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}
	// Captured before startUnix/isPublished/isCancelled are overwritten below by
	// the patch fields, so isReschedule can compare old vs. new start_time (#927).
	oldStartUnix, wasPublished, wasCancelled := startUnix, isPublished, isCancelled

	newOrgID := existingOrgID
	if req.OrganizationID != nil {
		newOrgID = sql.NullInt64{Int64: int64(*req.OrganizationID), Valid: true}
	}
	var newOrgIDArg *int
	if newOrgID.Valid {
		v := int(newOrgID.Int64)
		newOrgIDArg = &v
	}
	// requireTarget=true unconditionally: newOrgIDArg already resolves to the
	// existing org when the request omits organization_id, so it's only nil
	// when the event has no org at all — already caught by
	// requireExistingOrgMember before the target check runs.
	if !requireEventOrg(w, userRole, callerID, existingOrgID, newOrgIDArg, true) {
		return
	}
	if checkIfMatchConflict(w, r, existingChangedAt) {
		return
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
		writeInternalError(w, err)
		return
	}
	defer tx.Rollback()

	var previousStartTimeArg any
	if isReschedule(oldStartUnix, startUnix, wasPublished, wasCancelled) {
		previousStartTimeArg = oldStartUnix
	}

	if _, err := tx.Exec(
		`UPDATE events SET title=?, description=?, start_time=?, end_time=?, location_id=?,
		 has_ball=?, has_workshop=?, has_festival=?, is_cancelled=?, is_published=?,
		 workshop_difficulty=?, url=?, booking_url=?, organization_id=?, pricing=jsonb(?),
		 availability=?, tickets_total=?, booking_enabled=?, food=?, drink=?, floor_condition=?, attributes=jsonb(?),
		 contact_name=?, contact_email=?, changed_at=?, changed_by=?, changed_by_id=?,
		 previous_start_time=COALESCE(?,previous_start_time) WHERE id=?`,
		title, description, startUnix, endUnix, locationIDArg,
		hasBall, hasWorkshop, hasFestival, isCancelled, isPublished,
		workshopDifficulty, urlVal(url), urlVal(bookingURL), orgIDArg, pricingArg,
		availability, ticketsTotal, bookingEnabled, food, drink, floorCondition, attrsRaw,
		contactName, contactEmail, time.Now().UTC().Unix(), changedByUser, callerIDArg,
		previousStartTimeArg, id,
	); err != nil {
		writeInternalError(w, err)
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
		writeInternalError(w, err)
		return
	}
	syncEventLocationGeohash(id)

	event, err := fetchEventByID(db, id)
	if err != nil {
		writeInternalError(w, err)
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
		// suggester_email has served its purpose (rate-limiting + the
		// verification/manage-link email) once the event is published, and the
		// privacy notice promises minimal retention — clear it here (#944).
		result, err := db.Exec("UPDATE events SET is_published=1, email_verified=1, suggester_email='' WHERE id=?", id)
		if err != nil {
			writeInternalError(w, err)
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
			writeInternalError(w, err)
			return
		}
		if !requireExistingOrgMember(w, callerID, orgID) {
			return
		}
		db.Exec("UPDATE events SET is_published=1, email_verified=1, suggester_email='' WHERE id=?", id)
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
		writeInternalError(w, err)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Event not found or already assigned to an organisation", http.StatusNotFound)
		return
	}
	eventID, _ := strconv.Atoi(id)
	touchEvent(eventID, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/events/{id}
// admin: unrestricted. user/publisher: own org, and only within a shrinking
// window after creation — see eventDeletionDeadline.
func deleteEvent(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	id, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	switch userRole {
	case RoleAdmin:
		// unrestricted
	case RoleUser, RolePublisher:
		var orgID sql.NullInt64
		var startTime int64
		var createdAt time.Time
		if err := db.QueryRow("SELECT organization_id, start_time, created_at FROM events WHERE id=?", id).Scan(&orgID, &startTime, &createdAt); err == sql.ErrNoRows {
			writeError(w, "Event not found", http.StatusNotFound)
			return
		} else if err != nil {
			writeInternalError(w, err)
			return
		}
		if !requireExistingOrgMember(w, callerID, orgID) {
			return
		}
		deadline := eventDeletionDeadline(createdAt, time.Unix(startTime, 0))
		if time.Now().After(deadline) {
			writeError(w, "Forbidden: this event can no longer be deleted, only cancelled", http.StatusForbidden)
			return
		}
	default:
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Clear duplicate-review flags on partners of this event, but only when
	// the partner has no OTHER flagged event still pointing to it.  If three
	// events are mutually flagged and one is deleted, the remaining two may
	// still be duplicates of each other and must keep their flags.
	// The FK ON DELETE SET NULL handles duplicate_of_id itself;
	// needs_duplicate_review must be cleared explicitly.
	db.Exec(`UPDATE events
	          SET needs_duplicate_review=0, duplicate_of_id=NULL
	          WHERE duplicate_of_id=?
	            AND NOT EXISTS (
	              SELECT 1 FROM events z
	              WHERE z.duplicate_of_id = events.id
	                AND z.id != ?
	                AND z.needs_duplicate_review = 1
	            )`, id, id)

	result, err := db.Exec("DELETE FROM events WHERE id = ?", id)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// eventDeletionDeadline computes the last moment a non-admin org member may
// hard-delete an event. Normally that's 1 month after creation, but never
// later than 1 month before the event starts (close-in events should be
// cancelled, not deleted). When those two limits collide — the event starts
// soon after creation — a 1 week floor guarantees a minimum deletion window.
// The result is never later than the event's own start time.
func eventDeletionDeadline(createdAt, startTime time.Time) time.Time {
	deadline := createdAt.AddDate(0, 1, 0)
	if beforeStart := startTime.AddDate(0, -1, 0); beforeStart.Before(deadline) {
		deadline = beforeStart
	}
	if floor := createdAt.AddDate(0, 0, 7); deadline.Before(floor) {
		deadline = floor
	}
	if deadline.After(startTime) {
		deadline = startTime
	}
	return deadline
}

// POST /api/v1/events/{id}/cancel — set is_cancelled=1.
// admin: any event. user/publisher: own orgs.
func cancelEvent(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	id, err := intPathValue(r, "id")
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
			writeInternalError(w, err)
			return
		}
		if !requireExistingOrgMember(w, callerID, orgID) {
			return
		}
	default:
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}

	result, err := db.Exec("UPDATE events SET is_cancelled=1 WHERE id=?", id)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	}
	touchEvent(id, callerID)
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

	srcID, err := intPathValue(r, "id")
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
		writeInternalError(w, err)
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
		writeInternalError(w, err)
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
	cloneID, shortCode, _, err := insertEvent(tx, EventInput{
		Title:              cloneReq.Title,
		Description:        cloneReq.Description,
		StartTime:          0, // cleared
		EndTime:            0, // cleared
		LocationID:         locationID,
		HasBall:            cloneReq.HasBall,
		HasWorkshop:        cloneReq.HasWorkshop,
		HasFestival:        cloneReq.HasFestival,
		IsCancelled:        false,
		WorkshopDifficulty: cloneReq.WorkshopDifficulty,
		BookingURL:         cloneReq.BookingURL,
		IsPublished:        false,
		OrganizationID:     targetOrgID,
		UID:                "", // cleared
		URL:                cloneReq.URL,
		Source:             "", // cleared
		SourceLastModified: 0,  // cleared
		Pricing:            cloneReq.Pricing,
		FetchSourceID:      0,
		Food:               cloneReq.Food,
		Drink:              cloneReq.Drink,
		FloorCondition:     cloneReq.FloorCondition,
		Attributes:         cloneReq.Attributes,
		ContactName:        cloneReq.ContactName,
		ContactEmail:       cloneReq.ContactEmail,
		CreatedByID:        &callerID,
		ImageAIGenerated:   false,
	})
	if err != nil {
		writeInternalError(w, err)
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
		writeInternalError(w, err)
		return
	}

	event, err := fetchEventByID(db, cloneID)
	if err != nil {
		writeInternalError(w, err)
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

	cntQ := "SELECT COUNT(*), MAX(COALESCE(datetime(e.changed_at,'unixepoch'), e.created_at)) FROM events e LEFT JOIN locations l ON e.location_id = l.id WHERE e.is_published = 1 AND e.start_time >= ?"
	cntArgs := []any{now}
	if tag != "" {
		cntQ += " AND EXISTS (SELECT 1 FROM event_tags et WHERE et.event_id = e.id AND et.tag = ?)"
		cntArgs = append(cntArgs, tag)
	}
	if loc != "" {
		cntQ += ` AND l.location LIKE ? ESCAPE '\'`
		cntArgs = append(cntArgs, "%"+escapeLike(loc)+"%")
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
		query += ` AND l.location LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(loc)+"%")
	}

	query += " ORDER BY e.start_time ASC"

	rows, err := db.Query(query, args...)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()

	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	for rows.Next() {
		event, err := scanEventRow(rows)
		if err != nil {
			writeInternalError(w, err)
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
	id, err := intPathValue(r, "id")
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
		"SELECT COUNT(*), MAX(COALESCE(datetime(changed_at,'unixepoch'), created_at)) FROM events WHERE is_published = 1 AND start_time >= ? AND EXISTS (SELECT 1 FROM event_tags et WHERE et.event_id = events.id AND et.tag = ?)",
		now, tag) {
		return
	}
	query := eventListSelect + " WHERE e.is_published = 1 AND e.start_time >= ? AND EXISTS (SELECT 1 FROM event_tags et WHERE et.event_id = e.id AND et.tag = ?) ORDER BY e.start_time ASC"
	rows, err := db.Query(query, now, tag)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()

	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	for rows.Next() {
		event, err := scanEventRow(rows)
		if err != nil {
			writeInternalError(w, err)
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
	escapedTown := "%" + escapeLike(town) + "%"
	if checkPublicCacheHeaders(w, r,
		`SELECT COUNT(*), MAX(COALESCE(datetime(e.changed_at,'unixepoch'), e.created_at)) FROM events e LEFT JOIN locations l ON e.location_id = l.id WHERE e.is_published = 1 AND e.start_time >= ? AND l.town LIKE ? ESCAPE '\'`,
		now, escapedTown) {
		return
	}
	query := eventListSelect + ` WHERE e.is_published = 1 AND e.start_time >= ? AND l.town LIKE ? ESCAPE '\' ORDER BY e.start_time ASC`
	rows, err := db.Query(query, now, escapedTown)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()

	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	for rows.Next() {
		event, err := scanEventRow(rows)
		if err != nil {
			writeInternalError(w, err)
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

// checkPublicCacheHeaders runs cntQuery (must SELECT COUNT(*), MAX(last_modified))
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
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	EventCount int    `json:"event_count,omitempty"`
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

// filterKnownTags drops any tag not present in the tags vocabulary table.
// Feed sources (RSS/Atom categories, iCal VCATEGORY, JSON category/style
// fields) commonly use their own taxonomy unrelated to dansal's; those terms
// are silently discarded here rather than rejected as invalid (see #923).
func filterKnownTags(tags []string) []string {
	if len(tags) == 0 {
		return tags
	}
	known, err := knownTagSlugs()
	if err != nil {
		return tags
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if known[t] {
			out = append(out, t)
		}
	}
	return out
}

// categoryAliasMap returns the admin-configured mapping of raw feed category
// strings (e.g. "Bal Folk Termine") to known tag slugs (e.g. "bal-folk"),
// set up via the import preview UI or the /admin/category-mappings page (#1093).
func categoryAliasMap() (map[string]string, error) {
	rows, err := db.Query("SELECT category, tag_slug FROM category_aliases")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var category, slug string
		if err := rows.Scan(&category, &slug); err != nil {
			return nil, err
		}
		m[category] = slug
	}
	return m, rows.Err()
}

// resolveFeedTags maps raw feed category strings through category_aliases to
// known tag slugs, then applies the same known-slug filter as filterKnownTags.
// This lets an admin map a feed's own taxonomy onto dansal's tags once (#1093)
// instead of every future fetch of that source importing untagged.
func resolveFeedTags(tags []string) []string {
	if len(tags) == 0 {
		return tags
	}
	known, err := knownTagSlugs()
	if err != nil {
		return filterKnownTags(tags)
	}
	aliases, err := categoryAliasMap()
	if err != nil {
		aliases = nil
	}
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	add := func(t string) {
		if t != "" && known[t] && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, t := range tags {
		if known[t] {
			add(t)
			continue
		}
		if slug, ok := aliases[t]; ok {
			add(slug)
		}
	}
	return out
}

// CategoryAlias maps a raw feed category string to a known tag slug (#1093).
type CategoryAlias struct {
	Category string `json:"category"`
	TagSlug  string `json:"tag_slug"`
}

// GET /api/v1/category-aliases
func getCategoryAliases(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rows, err := db.Query("SELECT category, tag_slug FROM category_aliases ORDER BY category")
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()
	aliases := []CategoryAlias{}
	for rows.Next() {
		var a CategoryAlias
		if err := rows.Scan(&a.Category, &a.TagSlug); err != nil {
			writeInternalError(w, err)
			return
		}
		aliases = append(aliases, a)
	}
	json.NewEncoder(w).Encode(aliases)
}

// categoryAliasWriteAllowed matches the fetchURL() create-source policy:
// admins and regular users may manage category mappings, publishers may not.
func categoryAliasWriteAllowed(w http.ResponseWriter, callerRole string) bool {
	if callerRole == RolePublisher {
		writeError(w, "Forbidden: publishers may not manage category mappings", http.StatusForbidden)
		return false
	}
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// POST /api/v1/category-aliases
func createCategoryAlias(w http.ResponseWriter, r *http.Request) {
	_, callerRole := callerFromRequest(r)
	if !categoryAliasWriteAllowed(w, callerRole) {
		return
	}
	var req CategoryAlias
	if !decodeJSONBody(w, r, &req) {
		return
	}
	req.Category = strings.TrimSpace(req.Category)
	req.TagSlug = strings.TrimSpace(req.TagSlug)
	if req.Category == "" || req.TagSlug == "" {
		writeError(w, "category and tag_slug are required", http.StatusBadRequest)
		return
	}
	known, err := knownTagSlugs()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !known[req.TagSlug] {
		writeError(w, fmt.Sprintf("unknown tag %q", req.TagSlug), http.StatusBadRequest)
		return
	}
	if _, err := db.Exec(
		"INSERT INTO category_aliases (category, tag_slug) VALUES (?, ?) ON CONFLICT(category) DO UPDATE SET tag_slug=excluded.tag_slug",
		req.Category, req.TagSlug,
	); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/category-aliases?category=...
func deleteCategoryAlias(w http.ResponseWriter, r *http.Request) {
	_, callerRole := callerFromRequest(r)
	if !categoryAliasWriteAllowed(w, callerRole) {
		return
	}
	category := r.URL.Query().Get("category")
	if category == "" {
		writeError(w, "category is required", http.StatusBadRequest)
		return
	}
	if _, err := db.Exec("DELETE FROM category_aliases WHERE category = ?", category); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	rows, err := db.Query(`
		SELECT t.slug, t.name, t.category, COUNT(et.event_id) AS event_count
		FROM tags t
		LEFT JOIN event_tags et ON et.tag = t.slug
		GROUP BY t.slug, t.name, t.category
		ORDER BY t.category, t.name`)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()
	tags := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.Slug, &t.Name, &t.Category, &t.EventCount); err != nil {
			writeInternalError(w, err)
			return
		}
		tags = append(tags, t)
	}
	json.NewEncoder(w).Encode(tags)
}

// POST /api/v1/events/bulk-set-attributes — set org, tags, dances, musicians,
// instructors, food/drink, accessibility attributes, and/or pricing type on
// multiple events. Nil fields are skipped. Tags, dances, musicians and
// instructors are additive (existing kept).
func bulkSetEventAttributes(w http.ResponseWriter, r *http.Request) {
	callerID, role := callerFromRequest(r)
	if role != RoleAdmin && role != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		IDs            []int    `json:"ids"`
		OrgID          *int     `json:"org_id"`          // nil = skip; set to apply (can be 0 to unset)
		AddTags        []string `json:"add_tags"`        // nil = skip; additive
		RemoveTags     []string `json:"remove_tags"`     // nil = skip; subtractive
		AddDances      []int    `json:"add_dances"`      // nil = skip; additive (dance IDs)
		RemoveDances   []int    `json:"remove_dances"`   // nil = skip; subtractive
		AddMusicians   []int    `json:"add_musicians"`   // nil = skip; additive (musician IDs)
		AddInstructors []int    `json:"add_instructors"` // nil = skip; additive (instructor IDs)
		Food           *string  `json:"food"`            // nil = skip; "" unsets
		Drink          *string  `json:"drink"`           // nil = skip; "" unsets
		Wheelchair     *bool    `json:"wheelchair"`      // nil = skip
		Bar            *bool    `json:"bar"`             // nil = skip
		Kitchen        *bool    `json:"kitchen"`         // nil = skip
		PricingType    *string  `json:"pricing_type"`    // nil = skip; "free"/"donation"
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
		if req.RemoveTags != nil {
			for _, t := range req.RemoveTags {
				if t = strings.TrimSpace(t); t != "" {
					db.Exec("DELETE FROM event_tags WHERE event_id=? AND tag=?", id, t)
				}
			}
		}
		if req.AddDances != nil {
			batchInsertPairs(db, "event_dances", "event_id", "dance_id", id, req.AddDances)
		}
		if req.RemoveDances != nil {
			for _, did := range req.RemoveDances {
				db.Exec("DELETE FROM event_dances WHERE event_id=? AND dance_id=?", id, did)
			}
		}
		if req.AddMusicians != nil {
			batchInsertPairs(db, "event_musicians", "event_id", "musician_id", id, req.AddMusicians)
		}
		if req.AddInstructors != nil {
			batchInsertPairs(db, "event_instructors", "event_id", "instructor_id", id, req.AddInstructors)
		}
		if req.Food != nil {
			db.Exec("UPDATE events SET food=? WHERE id=?", *req.Food, id)
		}
		if req.Drink != nil {
			db.Exec("UPDATE events SET drink=? WHERE id=?", *req.Drink, id)
		}
		if req.Wheelchair != nil || req.Bar != nil || req.Kitchen != nil {
			var attrsRaw string
			db.QueryRow("SELECT COALESCE(json(attributes),'{}') FROM events WHERE id=?", id).Scan(&attrsRaw)
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
			db.QueryRow("SELECT COALESCE(json(pricing),'') FROM events WHERE id=?", id).Scan(&pricingRaw)
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
		touchEvent(id, callerID)
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
		touchEvent(id, callerID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/events/bulk-set-time — set the time-of-day of start_time/end_time
// on multiple events, keeping each event's own calendar date. Mirrors the
// single-day time semantics used by addSeriesDate (series.go). Either field
// may be omitted to leave it unchanged.
// admin: unrestricted. user: skips events where caller is not an org member.
func bulkSetEventTime(w http.ResponseWriter, r *http.Request) {
	callerID, role := callerFromRequest(r)
	if role != RoleAdmin && role != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		IDs       []int  `json:"ids"`
		StartTime string `json:"start_time"` // "HH:MM"; empty = leave unchanged
		EndTime   string `json:"end_time"`   // "HH:MM"; empty = leave unchanged
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		writeError(w, "ids required", http.StatusBadRequest)
		return
	}
	if req.StartTime == "" && req.EndTime == "" {
		writeError(w, "start_time or end_time required", http.StatusBadRequest)
		return
	}
	for _, id := range req.IDs {
		if role != RoleAdmin && !isOrgMemberOfEvent(callerID, id) {
			continue
		}
		var startEpoch, endEpoch int64
		if err := db.QueryRow("SELECT start_time, end_time FROM events WHERE id=?", id).Scan(&startEpoch, &endEpoch); err != nil {
			continue
		}
		d := time.Unix(startEpoch, 0).In(berlinLoc)
		newStart, newEnd := startEpoch, endEpoch
		if req.StartTime != "" {
			newStart = combineDateAndTime(d, req.StartTime)
		}
		if req.EndTime != "" {
			newEnd = combineDateAndTime(d, req.EndTime)
		}
		if newEnd <= newStart {
			newEnd = newStart + 3*3600
		}
		db.Exec("UPDATE events SET start_time=?, end_time=? WHERE id=?", newStart, newEnd, id)
		touchEvent(id, callerID)
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
	eventID, err := intPathValue(r, "id")
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
	eventID, err := intPathValue(r, "id")
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
		writeInternalError(w, err)
		return
	}
	db.Exec("INSERT OR IGNORE INTO event_locations (event_id, location_id) VALUES (?,?)", eventID, req.LocationID)
	syncEventLocationGeohash(eventID)
	touchEvent(eventID, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/events/{id}/location — clear the event's location.
func unsetEventLocationRef(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	db.Exec("UPDATE events SET location_id=NULL WHERE id=?", eventID)
	touchEvent(eventID, callerID)
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
	eventID, err := intPathValue(r, "id")
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
		writeInternalError(w, err)
		return
	}
	touchEvent(eventID, callerID)
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
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	db.Exec("UPDATE events SET organization_id=NULL WHERE id=?", eventID)
	touchEvent(eventID, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/events/{id}/musicians/{musician_id} — add one musician to the event.
func addEventMusician(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	musicianID, err := intPathValue(r, "musician_id")
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
	touchEvent(eventID, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/events/{id}/musicians/{musician_id} — remove one musician from the event.
func removeEventMusician(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	musicianID, err := intPathValue(r, "musician_id")
	if err != nil {
		writeError(w, "invalid musician id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	db.Exec("DELETE FROM event_musicians WHERE event_id=? AND musician_id=?", eventID, musicianID)
	touchEvent(eventID, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/events/{id}/instructors/{instructor_id} — add one instructor to the event.
func addEventInstructor(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	instructorID, err := intPathValue(r, "instructor_id")
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
	touchEvent(eventID, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/events/{id}/instructors/{instructor_id} — remove one instructor from the event.
func removeEventInstructor(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	instructorID, err := intPathValue(r, "instructor_id")
	if err != nil {
		writeError(w, "invalid instructor id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	db.Exec("DELETE FROM event_instructors WHERE event_id=? AND instructor_id=?", eventID, instructorID)
	touchEvent(eventID, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/events/{id}/dances/{dance_id} — add one dance to the event.
func addEventDance(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	danceID, err := intPathValue(r, "dance_id")
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
	touchEvent(eventID, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/events/{id}/dances/{dance_id} — remove one dance from the event.
func removeEventDance(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	danceID, err := intPathValue(r, "dance_id")
	if err != nil {
		writeError(w, "invalid dance id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	db.Exec("DELETE FROM event_dances WHERE event_id=? AND dance_id=?", eventID, danceID)
	touchEvent(eventID, callerID)
	w.WriteHeader(http.StatusNoContent)
}
