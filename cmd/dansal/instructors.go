package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
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
	CreatedAt string `json:"created_at"`

	FutureEventCount int `json:"future_event_count,omitempty"`
	PastEventCount   int `json:"past_event_count,omitempty"`
}

type InstructorRequest struct {
	Name    string `json:"name"`
	Bio     string `json:"bio"`
	Website string `json:"website"`
	Email   string `json:"email"`
}

const instructorCols = "id, name, COALESCE(bio,''), COALESCE(website,''), COALESCE(email,''), created_at"

// scanInstructor scans an instructorCols row into an Instructor. Extra
// destination pointers (e.g. for appended event-count columns) can be passed via extra.
func scanInstructor(row interface{ Scan(...any) error }, extra ...any) (Instructor, error) {
	var i Instructor
	dest := []any{&i.ID, &i.Name, &i.Bio, &i.Website, &i.Email, &i.CreatedAt}
	err := row.Scan(append(dest, extra...)...)
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
			SELECT ei.instructor_id,
				SUM(CASE WHEN e.start_time > strftime('%s','now') AND e.is_published=1 THEN 1 ELSE 0 END) AS future_count,
				SUM(CASE WHEN e.start_time <= strftime('%s','now') AND e.is_published=1 THEN 1 ELSE 0 END) AS past_count
			FROM event_instructors ei JOIN events e ON e.id = ei.event_id
			GROUP BY ei.instructor_id
		) ec ON ec.instructor_id = instructors.id`
	}
	var args []any

	if name := q.Get("name"); name != "" {
		query += " WHERE name LIKE ?"
		args = append(args, "%"+name+"%")
	}
	query += " ORDER BY name"

	rows, err := db.Query(query, args...)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
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
			writeError(w, err.Error(), http.StatusInternalServerError)
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
		writeError(w, err.Error(), http.StatusInternalServerError)
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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		if errors.As(err, new(*http.MaxBytesError)) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, err.Error(), status)
		return
	}
	var req InstructorRequest
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	inst, err := scanInstructor(db.QueryRow(
		"INSERT INTO instructors (name, bio, website, email, created_by_id) VALUES (?,?,?,?,?) RETURNING "+instructorCols,
		strings.TrimSpace(req.Name), req.Bio, req.Website, req.Email, callerID,
	))
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(inst)
}

// PUT /api/v1/instructors/{id}
func updateInstructor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")

	if callerRole != RoleAdmin {
		var createdBy sql.NullInt64
		if err := db.QueryRow("SELECT created_by_id FROM instructors WHERE id = ?", id).Scan(&createdBy); err == sql.ErrNoRows {
			writeError(w, "Instructor not found", http.StatusNotFound)
			return
		} else if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !createdBy.Valid || int(createdBy.Int64) != callerID {
			writeError(w, "Forbidden: you can only edit instructors you created", http.StatusForbidden)
			return
		}
	}

	var req InstructorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := db.Exec(
		"UPDATE instructors SET name=?, bio=?, website=?, email=? WHERE id=?",
		strings.TrimSpace(req.Name), req.Bio, req.Website, req.Email, id,
	)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, "Instructor not found", http.StatusNotFound)
		return
	}
	inst, err := scanInstructor(db.QueryRow("SELECT "+instructorCols+" FROM instructors WHERE id=?", id))
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(inst)
}

// DELETE /api/v1/instructors/{id}
func deleteInstructor(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")

	if callerRole != RoleAdmin {
		var createdBy sql.NullInt64
		if err := db.QueryRow("SELECT created_by_id FROM instructors WHERE id = ?", id).Scan(&createdBy); err == sql.ErrNoRows {
			writeError(w, "Instructor not found", http.StatusNotFound)
			return
		} else if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !createdBy.Valid || int(createdBy.Int64) != callerID {
			writeError(w, "Forbidden: you can only delete instructors you created", http.StatusForbidden)
			return
		}
	}

	result, err := db.Exec("DELETE FROM instructors WHERE id=?", id)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
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
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "Invalid event ID", http.StatusBadRequest)
		return
	}
	instructors, err := fetchEventInstructors(eventID)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
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
	eventID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "Invalid event ID", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}

	var ids []int
	if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
		writeError(w, "expected JSON array of instructor IDs", http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	tx.Exec("DELETE FROM event_instructors WHERE event_id=?", eventID)
	for _, id := range ids {
		tx.Exec("INSERT OR IGNORE INTO event_instructors (event_id, instructor_id) VALUES (?,?)", eventID, id)
	}
	if err := tx.Commit(); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	instructors, err := fetchEventInstructors(eventID)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(instructors)
}
