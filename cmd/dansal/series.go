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
	ID                int             `json:"id"`
	Slug              string          `json:"slug"`
	Title             string          `json:"title"`
	Description       string          `json:"description"`
	OrganizationID    *int            `json:"organization_id,omitempty"`
	MusicianID        *int            `json:"musician_id,omitempty"`
	InstructorID      *int            `json:"instructor_id,omitempty"`
	DefaultLocationID *int            `json:"default_location_id,omitempty"`
	DefaultStartTime  string          `json:"default_start_time"`
	DefaultEndTime    string          `json:"default_end_time"`
	InviteToken       string          `json:"invite_token,omitempty"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         int64           `json:"updated_at"`
	EventCount        int             `json:"event_count,omitempty"`
	Events            []SeriesEvent   `json:"events,omitempty"`
	TemplateData      json.RawMessage `json:"template_data,omitempty"`
}

// seriesTemplateData is the rich per-series default set applied to every
// occurrence created via addSeriesDate/createSeries, on top of the series'
// own title/location/times/org (which have their own dedicated fields and
// semantics — recurrence dates, default venue — that don't belong here).
// Mirrors the shape of dansal_web's templateEventData (cmd/dansal_web/admin_templates.go)
// minus the fields that are series-specific already (start/end time, org, location).
type seriesTemplateData struct {
	HasBall            bool                    `json:"has_ball"`
	HasWorkshop        bool                    `json:"has_workshop"`
	HasFestival        bool                    `json:"has_festival"`
	WorkshopDifficulty string                  `json:"workshop_difficulty"`
	URL                string                  `json:"url"`
	BookingURL         string                  `json:"booking_url"`
	Pricing            *Pricing                `json:"pricing,omitempty"`
	Tags               []string                `json:"tags"`
	DanceIDs           []int                   `json:"dance_ids"`
	Food               string                  `json:"food"`
	Drink              string                  `json:"drink"`
	FloorCondition     string                  `json:"floor_condition"`
	Attributes         map[string]bool         `json:"attributes,omitempty"`
	ContactName        string                  `json:"contact_name,omitempty"`
	ContactEmail       string                  `json:"contact_email,omitempty"`
	TicketsTotal       int                     `json:"tickets_total"`
	BookingEnabled     bool                    `json:"booking_enabled"`
	Timetable          []TimetableEntryRequest `json:"timetable"`
}

// applySeriesTemplate writes seriesTemplateData's fields onto a freshly
// inserted event. Only called right after the bare INSERT in
// createSeries/addSeriesDate, so there is nothing to preserve — every field
// is safe to set unconditionally (an empty td leaves the new row at its
// just-inserted defaults, which matches a series with no template_data).
func applySeriesTemplate(q querier, eventID int, td seriesTemplateData) error {
	var pricingArg any
	if td.Pricing != nil {
		if b, err := json.Marshal(td.Pricing); err == nil {
			pricingArg = string(b)
		}
	}
	if _, err := q.Exec(
		`UPDATE events SET has_ball=?, has_workshop=?, has_festival=?, workshop_difficulty=?,
		 url=?, booking_url=?, pricing=?, tickets_total=?, booking_enabled=?,
		 food=?, drink=?, floor_condition=?, attributes=?, contact_name=?, contact_email=? WHERE id=?`,
		td.HasBall, td.HasWorkshop, td.HasFestival, td.WorkshopDifficulty,
		urlVal(td.URL), urlVal(td.BookingURL), pricingArg, td.TicketsTotal, td.BookingEnabled,
		td.Food, td.Drink, td.FloorCondition, attrsJSON(td.Attributes), td.ContactName, td.ContactEmail, eventID,
	); err != nil {
		return err
	}
	if err := syncEventTags(q, eventID, td.Tags); err != nil {
		return err
	}
	if len(td.DanceIDs) > 0 {
		if err := batchInsertPairs(q, "event_dances", "event_id", "dance_id", eventID, td.DanceIDs); err != nil {
			return err
		}
	}
	for _, entry := range td.Timetable {
		if _, err := insertEntry(q, eventID, entry); err != nil {
			return err
		}
	}
	return nil
}

// parseSeriesTemplateData decodes an event_series.template_data JSON blob,
// tolerating the empty/unset "{}" default.
func parseSeriesTemplateData(raw json.RawMessage) seriesTemplateData {
	var td seriesTemplateData
	if len(raw) == 0 {
		return td
	}
	_ = json.Unmarshal(raw, &td)
	return td
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
	var orgID, musicianID, instructorID, locID sql.NullInt64
	var inviteToken sql.NullString
	var templateData string
	if err := row.Scan(
		&s.ID, &s.Slug, &s.Title, &s.Description,
		&orgID, &musicianID, &instructorID, &locID,
		&s.DefaultStartTime, &s.DefaultEndTime,
		&inviteToken, &s.CreatedAt, &s.UpdatedAt, &templateData,
	); err != nil {
		return s, err
	}
	if orgID.Valid {
		v := int(orgID.Int64)
		s.OrganizationID = &v
	}
	if musicianID.Valid {
		v := int(musicianID.Int64)
		s.MusicianID = &v
	}
	if instructorID.Valid {
		v := int(instructorID.Int64)
		s.InstructorID = &v
	}
	if locID.Valid {
		v := int(locID.Int64)
		s.DefaultLocationID = &v
	}
	if inviteToken.Valid {
		s.InviteToken = inviteToken.String
	}
	s.TemplateData = json.RawMessage(templateData)
	return s, nil
}

const seriesSelectCols = `id, slug, title, COALESCE(description,''),
	organization_id, musician_id, instructor_id, default_location_id,
	COALESCE(default_start_time,''), COALESCE(default_end_time,''),
	invite_token, created_at, COALESCE(updated_at,0), COALESCE(template_data,'{}')`

// loadSeriesEvents loads all events belonging to a series, ordered by start_time.
func loadSeriesEvents(seriesID int) ([]SeriesEvent, error) {
	rows, err := db.Query(`
		SELECT e.id, e.title, COALESCE(e.description,''), e.start_time, e.end_time,
		       e.location_id, COALESCE(NULLIF(l.short_name,''), l.location,''),
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
		se.StartTime = epochToLocal(startEpoch)
		se.EndTime = epochToLocal(endEpoch)
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
		writeInternalError(w, err)
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
	if series.MusicianID != nil && isMusicianOwner(callerID, *series.MusicianID) {
		ok = true
		return
	}
	if series.InstructorID != nil && isInstructorOwner(callerID, *series.InstructorID) {
		ok = true
		return
	}
	writeError(w, "forbidden", http.StatusForbidden)
	return
}

// isMusicianOwner reports whether callerID is the user who created musicianID
// — the only ownership link musicians have, mirroring the check already used
// by deleteMusician.
func isMusicianOwner(callerID, musicianID int) bool {
	var createdBy sql.NullInt64
	if err := db.QueryRow("SELECT created_by_id FROM musicians WHERE id=?", musicianID).Scan(&createdBy); err != nil {
		return false
	}
	return createdBy.Valid && int(createdBy.Int64) == callerID
}

// isInstructorOwner is the instructor equivalent of isMusicianOwner.
func isInstructorOwner(callerID, instructorID int) bool {
	var createdBy sql.NullInt64
	if err := db.QueryRow("SELECT created_by_id FROM instructors WHERE id=?", instructorID).Scan(&createdBy); err != nil {
		return false
	}
	return createdBy.Valid && int(createdBy.Int64) == callerID
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
	musicianIDFilter := r.URL.Query().Get("musician_id")
	instructorIDFilter := r.URL.Query().Get("instructor_id")

	if role == RoleAdmin {
		switch {
		case orgIDFilter != "":
			oid, _ := strconv.Atoi(orgIDFilter)
			rows, err = db.Query(`
				SELECT `+seriesSelectCols+`,
				       (SELECT COUNT(*) FROM events WHERE series_id = event_series.id) AS event_count
				FROM event_series WHERE organization_id=? ORDER BY id DESC`, oid)
		case musicianIDFilter != "":
			mid, _ := strconv.Atoi(musicianIDFilter)
			rows, err = db.Query(`
				SELECT `+seriesSelectCols+`,
				       (SELECT COUNT(*) FROM events WHERE series_id = event_series.id) AS event_count
				FROM event_series WHERE musician_id=? ORDER BY id DESC`, mid)
		case instructorIDFilter != "":
			iid, _ := strconv.Atoi(instructorIDFilter)
			rows, err = db.Query(`
				SELECT `+seriesSelectCols+`,
				       (SELECT COUNT(*) FROM events WHERE series_id = event_series.id) AS event_count
				FROM event_series WHERE instructor_id=? ORDER BY id DESC`, iid)
		default:
			rows, err = db.Query(`
				SELECT ` + seriesSelectCols + `,
				       (SELECT COUNT(*) FROM events WHERE series_id = event_series.id) AS event_count
				FROM event_series ORDER BY id DESC`)
		}
	} else {
		// Non-admins only see series for orgs they belong to, or series they
		// own directly via a musician/instructor they created.
		rows, err = db.Query(`
			SELECT `+seriesSelectCols+`,
			       (SELECT COUNT(*) FROM events WHERE series_id = event_series.id) AS event_count
			FROM event_series
			WHERE organization_id IN (
			      SELECT organization_id FROM organization_members WHERE user_id=?)
			   OR musician_id IN (SELECT id FROM musicians WHERE created_by_id=?)
			   OR instructor_id IN (SELECT id FROM instructors WHERE created_by_id=?)
			ORDER BY id DESC`, callerID, callerID, callerID)
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()

	result := []EventSeries{}
	for rows.Next() {
		var s EventSeries
		var orgID, musicianID, instructorID, locID sql.NullInt64
		var inviteToken sql.NullString
		var templateData string
		if err := rows.Scan(
			&s.ID, &s.Slug, &s.Title, &s.Description,
			&orgID, &musicianID, &instructorID, &locID,
			&s.DefaultStartTime, &s.DefaultEndTime,
			&inviteToken, &s.CreatedAt, &s.UpdatedAt, &templateData,
			&s.EventCount,
		); err != nil {
			writeInternalError(w, err)
			return
		}
		if orgID.Valid {
			v := int(orgID.Int64)
			s.OrganizationID = &v
		}
		if musicianID.Valid {
			v := int(musicianID.Int64)
			s.MusicianID = &v
		}
		if instructorID.Valid {
			v := int(instructorID.Int64)
			s.InstructorID = &v
		}
		if locID.Valid {
			v := int(locID.Int64)
			s.DefaultLocationID = &v
		}
		if inviteToken.Valid {
			s.InviteToken = inviteToken.String
		}
		s.TemplateData = json.RawMessage(templateData)
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
		Title             string          `json:"title"`
		Description       string          `json:"description"`
		OrganizationID    *int            `json:"organization_id"`
		MusicianID        *int            `json:"musician_id"`
		InstructorID      *int            `json:"instructor_id"`
		DefaultLocationID *int            `json:"default_location_id"`
		DefaultStartTime  string          `json:"default_start_time"`
		DefaultEndTime    string          `json:"default_end_time"`
		StartDate         string          `json:"start_date"`
		Recurrence        string          `json:"recurrence"`
		Occurrences       int             `json:"occurrences"`
		EndDate           string          `json:"end_date"`
		TemplateData      json.RawMessage `json:"template_data,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, "title is required", http.StatusBadRequest)
		return
	}
	templateDataStr := "{}"
	if len(req.TemplateData) > 0 {
		var td seriesTemplateData
		if err := json.Unmarshal(req.TemplateData, &td); err != nil {
			writeError(w, "invalid template_data: "+err.Error(), http.StatusBadRequest)
			return
		}
		templateDataStr = string(req.TemplateData)
	}

	// Auth check — a series must be owned by something the caller has rights
	// over: an org they belong to, or a musician/instructor they created.
	// Every owner field supplied is validated independently (not just the
	// first one) — the INSERT below writes all three fields verbatim, so
	// leaving any of them unchecked would let a caller who legitimately owns
	// e.g. an org piggyback an arbitrary musician_id/instructor_id they don't
	// own onto the same request.
	if role != RoleAdmin {
		if req.OrganizationID == nil && req.MusicianID == nil && req.InstructorID == nil {
			writeError(w, "organization_id, musician_id or instructor_id required for non-admin", http.StatusBadRequest)
			return
		}
		if req.OrganizationID != nil && !isOrgMember(callerID, *req.OrganizationID) {
			writeError(w, "forbidden", http.StatusForbidden)
			return
		}
		if req.MusicianID != nil && !isMusicianOwner(callerID, *req.MusicianID) {
			writeError(w, "forbidden", http.StatusForbidden)
			return
		}
		if req.InstructorID != nil && !isInstructorOwner(callerID, *req.InstructorID) {
			writeError(w, "forbidden", http.StatusForbidden)
			return
		}
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

	// Compute dates from recurrence only when start_date is provided.
	var dates []time.Time
	if req.StartDate != "" {
		startDate, err := time.ParseInLocation("2006-01-02", req.StartDate, berlinLoc)
		if err != nil {
			writeError(w, "invalid start_date: "+err.Error(), http.StatusBadRequest)
			return
		}
		interval := 7
		if req.Recurrence == "biweekly" {
			interval = 14
		}
		if req.EndDate != "" {
			endDate, err := time.ParseInLocation("2006-01-02", req.EndDate, berlinLoc)
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
	}

	// Build slug
	baseSlug := slugify(req.Title)
	if baseSlug == "" {
		baseSlug = "series"
	}

	tx, err := db.Begin()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer tx.Rollback()

	slug := uniqueSlug(baseSlug, 0)

	result, err := tx.Exec(`INSERT INTO event_series
		(slug, title, description, organization_id, musician_id, instructor_id, default_location_id, default_start_time, default_end_time, updated_at, created_by_id, updated_by, template_data)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		slug, req.Title, req.Description,
		optionalInt(req.OrganizationID), optionalInt(req.MusicianID), optionalInt(req.InstructorID),
		optionalInt(req.DefaultLocationID),
		startTimeStr, endTimeStr,
		time.Now().Unix(), callerID, resolveDisplayName(callerID), templateDataStr,
	)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	seriesID64, _ := result.LastInsertId()
	seriesID := int(seriesID64)
	td := parseSeriesTemplateData(json.RawMessage(templateDataStr))

	// Generate event rows
	for _, d := range dates {
		startEpoch := combineDateAndTime(d, startTimeStr)
		endEpoch := combineDateAndTime(d, endTimeStr)
		if endEpoch <= startEpoch {
			endEpoch = startEpoch + 3*3600 // fallback 3h
		}
		var evResult sql.Result
		if req.OrganizationID != nil {
			evResult, err = tx.Exec(`INSERT INTO events
				(title, description, start_time, end_time, location_id, organization_id, series_id, is_published)
				VALUES (?,?,?,?,?,?,?,0)`,
				req.Title, "", startEpoch, endEpoch,
				optionalInt(req.DefaultLocationID), *req.OrganizationID, seriesID,
			)
		} else {
			evResult, err = tx.Exec(`INSERT INTO events
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
		evID64, _ := evResult.LastInsertId()
		evID := int(evID64)
		if req.MusicianID != nil {
			tx.Exec("INSERT OR IGNORE INTO event_musicians (event_id, musician_id) VALUES (?,?)", evID, *req.MusicianID)
		}
		if req.InstructorID != nil {
			tx.Exec("INSERT OR IGNORE INTO event_instructors (event_id, instructor_id) VALUES (?,?)", evID, *req.InstructorID)
		}
		if err := applySeriesTemplate(tx, evID, td); err != nil {
			writeError(w, "failed to apply series defaults: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		writeInternalError(w, err)
		return
	}

	// Return created series
	row := db.QueryRow("SELECT "+seriesSelectCols+" FROM event_series WHERE id=?", seriesID)
	s, err := scanSeries(row)
	if err != nil {
		writeInternalError(w, err)
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
		writeInternalError(w, err)
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
		Title             string          `json:"title"`
		Description       string          `json:"description"`
		DefaultLocationID *int            `json:"default_location_id"`
		DefaultStartTime  string          `json:"default_start_time"`
		DefaultEndTime    string          `json:"default_end_time"`
		OrganizationID    *int            `json:"organization_id"`
		MusicianID        *int            `json:"musician_id"`
		InstructorID      *int            `json:"instructor_id"`
		TemplateData      json.RawMessage `json:"template_data,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.Title) == "" {
		req.Title = series.Title
	}

	orgID := series.OrganizationID
	musicianID := series.MusicianID
	instructorID := series.InstructorID
	if role == RoleAdmin {
		if req.OrganizationID != nil {
			if *req.OrganizationID == 0 {
				orgID = nil
			} else {
				orgID = req.OrganizationID
			}
		}
		if req.MusicianID != nil {
			if *req.MusicianID == 0 {
				musicianID = nil
			} else {
				musicianID = req.MusicianID
			}
		}
		if req.InstructorID != nil {
			if *req.InstructorID == 0 {
				instructorID = nil
			} else {
				instructorID = req.InstructorID
			}
		}
	}
	templateDataStr := string(series.TemplateData)
	if len(req.TemplateData) > 0 {
		var td seriesTemplateData
		if err := json.Unmarshal(req.TemplateData, &td); err != nil {
			writeError(w, "invalid template_data: "+err.Error(), http.StatusBadRequest)
			return
		}
		templateDataStr = string(req.TemplateData)
	}
	_, err := db.Exec(`UPDATE event_series
		SET title=?, description=?, default_location_id=?, default_start_time=?, default_end_time=?,
		    organization_id=?, musician_id=?, instructor_id=?, updated_at=?, updated_by=?, template_data=?
		WHERE id=?`,
		req.Title, req.Description,
		optionalInt(req.DefaultLocationID),
		req.DefaultStartTime, req.DefaultEndTime,
		optionalInt(orgID), optionalInt(musicianID), optionalInt(instructorID),
		time.Now().Unix(), resolveDisplayName(callerID), templateDataStr,
		series.ID,
	)
	if err != nil {
		writeInternalError(w, err)
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
	d, err := time.ParseInLocation("2006-01-02", req.Date, berlinLoc)
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

	var result sql.Result
	if series.OrganizationID != nil {
		result, err = db.Exec(`INSERT INTO events
			(title, description, start_time, end_time, location_id, organization_id, series_id, is_published)
			VALUES (?,?,?,?,?,?,?,1)`,
			series.Title, "", startEpoch, endEpoch,
			optionalInt(series.DefaultLocationID), *series.OrganizationID, series.ID,
		)
	} else {
		result, err = db.Exec(`INSERT INTO events
			(title, description, start_time, end_time, location_id, series_id, is_published)
			VALUES (?,?,?,?,?,?,1)`,
			series.Title, "", startEpoch, endEpoch,
			optionalInt(series.DefaultLocationID), series.ID,
		)
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	evID64, _ := result.LastInsertId()
	evID := int(evID64)
	if series.MusicianID != nil {
		db.Exec("INSERT OR IGNORE INTO event_musicians (event_id, musician_id) VALUES (?,?)", evID, *series.MusicianID)
	}
	if series.InstructorID != nil {
		db.Exec("INSERT OR IGNORE INTO event_instructors (event_id, instructor_id) VALUES (?,?)", evID, *series.InstructorID)
	}
	if err := applySeriesTemplate(db, evID, parseSeriesTemplateData(series.TemplateData)); err != nil {
		writeInternalError(w, err)
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
		writeInternalError(w, err)
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
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/series-by-token/{token}
func getSeriesByToken(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	row := db.QueryRow("SELECT "+seriesSelectCols+" FROM event_series WHERE invite_token=?", tok)
	s, err := scanSeries(row)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	events, err := loadSeriesEvents(s.ID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	s.Events = events
	// Strip the token from the public response
	s.InviteToken = ""
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

// PATCH /api/v1/series-by-token/{token}/events/{eventID}
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
		writeInternalError(w, err)
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
	t := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, berlinLoc)
	return t.Unix()
}

// POST /api/v1/series/{id}/descriptions
// Bulk-updates the description of events belonging to this series.
// Body: {"updates": [{"event_id": 1, "description": "..."}, ...]}
func updateSeriesDescriptions(w http.ResponseWriter, r *http.Request) {
	series, ok := checkSeriesAccess(w, r)
	if !ok {
		return
	}
	var req struct {
		Updates []struct {
			EventID     int    `json:"event_id"`
			Description string `json:"description"`
		} `json:"updates"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	for _, u := range req.Updates {
		// Verify the event belongs to this series before updating.
		var sid sql.NullInt64
		if err := db.QueryRow("SELECT series_id FROM events WHERE id=?", u.EventID).Scan(&sid); err != nil {
			continue
		}
		if !sid.Valid || int(sid.Int64) != series.ID {
			continue
		}
		db.Exec("UPDATE events SET description=? WHERE id=?", u.Description, u.EventID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/series/{id}/assign-events
// Assigns existing events to this series. Per-event org check: only events
// whose organization_id matches the series org (or that are orphaned) are
// assigned; mismatches are silently skipped.
func assignSeriesEvents(w http.ResponseWriter, r *http.Request) {
	series, ok := checkSeriesAccess(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs []int `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		writeError(w, "ids required", http.StatusBadRequest)
		return
	}
	for _, id := range req.IDs {
		var evOrgID *int
		var tmp sql.NullInt64
		if err := db.QueryRow("SELECT organization_id FROM events WHERE id=?", id).Scan(&tmp); err != nil {
			continue
		}
		if tmp.Valid {
			v := int(tmp.Int64)
			evOrgID = &v
		}
		if evOrgID != nil && series.OrganizationID != nil && *evOrgID != *series.OrganizationID {
			continue // org mismatch — skip silently
		}
		db.Exec("UPDATE events SET series_id=? WHERE id=?", series.ID, id)
		// Propagate series org to event, matching the behaviour of addSeriesDate for new dates.
		if series.OrganizationID != nil {
			db.Exec("UPDATE events SET organization_id=? WHERE id=?", *series.OrganizationID, id)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/series/{id}/events — list events belonging to this series (#727).
func getSeriesEvents(w http.ResponseWriter, r *http.Request) {
	series, ok := checkSeriesAccess(w, r)
	if !ok {
		return
	}
	events, err := loadSeriesEvents(series.ID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, events)
}

// PUT /api/v1/series/{id}/events/{event_id} — add one event to the series (#727).
// Single-item counterpart to assign-events: rejects (rather than silently
// skipping) an event whose organization_id conflicts with the series org.
func addSeriesEvent(w http.ResponseWriter, r *http.Request) {
	series, ok := checkSeriesAccess(w, r)
	if !ok {
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("event_id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	var evOrgID sql.NullInt64
	if err := db.QueryRow("SELECT organization_id FROM events WHERE id=?", eventID).Scan(&evOrgID); err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}
	if evOrgID.Valid && series.OrganizationID != nil && evOrgID.Int64 != int64(*series.OrganizationID) {
		writeError(w, "event organization does not match series organization", http.StatusConflict)
		return
	}
	db.Exec("UPDATE events SET series_id=? WHERE id=?", series.ID, eventID)
	if series.OrganizationID != nil {
		db.Exec("UPDATE events SET organization_id=? WHERE id=?", *series.OrganizationID, eventID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/series/{id}/events/{event_id} — remove one event from the series (#727).
// Consolidates the existing asymmetric assign-events/remove-from-series pair
// into a single consistent location; does not touch organization_id.
func removeSeriesEvent(w http.ResponseWriter, r *http.Request) {
	series, ok := checkSeriesAccess(w, r)
	if !ok {
		return
	}
	eventID, err := strconv.Atoi(r.PathValue("event_id"))
	if err != nil {
		writeError(w, "invalid event id", http.StatusBadRequest)
		return
	}
	db.Exec("UPDATE events SET series_id=NULL WHERE id=? AND series_id=?", eventID, series.ID)
	w.WriteHeader(http.StatusNoContent)
}
