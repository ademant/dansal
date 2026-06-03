package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type APIKey struct {
	ID        int    `json:"id"`
	UserID    int    `json:"user_id"`
	Name      string `json:"name"`
	Key       string `json:"key,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	CreatedAt string `json:"created_at"`
}

type CreateAPIKeyRequest struct {
	Name      string `json:"name"`
	UserID    *int   `json:"user_id,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"` // RFC3339, optional
}

func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ak_" + base64.URLEncoding.EncodeToString(b), nil
}

// validateAPIKey checks an API key and returns the associated user.
// Results are cached for up to credCacheTTL; the cache is invalidated on deletion.
func validateAPIKey(key string) (int, string, error) {
	if userID, role, _, ok := credentials.get(key); ok {
		return userID, role, nil
	}

	var userID int
	var userRole string
	var expiresAt sql.NullString

	err := db.QueryRow(
		"SELECT users.id, users.role, api_keys.expires_at FROM api_keys JOIN users ON api_keys.user_id = users.id WHERE api_keys.api_key = ? AND users.disabled = 0",
		key,
	).Scan(&userID, &userRole, &expiresAt)

	if err == sql.ErrNoRows {
		return 0, "", fmt.Errorf("invalid api key")
	}
	if err != nil {
		return 0, "", err
	}

	var expTime time.Time
	if expiresAt.Valid && expiresAt.String != "" {
		expTime, err = parseTokenExpiration(expiresAt.String)
		if err != nil || time.Now().After(expTime) {
			return 0, "", fmt.Errorf("api key expired")
		}
	}

	credentials.set(key, userID, userRole, 0, expTime) // tokenID 0 = API key, expTime zero → TTL cap only
	return userID, userRole, nil
}

func listAPIKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, callerRole := callerFromRequest(r)

	var rows *sql.Rows
	var err error

	if callerRole == RoleAdmin {
		rows, err = db.Query("SELECT id, user_id, name, expires_at, created_at FROM api_keys ORDER BY id")
	} else {
		// Non-admins see their own keys plus keys for publishers in the same org(s).
		rows, err = db.Query(`
			SELECT ak.id, ak.user_id, ak.name, ak.expires_at, ak.created_at FROM api_keys ak
			WHERE ak.user_id = ?
			UNION
			SELECT ak.id, ak.user_id, ak.name, ak.expires_at, ak.created_at FROM api_keys ak
			JOIN users u ON u.id = ak.user_id AND u.role = 'publisher'
			JOIN organization_members om1 ON om1.user_id = ak.user_id
			JOIN organization_members om2 ON om2.organization_id = om1.organization_id AND om2.user_id = ?
			ORDER BY id`, callerID, callerID)
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to fetch API keys"})
		return
	}
	defer rows.Close()

	keys := []APIKey{}
	for rows.Next() {
		var k APIKey
		var exp sql.NullString
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &exp, &k.CreatedAt); err != nil {
			continue
		}
		if exp.Valid {
			k.ExpiresAt = epochStrToRFC3339(exp.String)
		}
		keys = append(keys, k)
	}

	json.NewEncoder(w).Encode(keys)
}

func createAPIKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, callerRole := callerFromRequest(r)

	var req CreateAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request body"})
		return
	}

	if req.Name == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "name is required"})
		return
	}

	var expiresAt *int64
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "expires_at must be RFC3339"})
			return
		}
		epoch := t.Unix()
		expiresAt = &epoch
	}

	targetUserID := callerID
	if req.UserID != nil {
		if callerRole != RoleAdmin && *req.UserID != callerID {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "Only admins can create API keys for other users"})
			return
		}
		targetUserID = *req.UserID
	}

	key, err := generateAPIKey()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to generate API key"})
		return
	}

	var id int
	var createdAt string
	err = db.QueryRow(
		"INSERT INTO api_keys (user_id, name, api_key, expires_at) VALUES (?, ?, ?, ?) RETURNING id, created_at",
		targetUserID, req.Name, key, expiresAt,
	).Scan(&id, &createdAt)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to create API key"})
		return
	}

	resp := APIKey{
		ID:        id,
		UserID:    targetUserID,
		Name:      req.Name,
		Key:       key,
		CreatedAt: createdAt,
	}
	if req.ExpiresAt != "" {
		resp.ExpiresAt = req.ExpiresAt
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, callerRole := callerFromRequest(r)

	keyID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid API key ID"})
		return
	}

	var ownerID int
	var keyValue string
	err = db.QueryRow("SELECT user_id, api_key FROM api_keys WHERE id = ?", keyID).Scan(&ownerID, &keyValue)
	if err == sql.ErrNoRows {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "API key not found"})
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Internal server error"})
		return
	}

	if callerRole != RoleAdmin && callerID != ownerID {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "Cannot delete another user's API key"})
		return
	}

	if _, err = db.Exec("DELETE FROM api_keys WHERE id = ?", keyID); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to delete API key"})
		return
	}

	credentials.invalidate(keyValue)
	w.WriteHeader(http.StatusNoContent)
}
