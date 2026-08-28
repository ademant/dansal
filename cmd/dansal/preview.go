package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	ics "github.com/arran4/golang-ical"
)

// POST /api/v1/events/preview — parse iCal or folkdance-JSON without saving.
// Accepts multipart/form-data with fields: file, url, type, organization_id.
func previewEventsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, callerRole := callerFromRequest(r)

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeError(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	feedType := r.FormValue("type")
	if feedType == "" {
		feedType = "ical"
	}

	// Non-admins must belong to the target organisation.
	var orgID *int
	if callerRole != RoleAdmin {
		orgStr := r.FormValue("organization_id")
		if orgStr == "" {
			orgStr = r.URL.Query().Get("organization_id")
		}
		if orgStr == "" {
			writeError(w, "organization_id is required", http.StatusBadRequest)
			return
		}
		n, err := strconv.Atoi(orgStr)
		if err != nil || !isOrgMember(callerID, n) {
			writeError(w, "Forbidden: not a member of the specified organization", http.StatusForbidden)
			return
		}
		orgID = &n
	}

	src := FetchSource{Type: feedType, OrganizationID: orgID}

	var body []byte

	if feedURL := r.FormValue("url"); feedURL != "" {
		// webcal:// and webcals:// are iCal subscription aliases (the latter the
		// secure variant, analogous to http/https); treat both as https, same as
		// the fetchURL handler does when saving a recurring fetch source.
		lowerURL := strings.ToLower(feedURL)
		if strings.HasPrefix(lowerURL, "webcals://") {
			feedURL = "https://" + feedURL[len("webcals://"):]
		} else if strings.HasPrefix(lowerURL, "webcal://") {
			feedURL = "https://" + feedURL[len("webcal://"):]
		}
		parsed, err := url.Parse(feedURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			writeError(w, "URL must use http or https scheme", http.StatusBadRequest)
			return
		}
		src.URL = feedURL
		if feedType == "ical" {
			src.Type = detectFetchType(feedURL)
		}
		resp, err := safeClient.Get(feedURL)
		if err != nil {
			writeError(w, "fetch failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			writeError(w, fmt.Sprintf("remote returned %d", resp.StatusCode), http.StatusBadGateway)
			return
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if err != nil {
			writeError(w, "read failed", http.StatusBadGateway)
			return
		}
	} else {
		file, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, "file or url is required", http.StatusBadRequest)
			return
		}
		defer file.Close()
		body, err = io.ReadAll(io.LimitReader(file, 10<<20))
		if err != nil {
			writeError(w, "read failed", http.StatusBadRequest)
			return
		}
	}

	reqs, err := parseBodyToRequests(body, src)
	if err != nil {
		writeError(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if reqs == nil {
		reqs = []EventCreateRequest{}
	}
	for i := range reqs {
		reqs[i].DuplicateStatus = previewDuplicateStatus(reqs[i])
	}
	json.NewEncoder(w).Encode(reqs)
}

// previewDuplicateStatus checks the DB for a matching event using the same
// shared findExistingEvent dedup hierarchy as insertEvent (#1005) and
// returns "new", "exists", or "updated".
func previewDuplicateStatus(req EventCreateRequest) string {
	startStr := req.StartTime
	if len(req.Date) > 0 {
		startStr = req.Date[0].StartTime
	}
	startEpoch, startErr := parseTimeToUnix(startStr)
	var startTime *int64
	if startErr == nil {
		startTime = &startEpoch
	}

	// Resolve the feed's location name to a DB location_id the same way
	// tier 3 needs it — insertEvent's caller resolves this via ensureLocation
	// (which may create a row); preview must not create anything, so it does
	// a read-only lookup instead. locID stays 0 (tier 3 skipped) when the
	// feed's location name doesn't match a stored location.
	var locID int64
	if startErr == nil {
		db.QueryRow("SELECT id FROM locations WHERE location=?", req.Location.Location).Scan(&locID)
		if locID == 0 && req.Location.Address != "" {
			composite := req.Location.Location + " - " + req.Location.Address
			db.QueryRow("SELECT id FROM locations WHERE location=?", composite).Scan(&locID)
		}
	}

	found, tier, err := findExistingEvent(db, req.Title, req.URL, startTime, locID, req.UID, req.FetchSourceID)
	// Tier 5 is a low-confidence review hint, not a real match — preview has
	// no "flag for review" state, so it reports "new" same as no match.
	if err != nil || tier == TierNone || tier == TierFuzzyReview {
		return "new"
	}

	// Always check whether the feed's location geodata differs from the DB —
	// location coordinates can change independently of the event record.
	if previewLocationUpdated(found.ID, req.Location) {
		return "updated"
	}

	// Match found — determine whether event data has changed.
	if req.SourceLastModified > 0 {
		if req.SourceLastModified > found.SourceLastModified {
			return "updated"
		}
		return "exists"
	}

	// No source timestamps — compare key event fields.
	if startTime != nil && *startTime != found.StartTime {
		return "updated"
	}
	if req.IsCancelled != found.IsCancelled {
		return "updated"
	}
	if req.Title != found.Title {
		return "updated"
	}
	return "exists"
}

// previewLocationUpdated returns true when the feed supplies coordinates that
// differ meaningfully from what is stored in the DB for the matched event's
// location. A tolerance of 0.0001° (~11 m) avoids false positives from
// floating-point rounding in different feed sources.
func previewLocationUpdated(eventID int, loc EventLocationRequest) bool {
	if loc.Latitude == nil && loc.Longitude == nil {
		return false // feed carries no geodata — nothing to compare
	}
	const eps = 0.0001
	var dbLat, dbLng *float64
	db.QueryRow(`SELECT l.latitude, l.longitude
		FROM events e JOIN locations l ON e.location_id = l.id
		WHERE e.id = ?`, eventID).Scan(&dbLat, &dbLng)

	if loc.Latitude != nil {
		if dbLat == nil || math.Abs(*loc.Latitude-*dbLat) > eps {
			return true
		}
	}
	if loc.Longitude != nil {
		if dbLng == nil || math.Abs(*loc.Longitude-*dbLng) > eps {
			return true
		}
	}
	return false
}

func parseBodyToRequests(body []byte, src FetchSource) ([]EventCreateRequest, error) {
	switch src.Type {
	case "json":
		if gancioJSONProbe(src.URL) {
			return parseGancioJSONToRequests(body, src)
		}
		if tecJSONProbe(src.URL) {
			return parseTECJSONToRequests(body, src)
		}
		return parseFolkdanceJSONToRequests(body, src)
	case "folkdance-json":
		return parseFolkdanceJSONToRequests(body, src)
	case "gancio-json":
		return parseGancioJSONToRequests(body, src)
	default:
		cal, err := ics.ParseCalendar(strings.NewReader(string(body)))
		if err != nil {
			return nil, fmt.Errorf("parse iCal: %w", err)
		}
		return parseICalToRequests(cal, src), nil
	}
}
