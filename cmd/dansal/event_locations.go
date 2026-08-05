package main

import (
	"database/sql"
	"net/http"
	"strconv"
)

// addEventExtraLocation handles PUT /api/v1/events/{id}/locations/{location_id}.
// Inserts the location into event_locations. If no primary location is set yet,
// also promotes it to primary (events.location_id).
func addEventExtraLocation(w http.ResponseWriter, r *http.Request) {
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
	locationID, err := strconv.Atoi(r.PathValue("location_id"))
	if err != nil {
		writeError(w, "invalid location_id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	var exists int
	db.QueryRow("SELECT COUNT(*) FROM locations WHERE id=?", locationID).Scan(&exists)
	if exists == 0 {
		writeError(w, "Location not found", http.StatusNotFound)
		return
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO event_locations (event_id, location_id) VALUES (?,?)", eventID, locationID); err != nil {
		writeInternalError(w, err)
		return
	}
	// If no primary location yet, promote this one.
	var current sql.NullInt64
	db.QueryRow("SELECT location_id FROM events WHERE id=?", eventID).Scan(&current)
	if !current.Valid {
		db.Exec("UPDATE events SET location_id=? WHERE id=?", locationID, eventID)
		syncEventLocationGeohash(eventID)
	}
	touchEvent(eventID, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// removeEventExtraLocation handles DELETE /api/v1/events/{id}/locations/{location_id}.
// Refuses to remove the primary location; use /primary to promote another first.
func removeEventExtraLocation(w http.ResponseWriter, r *http.Request) {
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
	locationID, err := strconv.Atoi(r.PathValue("location_id"))
	if err != nil {
		writeError(w, "invalid location_id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	var primary sql.NullInt64
	db.QueryRow("SELECT location_id FROM events WHERE id=?", eventID).Scan(&primary)
	if primary.Valid && int(primary.Int64) == locationID {
		writeError(w, "cannot remove primary location; promote another location to primary first", http.StatusConflict)
		return
	}
	db.Exec("DELETE FROM event_locations WHERE event_id=? AND location_id=?", eventID, locationID)
	touchEvent(eventID, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// setEventExtraLocationPrimary handles PUT /api/v1/events/{id}/locations/{location_id}/primary.
// Promotes an already-assigned location to primary (updates events.location_id).
func setEventExtraLocationPrimary(w http.ResponseWriter, r *http.Request) {
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
	locationID, err := strconv.Atoi(r.PathValue("location_id"))
	if err != nil {
		writeError(w, "invalid location_id", http.StatusBadRequest)
		return
	}
	if !timetableAuthCheck(w, userRole, callerID, eventID) {
		return
	}
	var cnt int
	db.QueryRow("SELECT COUNT(*) FROM event_locations WHERE event_id=? AND location_id=?", eventID, locationID).Scan(&cnt)
	if cnt == 0 {
		writeError(w, "location not assigned to this event", http.StatusNotFound)
		return
	}
	if _, err := db.Exec("UPDATE events SET location_id=? WHERE id=?", locationID, eventID); err != nil {
		writeInternalError(w, err)
		return
	}
	syncEventLocationGeohash(eventID)
	touchEvent(eventID, callerID)
	w.WriteHeader(http.StatusNoContent)
}
