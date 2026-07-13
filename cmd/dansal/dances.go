package main

import (
	"encoding/json"
	"net/http"
)

type Dance struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GET /api/v1/dances
func getDances(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, name FROM dances ORDER BY name")
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()
	dances := []Dance{}
	for rows.Next() {
		var d Dance
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
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
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	var d Dance
	if err := db.QueryRow("INSERT INTO dances (name, created_by_id) VALUES (?, ?) RETURNING id, name", req.Name, callerID).Scan(&d.ID, &d.Name); err != nil {
		writeError(w, "Failed to create dance", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(d)
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
