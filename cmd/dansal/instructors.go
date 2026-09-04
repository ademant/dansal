package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type Instructor struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Bio       string `json:"bio,omitempty"`
	Website   string `json:"website,omitempty"`
	Email     string `json:"email,omitempty"`
	Mastodon  string `json:"mastodon,omitempty"`
	Instagram string `json:"instagram,omitempty"`
	Facebook  string `json:"facebook,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`

	AvatarURL string `json:"avatar_url,omitempty"`

	FutureEventCount int `json:"future_event_count,omitempty"`
	PastEventCount   int `json:"past_event_count,omitempty"`
}

type InstructorRequest struct {
	Name      string `json:"name"`
	Bio       string `json:"bio"`
	Website   string `json:"website"`
	Email     string `json:"email"`
	Mastodon  string `json:"mastodon"`
	Instagram string `json:"instagram"`
	Facebook  string `json:"facebook"`
}

// InstructorMergePatchRequest is the body accepted by PATCH
// /api/v1/instructors/{id} (Content-Type: application/merge-patch+json —
// RFC 7396). Every field is a pointer: an omitted key leaves the existing
// value unchanged; a present key sets it (an explicit "" clears a field).
type InstructorMergePatchRequest struct {
	Name      *string `json:"name,omitempty"`
	Bio       *string `json:"bio,omitempty"`
	Website   *string `json:"website,omitempty"`
	Email     *string `json:"email,omitempty"`
	Mastodon  *string `json:"mastodon,omitempty"`
	Instagram *string `json:"instagram,omitempty"`
	Facebook  *string `json:"facebook,omitempty"`
}

const instructorCols = "id, name, COALESCE(bio,''), COALESCE(website,''), COALESCE(email,''), COALESCE(mastodon,''), COALESCE(instagram,''), COALESCE(facebook,''), created_at, COALESCE(updated_at,0), COALESCE(updated_by,'')"

// scanInstructor scans an instructorCols row into an Instructor. Extra
// destination pointers (e.g. for appended event-count columns) can be passed via extra.
func scanInstructor(row interface{ Scan(...any) error }, extra ...any) (Instructor, error) {
	var i Instructor
	dest := []any{&i.ID, &i.Name, &i.Bio, &i.Website, &i.Email, &i.Mastodon, &i.Instagram, &i.Facebook, &i.CreatedAt, &i.UpdatedAt, &i.UpdatedBy}
	err := row.Scan(append(dest, extra...)...)
	if err == nil {
		i.AvatarURL = instructorAvatars.url(i.ID)
	}
	return i, err
}

// GET /api/v1/instructors
func getInstructors(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	withCounts := q.Get("with_event_counts") == "true"

	query := "SELECT " + instructorCols
	if withCounts {
		query += `, COALESCE(ec.future_count,0), COALESCE(ec.past_count,0)`
	}
	query += " FROM instructors"
	if withCounts {
		query += ` LEFT JOIN (
			SELECT instructor_id,
				SUM(CASE WHEN start_time > strftime('%s','now') AND is_published=1 THEN 1 ELSE 0 END) AS future_count,
				SUM(CASE WHEN start_time <= strftime('%s','now') AND is_published=1 THEN 1 ELSE 0 END) AS past_count
			FROM (
				SELECT DISTINCT ei.instructor_id AS instructor_id, e.id AS event_id, e.start_time AS start_time, e.is_published AS is_published
				FROM event_instructors ei JOIN events e ON e.id = ei.event_id
				UNION
				SELECT DISTINCT t.instructor_id AS instructor_id, e.id AS event_id, e.start_time AS start_time, e.is_published AS is_published
				FROM timetable_entries t JOIN events e ON e.id = t.event_id
				WHERE t.instructor_id IS NOT NULL
			)
			GROUP BY instructor_id
		) ec ON ec.instructor_id = instructors.id`
	}
	var args []any
	where := false
	addWhere := newWhereAppender(&query, &where, &args)
	if name := q.Get("name"); name != "" {
		addWhere(`name LIKE ? ESCAPE '\'`, "%"+escapeLike(name)+"%")
	}
	if orgIDStr := q.Get("organization_id"); orgIDStr != "" {
		if orgID, err := strconv.Atoi(orgIDStr); err == nil {
			addWhere(`id IN (
				SELECT DISTINCT ei.instructor_id FROM event_instructors ei
				JOIN events e ON e.id = ei.event_id
				WHERE e.organization_id = ? AND e.is_published = 1
			)`, orgID)
		}
	}
	applyListPagination(r, "name ASC", &query, &args)

	rows, err := db.Query(query, args...)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()
	instructors := []Instructor{}
	for rows.Next() {
		var extra []any
		var futureCount, pastCount int
		if withCounts {
			extra = append(extra, &futureCount, &pastCount)
		}
		inst, err := scanInstructor(rows, extra...)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		if withCounts {
			inst.FutureEventCount = futureCount
			inst.PastEventCount = pastCount
		}
		instructors = append(instructors, inst)
	}
	writeJSON(w, instructors)
}

// GET /api/v1/instructors/{id}
func getInstructor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inst, err := scanInstructor(db.QueryRow("SELECT "+instructorCols+" FROM instructors WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, "Instructor not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, inst)
}

// POST /api/v1/instructors
func createInstructor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	body, ok := readBodyOrError(w, r)
	if !ok {
		return
	}
	var req InstructorRequest
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	inst, err := scanInstructor(db.QueryRow(
		"INSERT INTO instructors (name, bio, website, email, mastodon, instagram, facebook, created_by_id) VALUES (?,?,?,?,?,?,?,?) RETURNING "+instructorCols,
		strings.TrimSpace(req.Name), req.Bio, req.Website, req.Email, req.Mastodon, req.Instagram, req.Facebook, callerID,
	))
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/instructors/%d", inst.ID))
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(inst)
}

// PUT /api/v1/instructors/{id}
// checkInstructorOwnership enforces "non-admin caller must be the instructor's
// creator" — updateInstructor, patchInstructor, and deleteInstructor all need
// this identically, differing only in whether the error says "edit" or
// "delete". Writes the appropriate 404/403 and returns false on failure,
// same convention as checkLocationWriteAccess in locations.go.
func checkInstructorOwnership(w http.ResponseWriter, callerID int, callerRole, id, action string) bool {
	if callerRole == RoleAdmin {
		return true
	}
	var createdBy sql.NullInt64
	if err := db.QueryRow("SELECT created_by_id FROM instructors WHERE id = ?", id).Scan(&createdBy); err == sql.ErrNoRows {
		writeError(w, "Instructor not found", http.StatusNotFound)
		return false
	} else if err != nil {
		writeInternalError(w, err)
		return false
	}
	if !createdBy.Valid || int(createdBy.Int64) != callerID {
		writeError(w, "Forbidden: you can only "+action+" instructors you created", http.StatusForbidden)
		return false
	}
	return true
}

func updateInstructor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")

	if !checkInstructorOwnership(w, callerID, callerRole, id, "edit") {
		return
	}

	var req InstructorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := db.Exec(
		"UPDATE instructors SET name=?, bio=?, website=?, email=?, mastodon=?, instagram=?, facebook=?, updated_at=strftime('%s','now'), updated_by=? WHERE id=?",
		strings.TrimSpace(req.Name), req.Bio, req.Website, req.Email, req.Mastodon, req.Instagram, req.Facebook, resolveDisplayName(callerID), id,
	)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, "Instructor not found", http.StatusNotFound)
		return
	}
	inst, err := scanInstructor(db.QueryRow("SELECT "+instructorCols+" FROM instructors WHERE id=?", id))
	if err != nil {
		writeInternalError(w, err)
		return
	}
	json.NewEncoder(w).Encode(inst)
}

// PATCH /api/v1/instructors/{id} - partial update (RFC 7396 JSON Merge Patch)
func patchInstructor(w http.ResponseWriter, r *http.Request) {
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

	if !checkInstructorOwnership(w, callerID, callerRole, id, "edit") {
		return
	}

	inst, err := scanInstructor(db.QueryRow("SELECT "+instructorCols+" FROM instructors WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, "Instructor not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}

	var req InstructorMergePatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			writeError(w, "name is required", http.StatusBadRequest)
			return
		}
		inst.Name = strings.TrimSpace(*req.Name)
	}
	if req.Bio != nil {
		inst.Bio = *req.Bio
	}
	if req.Website != nil {
		inst.Website = *req.Website
	}
	if req.Email != nil {
		inst.Email = *req.Email
	}
	if req.Mastodon != nil {
		inst.Mastodon = *req.Mastodon
	}
	if req.Instagram != nil {
		inst.Instagram = *req.Instagram
	}
	if req.Facebook != nil {
		inst.Facebook = *req.Facebook
	}

	result, err := db.Exec(
		"UPDATE instructors SET name=?, bio=?, website=?, email=?, mastodon=?, instagram=?, facebook=?, updated_at=strftime('%s','now'), updated_by=? WHERE id=?",
		inst.Name, inst.Bio, inst.Website, inst.Email, inst.Mastodon, inst.Instagram, inst.Facebook, resolveDisplayName(callerID), id,
	)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, "Instructor not found", http.StatusNotFound)
		return
	}
	updated, err := scanInstructor(db.QueryRow("SELECT "+instructorCols+" FROM instructors WHERE id=?", id))
	if err != nil {
		writeInternalError(w, err)
		return
	}
	json.NewEncoder(w).Encode(updated)
}

// DELETE /api/v1/instructors/{id}
func deleteInstructor(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")

	if !checkInstructorOwnership(w, callerID, callerRole, id, "delete") {
		return
	}

	result, err := db.Exec("DELETE FROM instructors WHERE id=?", id)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, "Instructor not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/events/{id}/instructors
func getEventInstructors(w http.ResponseWriter, r *http.Request) {
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "Invalid event ID", http.StatusBadRequest)
		return
	}
	instructors, err := fetchEventInstructors(eventID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, instructors)
}

func fetchEventInstructors(eventID int) ([]Instructor, error) {
	rows, err := db.Query(
		"SELECT "+instructorCols+" FROM instructors WHERE id IN (SELECT instructor_id FROM event_instructors WHERE event_id=?) ORDER BY name",
		eventID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Instructor{}
	for rows.Next() {
		inst, err := scanInstructor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return out, nil
}

// PUT /api/v1/events/{id}/instructors — replace instructor list atomically
func setEventInstructors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	callerID, userRole := callerFromRequest(r)
	if userRole != RoleAdmin && userRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	eventID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "Invalid event ID", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}

	var ids []int
	if !decodeJSONBody(w, r, &ids) {
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer tx.Rollback()

	tx.Exec("DELETE FROM event_instructors WHERE event_id=?", eventID)
	for _, id := range ids {
		tx.Exec("INSERT OR IGNORE INTO event_instructors (event_id, instructor_id) VALUES (?,?)", eventID, id)
	}
	if err := tx.Commit(); err != nil {
		writeInternalError(w, err)
		return
	}

	instructors, err := fetchEventInstructors(eventID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	json.NewEncoder(w).Encode(instructors)
}
