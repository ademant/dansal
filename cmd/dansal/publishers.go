package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type CreatePublisherRequest struct {
	Name  string `json:"name"`
	OrgID *int   `json:"org_id"`
}

type PublisherCreatedResponse struct {
	UserID int    `json:"user_id"`
	Name   string `json:"name"`
	KeyID  int    `json:"key_id"`
	APIKey string `json:"api_key"`
	OrgID  *int   `json:"org_id,omitempty"`
}

// POST /api/v1/publishers — create a publisher service account + API key atomically.
// Admin and user roles may create publishers; user must be a member of the specified org.
func createPublisher(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)
	if callerRole != RoleAdmin && callerRole != RoleUser {
		writeError(w, "Forbidden: only admin or user may create publishers", http.StatusForbidden)
		return
	}

	var req CreatePublisherRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.OrgID == nil {
		writeError(w, "org_id is required", http.StatusBadRequest)
		return
	}

	// Non-admin must be a member of the specified org.
	if callerRole != RoleAdmin && !isOrgMember(callerID, *req.OrgID) {
		writeError(w, "Forbidden: you must be a member of the specified organisation", http.StatusForbidden)
		return
	}

	// Verify org exists.
	var exists int
	if err := db.QueryRow("SELECT COUNT(*) FROM organizations WHERE id=?", *req.OrgID).Scan(&exists); err != nil || exists == 0 {
		writeError(w, "Organisation not found", http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Create the publisher user record (no email, no password, no passkeys).
	result, err := tx.Exec(
		"INSERT INTO users (role, display_name, password_hash) VALUES (?, ?, '')",
		RolePublisher, req.Name,
	)
	if err != nil {
		writeError(w, "failed to create publisher account", http.StatusInternalServerError)
		return
	}
	userID, _ := result.LastInsertId()

	// Assign to org.
	if _, err := tx.Exec(
		"INSERT INTO organization_members (organization_id, user_id) VALUES (?, ?)",
		*req.OrgID, userID,
	); err != nil {
		writeError(w, "failed to assign publisher to org", http.StatusInternalServerError)
		return
	}

	// Generate and store API key.
	key, err := generateAPIKey()
	if err != nil {
		writeError(w, "failed to generate API key", http.StatusInternalServerError)
		return
	}
	keyName := req.Name
	if keyName == "" {
		keyName = fmt.Sprintf("publisher#%d", userID)
	}
	var keyID int
	var createdAt string
	if err := tx.QueryRow(
		"INSERT INTO api_keys (user_id, name, api_key) VALUES (?, ?, ?) RETURNING id, created_at",
		userID, keyName, key,
	).Scan(&keyID, &createdAt); err != nil {
		writeError(w, "failed to create API key", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, "db commit error", http.StatusInternalServerError)
		return
	}

	displayName := req.Name
	if displayName == "" {
		displayName = fmt.Sprintf("publisher#%d", userID)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(PublisherCreatedResponse{
		UserID: int(userID),
		Name:   displayName,
		KeyID:  keyID,
		APIKey: key,
		OrgID:  req.OrgID,
	})
}

// POST /api/v1/publishers/{id}/regenerate-key — rotate the API key for a publisher.
// Admin may act on any publisher; user may only act on publishers in their org.
func regeneratePublisherKey(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	targetID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Verify target is a publisher.
	var targetRole string
	if err := db.QueryRow("SELECT role FROM users WHERE id=?", targetID).Scan(&targetRole); err != nil {
		writeError(w, "publisher not found", http.StatusNotFound)
		return
	}
	if targetRole != RolePublisher {
		writeError(w, "target is not a publisher", http.StatusBadRequest)
		return
	}

	// Non-admins must share an org with the publisher.
	if callerRole != RoleAdmin {
		if callerRole != RoleUser {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		var shared int
		db.QueryRow(`
			SELECT COUNT(*) FROM organization_members om1
			JOIN organization_members om2 ON om1.organization_id = om2.organization_id
			WHERE om1.user_id = ? AND om2.user_id = ?`, callerID, targetID).Scan(&shared)
		if shared == 0 {
			writeError(w, "Forbidden: publisher is not in your organisation", http.StatusForbidden)
			return
		}
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Invalidate old keys.
	rows, _ := tx.Query("SELECT api_key FROM api_keys WHERE user_id=?", targetID)
	var oldKeys []string
	for rows.Next() {
		var k string
		rows.Scan(&k)
		oldKeys = append(oldKeys, k)
	}
	rows.Close()
	tx.Exec("DELETE FROM api_keys WHERE user_id=?", targetID)

	// Generate new key.
	newKey, err := generateAPIKey()
	if err != nil {
		writeError(w, "failed to generate key", http.StatusInternalServerError)
		return
	}
	var keyName string
	db.QueryRow("SELECT COALESCE(display_name, 'publisher#'+CAST(id AS TEXT)) FROM users WHERE id=?", targetID).Scan(&keyName)
	if keyName == "" {
		keyName = fmt.Sprintf("publisher#%d", targetID)
	}
	var keyID int
	if err := tx.QueryRow(
		"INSERT INTO api_keys (user_id, name, api_key) VALUES (?, ?, ?) RETURNING id",
		targetID, keyName, newKey,
	).Scan(&keyID); err != nil {
		writeError(w, "failed to store new key", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	for _, k := range oldKeys {
		credentials.invalidate(k)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"key_id":  keyID,
		"api_key": newKey,
	})
}

// DELETE /api/v1/publishers/{id} — delete a publisher service account.
// Admin may act on any publisher; user may only act on publishers in their org.
func deletePublisher(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	targetID, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var targetRole string
	if err := db.QueryRow("SELECT role FROM users WHERE id=?", targetID).Scan(&targetRole); err != nil {
		writeError(w, "publisher not found", http.StatusNotFound)
		return
	}
	if targetRole != RolePublisher {
		writeError(w, "target is not a publisher", http.StatusBadRequest)
		return
	}

	if callerRole != RoleAdmin {
		if callerRole != RoleUser {
			writeError(w, "Forbidden", http.StatusForbidden)
			return
		}
		var shared int
		db.QueryRow(`
			SELECT COUNT(*) FROM organization_members om1
			JOIN organization_members om2 ON om1.organization_id = om2.organization_id
			WHERE om1.user_id = ? AND om2.user_id = ?`, callerID, targetID).Scan(&shared)
		if shared == 0 {
			writeError(w, "Forbidden: publisher is not in your organisation", http.StatusForbidden)
			return
		}
	}

	rows, _ := db.Query("SELECT api_key FROM api_keys WHERE user_id=?", targetID)
	var oldKeys []string
	for rows.Next() {
		var k string
		rows.Scan(&k)
		oldKeys = append(oldKeys, k)
	}
	rows.Close()

	if _, err := db.Exec("DELETE FROM users WHERE id=?", targetID); err != nil {
		writeError(w, "failed to delete publisher", http.StatusInternalServerError)
		return
	}

	for _, k := range oldKeys {
		credentials.invalidate(k)
	}

	w.WriteHeader(http.StatusNoContent)
}
