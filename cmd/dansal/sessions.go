package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

type Session struct {
	ID          int    `json:"id"`
	UserAgent   string `json:"user_agent,omitempty"`
	IP          string `json:"ip,omitempty"`
	Fingerprint bool   `json:"fingerprint"`
	CreatedAt   string `json:"created_at"`
	LastSeenAt  string `json:"last_seen_at,omitempty"`
	ExpiresAt   string `json:"expires_at"`
	Current     bool   `json:"current"`
}

// GET /api/v1/sessions — list active sessions for the authenticated user.
func getSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	userID, _ := callerFromRequest(r)
	currentSessionID, _ := strconv.Atoi(r.Header.Get("X-Session-ID"))

	rows, err := db.Query(`
		SELECT id,
		       COALESCE(user_agent,''),
		       COALESCE(ip,''),
		       CASE WHEN fingerprint IS NOT NULL AND fingerprint != '' THEN 1 ELSE 0 END,
		       created_at,
		       COALESCE(last_seen_at,''),
		       expires_at
		FROM tokens
		WHERE user_id = ? AND expires_at > strftime('%s','now')
		ORDER BY COALESCE(last_seen_at, created_at) DESC`, userID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		var s Session
		var hasFP int
		if err := rows.Scan(&s.ID, &s.UserAgent, &s.IP, &hasFP, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt); err != nil {
			writeInternalError(w, err)
			return
		}
		s.LastSeenAt = epochStrToRFC3339(s.LastSeenAt)
		s.ExpiresAt = epochStrToRFC3339(s.ExpiresAt)
		s.Fingerprint = hasFP == 1
		s.Current = s.ID == currentSessionID
		sessions = append(sessions, s)
	}
	json.NewEncoder(w).Encode(sessions)
}

// DELETE /api/v1/sessions/{id} — revoke a specific session.
// Users may revoke their own sessions; admins may revoke any session.
func deleteSession(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	sessionID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, "Invalid session ID", http.StatusBadRequest)
		return
	}

	var ownerID int
	var token string
	err = db.QueryRow("SELECT user_id, token FROM tokens WHERE id=?", sessionID).Scan(&ownerID, &token)
	if err == sql.ErrNoRows {
		writeError(w, "Session not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}

	if ownerID != callerID && callerRole != RoleAdmin {
		writeError(w, fmt.Sprintf("Forbidden: session belongs to user %d", ownerID), http.StatusForbidden)
		return
	}

	db.Exec("DELETE FROM tokens WHERE id=?", sessionID)
	credentials.invalidate(token)
	lastSeenMu.Lock()
	delete(lastSeenCache, token)
	lastSeenMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
