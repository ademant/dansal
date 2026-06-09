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
}

type InstructorRequest struct {
	Name    string `json:"name"`
	Bio     string `json:"bio"`
	Website string `json:"website"`
	Email   string `json:"email"`
}

const instructorCols = "id, name, COALESCE(bio,''), COALESCE(website,''), COALESCE(email,''), created_at"

func scanInstructor(row interface{ Scan(...any) error }) (Instructor, error) {
	var i Instructor
	err := row.Scan(&i.ID, &i.Name, &i.Bio, &i.Website, &i.Email, &i.CreatedAt)
	return i, err
}

// GET /api/v1/instructors
func getInstructors(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := "SELECT " + instructorCols + " FROM instructors"
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
		inst, err := scanInstructor(rows)
		if err != nil {
			writeError(w, err.Error(), http.StatusInternalServerError)
			return
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
	_, callerRole := callerFromRequest(r)
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
		"INSERT INTO instructors (name, bio, website, email) VALUES (?,?,?,?) RETURNING "+instructorCols,
		strings.TrimSpace(req.Name), req.Bio, req.Website, req.Email,
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
	_, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
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
	_, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
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
