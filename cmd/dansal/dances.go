package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type Dance struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// GET /api/v1/dances
func getDances(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, name, description FROM dances ORDER BY name")
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()
	dances := []Dance{}
	for rows.Next() {
		var d Dance
		if err := rows.Scan(&d.ID, &d.Name, &d.Description); err != nil {
			writeInternalError(w, err)
			return
		}
		dances = append(dances, d)
	}
	writeJSON(w, dances)
}

// POST /api/v1/dances
func createDance(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin {
		writeError(w, "Forbidden: only admins may create dances", http.StatusForbidden)
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	var d Dance
	if err := db.QueryRow("INSERT INTO dances (name, description, created_by_id) VALUES (?, ?, ?) RETURNING id, name, description", req.Name, req.Description, callerID).Scan(&d.ID, &d.Name, &d.Description); err != nil {
		writeError(w, "Failed to create dance", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", fmt.Sprintf("/api/v1/dances/%d", d.ID))
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(d)
}

// PUT /api/v1/dances/{id} — full replace of name+description (#1290).
func updateDance(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin {
		writeError(w, "Forbidden: only admins may edit dances", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	result, err := db.Exec(
		"UPDATE dances SET name=?, description=?, updated_at=strftime('%s','now'), updated_by=? WHERE id=?",
		req.Name, req.Description, resolveDisplayName(callerID), id,
	)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Dance not found", http.StatusNotFound)
		return
	}
	var d Dance
	if err := db.QueryRow("SELECT id, name, description FROM dances WHERE id = ?", id).Scan(&d.ID, &d.Name, &d.Description); err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, d)
}

// DELETE /api/v1/dances/{id}
func deleteDance(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-User-Role") != RoleAdmin {
		writeError(w, "Forbidden: only admins may delete dances", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	result, err := db.Exec("DELETE FROM dances WHERE id = ?", id)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeError(w, "Dance not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
