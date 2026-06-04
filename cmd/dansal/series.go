package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// EventSeries represents a recurring event series.
type EventSeries struct {
	ID                int          `json:"id"`
	Slug              string       `json:"slug"`
	Title             string       `json:"title"`
	Description       string       `json:"description"`
	OrganizationID    *int         `json:"organization_id,omitempty"`
	DefaultLocationID *int         `json:"default_location_id,omitempty"`
	DefaultStartTime  string       `json:"default_start_time"`
	DefaultEndTime    string       `json:"default_end_time"`
	InviteToken       string       `json:"invite_token,omitempty"`
	CreatedAt         string       `json:"created_at"`
	UpdatedAt         int64        `json:"updated_at"`
	EventCount        int          `json:"event_count,omitempty"`
	Events            []SeriesEvent `json:"events,omitempty"`
}

// SeriesEvent is a lightweight event view for the management table.
type SeriesEvent struct {
	ID           int    `json:"id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	StartTime    string `json:"start_time"` // RFC3339 local
	EndTime      string `json:"end_time"`
	LocationID   *int   `json:"location_id,omitempty"`
	LocationName string `json:"location_name,omitempty"`
	IsCancelled  bool   `json:"is_cancelled"`
	IsPublished  bool   `json:"is_published"`
}

// slugify converts a title to a URL-safe slug.
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevHyphen = false
		} else if !prevHyphen {
			b.WriteRune('-')
			prevHyphen = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	// Remove non-ASCII
	re := regexp.MustCompile(`[^a-z0-9-]`)
	slug = re.ReplaceAllString(slug, "")
	// Deduplicate hyphens
	re2 := regexp.MustCompile(`-{2,}`)
	slug = re2.ReplaceAllString(slug, "-")
	return strings.Trim(slug, "-")
}

// uniqueSlug generates a unique slug, appending -2, -3 etc. if needed.
func uniqueSlug(base string, excludeID int) string {
	slug := base
	for i := 2; ; i++ {
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM event_series WHERE slug=? AND id!=?", slug, excludeID).Scan(&count)
		if err != nil || count == 0 {
			return slug
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
}

// scanSeries scans one row from event_series.
func scanSeries(row interface{ Scan(...any) error }) (EventSeries, error) {
	var s EventSeries
	var orgID, locID sql.NullInt64
	var inviteToken sql.NullString
	if err := row.Scan(
		&s.ID, &s.Slug, &s.Title, &s.Description,
		&orgID, &locID,
		&s.DefaultStartTime, &s.DefaultEndTime,
		&inviteToken, &s.CreatedAt, &s.UpdatedAt,
	); err != nil {
		return s, err
	}
	if orgID.Valid {
		v := int(orgID.Int64)
		s.OrganizationID = &v
	}
	if locID.Valid {
		v := int(locID.Int64)
		s.DefaultLocationID = &v
	}
	if inviteToken.Valid {
		s.InviteToken = inviteToken.String
	}
	return s, nil
}

const seriesSelectCols = `id, slug, title, COALESCE(description,''),
	organization_id, default_location_id,
	COALESCE(default_start_time,''), COALESCE(default_end_time,''),
	invite_token, created_at, COALESCE(updated_at,0)`

// loadSeriesEvents loads all events belonging to a series, ordered by start_time.
func loadSeriesEvents(seriesID int) ([]SeriesEvent, error) {
	rows, err := db.Query(`
		SELECT e.id, e.title, COALESCE(e.description,''), e.start_time, e.end_time,
		       e.location_id, COALESCE(l.location,''),
		       e.is_cancelled, e.is_published
		FROM events e
		LEFT JOIN locations l ON l.id = e.location_id
		WHERE e.series_id = ?
		ORDER BY e.start_time ASC`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []SeriesEvent
	for rows.Next() {
		var se SeriesEvent
		var locID sql.NullInt64
		var startEpoch, endEpoch int64
		var isCancelled, isPublished int
		if err := rows.Scan(
			&se.ID, &se.Title, &se.Description,
			&startEpoch, &endEpoch,
			&locID, &se.LocationName,
			&isCancelled, &isPublished,
		); err != nil {
			return nil, err
		}
		if locID.Valid {
			v := int(locID.Int64)
			se.LocationID = &v
		}
		se.IsCancelled = isCancelled != 0
		se.IsPublished = isPublished != 0
		se.StartTime = time.Unix(startEpoch, 0).Format(time.RFC3339)
		se.EndTime = time.Unix(endEpoch, 0).Format(time.RFC3339)
		events = append(events, se)
	}
	return events, nil
}

// checkSeriesAccess returns (seriesID, orgID, ok). If not ok, the handler has already written the error.
func checkSeriesAccess(w http.ResponseWriter, r *http.Request) (series EventSeries, ok bool) {
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	row := db.QueryRow("SELECT "+seriesSelectCols+" FROM event_series WHERE id=?", id)
	series, err = scanSeries(row)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	callerID, role := callerFromRequest(r)
	if callerID == 0 {
		writeError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if role == RoleAdmin {
		ok = true
		return
	}
	if series.OrganizationID != nil && isOrgMember(callerID, *series.OrganizationID) {
		ok = true
		return
	}
	writeError(w, "forbidden", http.StatusForbidden)
	return
}

// GET /api/v1/series
func getSeries(w http.ResponseWriter, r *http.Request) {
	callerID, role := callerFromRequest(r)
	if callerID == 0 {
		writeError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var rows *sql.Rows
	var err error

	orgIDFilter := r.URL.Query().Get("org_id")

	if role == RoleAdmin {
		if orgIDFilter != "" {
			oid, _ := strconv.Atoi(orgIDFilter)
			rows, err = db.Query(`
				SELECT `+seriesSelectCols+`,
				       (SELECT COUNT(*) FROM events WHERE series_id = event_series.id) AS event_count
				FROM event_series WHERE organization_id=? ORDER BY id DESC`, oid)
		} else {
			rows, err = db.Query(`
				SELECT `+seriesSelectCols+`,
				       (SELECT COUNT(*) FROM events WHERE series_id = event_series.id) AS event_count
				FROM event_series ORDER BY id DESC`)
		}
	} else {
		// Non-admins only see series for orgs they belong to.
		rows, err = db.Query(`
			SELECT `+seriesSelectCols+`,
			       (SELECT COUNT(*) FROM events WHERE series_id = event_series.id) AS event_count
			FROM event_series
			WHERE organization_id IN (
			      SELECT organization_id FROM organization_members WHERE user_id=?)
			ORDER BY id DESC`, callerID)
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	result := []EventSeries{}
	for rows.Next() {
		var s EventSeries
		var orgID, locID sql.NullInt64
		var inviteToken sql.NullString
		if err := rows.Scan(
			&s.ID, &s.Slug, &s.Title, &s.Description,
			&orgID, &locID,
			&s.DefaultStartTime, &s.DefaultEndTime,
			&inviteToken, &s.CreatedAt, &s.UpdatedAt,
			&s.EventCount,
		); err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if orgID.Valid {
			v := int(orgID.Int64)
			s.OrganizationID = &v
		}
		if locID.Valid {
			v := int(locID.Int64)
			s.DefaultLocationID = &v
		}
		if inviteToken.Valid {
			s.InviteToken = inviteToken.String
		}
		result = append(result, s)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// POST /api/v1/series
func createSeries(w http.ResponseWriter, r *http.Request) {
	callerID, role := callerFromRequest(r)
	if callerID == 0 {
		writeError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Title             string `json:"title"`
		Description       string `json:"description"`
		OrganizationID    *int   `json:"organization_id"`
		DefaultLocationID *int   `json:"default_location_id"`
		DefaultStartTime  string `json:"default_start_time"`
		DefaultEndTime    string `json:"default_end_time"`
		StartDate         string `json:"start_date"`
		Recurrence        string `json:"recurrence"`
		Occurrences       int    `json:"occurrences"`
		EndDate           string `json:"end_date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, "title is required", http.StatusBadRequest)
		return
	}

	// Auth check
	if role != RoleAdmin {
		if req.OrganizationID == nil {
			writeError(w, "organization_id required for non-admin", http.StatusBadRequest)
			return
		}
		if !isOrgMember(callerID, *req.OrganizationID) {
			writeError(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	// Parse start date
	if req.StartDate == "" {
		writeError(w, "start_date is required", http.StatusBadRequest)
		return
	}
	startDate, err := time.Parse("2006-01-02", req.StartDate)
	if err != nil {
		writeError(w, "invalid start_date: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Parse default times
	startTimeStr := req.DefaultStartTime
	endTimeStr := req.DefaultEndTime
	if startTimeStr == "" {
		startTimeStr = "20:00"
	}
	if endTimeStr == "" {
		endTimeStr = "23:00"
	}

	// Parse recurrence interval
	interval := 7 // weekly default
	if req.Recurrence == "biweekly" {
		interval = 14
	}

	// Compute dates
	var dates []time.Time
	if req.EndDate != "" {
		endDate, err := time.Parse("2006-01-02", req.EndDate)
		if err != nil {
			writeError(w, "invalid end_date", http.StatusBadRequest)
			return
		}
		d := startDate
		for !d.After(endDate) && len(dates) < 52 {
			dates = append(dates, d)
			d = d.AddDate(0, 0, interval)
		}
	} else {
		n := req.Occurrences
		if n <= 0 {
			n = 10
		}
		if n > 52 {
			n = 52
		}
		d := startDate
		for i := 0; i < n; i++ {
			dates = append(dates, d)
			d = d.AddDate(0, 0, interval)
		}
	}

	// Build slug
	baseSlug := slugify(req.Title)
	if baseSlug == "" {
		baseSlug = "series"
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	slug := uniqueSlug(baseSlug, 0)

	var result sql.Result
	if req.OrganizationID != nil {
		result, err = tx.Exec(`INSERT INTO event_series
			(slug, title, description, organization_id, default_location_id, default_start_time, default_end_time, updated_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			slug, req.Title, req.Description,
			*req.OrganizationID, optionalInt(req.DefaultLocationID),
			startTimeStr, endTimeStr,
			time.Now().Unix(),
		)
	} else {
		result, err = tx.Exec(`INSERT INTO event_series
			(slug, title, description, default_location_id, default_start_time, default_end_time, updated_at)
			VALUES (?,?,?,?,?,?,?)`,
			slug, req.Title, req.Description,
			optionalInt(req.DefaultLocationID),
			startTimeStr, endTimeStr,
			time.Now().Unix(),
		)
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	seriesID64, _ := result.LastInsertId()
	seriesID := int(seriesID64)

	// Generate event rows
	for _, d := range dates {
		startEpoch := combineDateAndTime(d, startTimeStr)
		endEpoch := combineDateAndTime(d, endTimeStr)
		if endEpoch <= startEpoch {
			endEpoch = startEpoch + 3*3600 // fallback 3h
		}
		if req.OrganizationID != nil {
			_, err = tx.Exec(`INSERT INTO events
				(title, description, start_time, end_time, location_id, organization_id, series_id, is_published)
				VALUES (?,?,?,?,?,?,?,0)`,
				req.Title, "", startEpoch, endEpoch,
				optionalInt(req.DefaultLocationID), *req.OrganizationID, seriesID,
			)
		} else {
			_, err = tx.Exec(`INSERT INTO events
				(title, description, start_time, end_time, location_id, series_id, is_published)
				VALUES (?,?,?,?,?,?,0)`,
				req.Title, "", startEpoch, endEpoch,
				optionalInt(req.DefaultLocationID), seriesID,
			)
		}
		if err != nil {
			writeError(w, "failed to insert event: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return created series
	row := db.QueryRow("SELECT "+seriesSelectCols+" FROM event_series WHERE id=?", seriesID)
	s, err := scanSeries(row)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.EventCount = len(dates)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(s)
}

// GET /api/v1/series/{id}
func getSeriesByID(w http.ResponseWriter, r *http.Request) {
	series, ok := checkSeriesAccess(w, r)
	if !ok {
		return
	}
	events, err := loadSeriesEvents(series.ID)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	series.Events = events
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(series)
}

// PUT /api/v1/series/{id}
func updateSeries(w http.ResponseWriter, r *http.Request) {
	series, ok := checkSeriesAccess(w, r)
	if !ok {
		return
	}
	callerID, role := callerFromRequest(r)

	var req struct {
		Title             string `json:"title"`
		Description       string `json:"description"`
		DefaultLocationID *int   `json:"default_location_id"`
		DefaultStartTime  string `json:"default_start_time"`
		DefaultEndTime    string `json:"default_end_time"`
		OrganizationID    *int   `json:"organization_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.Title) == "" {
		req.Title = series.Title
	}

	orgID := series.OrganizationID
	if role == RoleAdmin && req.OrganizationID != nil {
		orgID = req.OrganizationID
	}
	_ = callerID

	_, err := db.Exec(`UPDATE event_series
		SET title=?, description=?, default_location_id=?, default_start_time=?, default_end_time=?,
		    organization_id=?, updated_at=?
		WHERE id=?`,
		req.Title, req.Description,
		optionalInt(req.DefaultLocationID),
		req.DefaultStartTime, req.DefaultEndTime,
		optionalInt(orgID),
		time.Now().Unix(),
		series.ID,
	)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/series/{id}
func deleteSeries(w http.ResponseWriter, r *http.Request) {
	series, ok := checkSeriesAccess(w, r)
	if !ok {
		return
	}
	// Detach events (set series_id=NULL), do NOT delete events
	db.Exec("UPDATE events SET series_id=NULL WHERE series_id=?", series.ID)
	db.Exec("DELETE FROM event_series WHERE id=?", series.ID)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/series/{id}/add-date
func addSeriesDate(w http.ResponseWriter, r *http.Request) {
	series, ok := checkSeriesAccess(w, r)
	if !ok {
		return
	}

	var req struct {
		Date      string `json:"date"`
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Date == "" {
		writeError(w, "date is required", http.StatusBadRequest)
		return
	}
	d, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		writeError(w, "invalid date", http.StatusBadRequest)
		return
	}

	startTimeStr := req.StartTime
	endTimeStr := req.EndTime
	if startTimeStr == "" {
		startTimeStr = series.DefaultStartTime
	}
	if endTimeStr == "" {
		endTimeStr = series.DefaultEndTime
	}
	if startTimeStr == "" {
		startTimeStr = "20:00"
	}
	if endTimeStr == "" {
		endTimeStr = "23:00"
	}

	startEpoch := combineDateAndTime(d, startTimeStr)
	endEpoch := combineDateAndTime(d, endTimeStr)
	if endEpoch <= startEpoch {
		endEpoch = startEpoch + 3*3600
	}

	if series.OrganizationID != nil {
		_, err = db.Exec(`INSERT INTO events
			(title, description, start_time, end_time, location_id, organization_id, series_id, is_published)
			VALUES (?,?,?,?,?,?,?,0)`,
			series.Title, "", startEpoch, endEpoch,
			optionalInt(series.DefaultLocationID), *series.OrganizationID, series.ID,
		)
	} else {
		_, err = db.Exec(`INSERT INTO events
			(title, description, start_time, end_time, location_id, series_id, is_published)
			VALUES (?,?,?,?,?,?,0)`,
			series.Title, "", startEpoch, endEpoch,
			optionalInt(series.DefaultLocationID), series.ID,
		)
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// POST /api/v1/series/{id}/token/regenerate
func regenerateSeriesToken(w http.ResponseWriter, r *http.Request) {
	series, ok := checkSeriesAccess(w, r)
	if !ok {
		return
	}
	tok, err := generateToken(24)
	if err != nil {
		writeError(w, "token generation failed", http.StatusInternalServerError)
		return
	}
	_, err = db.Exec("UPDATE event_series SET invite_token=?, updated_at=? WHERE id=?",
		tok, time.Now().Unix(), series.ID)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"invite_token": tok})
}

// POST /api/v1/series/{id}/token/revoke
func revokeSeriesToken(w http.ResponseWriter, r *http.Request) {
	series, ok := checkSeriesAccess(w, r)
	if !ok {
		return
	}
	_, err := db.Exec("UPDATE event_series SET invite_token=NULL, updated_at=? WHERE id=?",
		time.Now().Unix(), series.ID)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/series/token/{token}
func getSeriesByToken(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	row := db.QueryRow("SELECT "+seriesSelectCols+" FROM event_series WHERE invite_token=?", tok)
	s, err := scanSeries(row)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	events, err := loadSeriesEvents(s.ID)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Events = events
	// Strip the token from the public response
	s.InviteToken = ""
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

// PATCH /api/v1/series/token/{token}/events/{eventID}
func patchSeriesEventDescription(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	eventIDStr := r.PathValue("eventID")
	eventID, err := strconv.Atoi(eventIDStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Verify token and that event belongs to this series
	var seriesID int
	if err := db.QueryRow("SELECT id FROM event_series WHERE invite_token=?", tok).Scan(&seriesID); err != nil {
		http.NotFound(w, r)
		return
	}
	var evSeriesID sql.NullInt64
	if err := db.QueryRow("SELECT series_id FROM events WHERE id=?", eventID).Scan(&evSeriesID); err != nil {
		http.NotFound(w, r)
		return
	}
	if !evSeriesID.Valid || int(evSeriesID.Int64) != seriesID {
		writeError(w, "event does not belong to this series", http.StatusForbidden)
		return
	}

	var req struct {
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	_, err = db.Exec("UPDATE events SET description=? WHERE id=?", req.Description, eventID)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// optionalInt returns nil if p is nil, otherwise the dereferenced value as interface{}.
func optionalInt(p *int) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

// combineDateAndTime parses "HH:MM" and adds it to the given date, returning Unix epoch.
func combineDateAndTime(d time.Time, timeStr string) int64 {
	parts := strings.SplitN(timeStr, ":", 2)
	h, m := 20, 0
	if len(parts) == 2 {
		h, _ = strconv.Atoi(parts[0])
		m, _ = strconv.Atoi(parts[1])
	}
	t := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, d.Location())
	return t.Unix()
}
