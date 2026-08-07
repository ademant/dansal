package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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

// hashAPIKey returns the hex-encoded SHA-256 digest of an API key, which is
// what's stored in api_keys.api_key — the raw key is never persisted, only
// returned once in the HTTP response at creation/renewal time.
func hashAPIKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// validateAPIKey checks an API key and returns the associated user.
// Results are cached for up to credCacheTTL; the cache is invalidated on deletion.
func validateAPIKey(key string) (int, string, error) {
	if userID, role, _, ok := credentials.get(key, ""); ok { // API keys are never IP-pinned
		return userID, role, nil
	}

	var userID int
	var userRole string
	var expiresAt sql.NullString

	err := db.QueryRow(
		"SELECT users.id, users.role, api_keys.expires_at FROM api_keys JOIN users ON api_keys.user_id = users.id WHERE api_keys.api_key = ? AND users.disabled = 0",
		hashAPIKey(key),
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

	credentials.set(key, userID, userRole, 0, expTime, "") // tokenID 0 = API key, expTime zero → TTL cap only
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
		targetUserID, req.Name, hashAPIKey(key), expiresAt,
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

	keyID, err := intPathValue(r, "id")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid API key ID"})
		return
	}

	var ownerID int
	err = db.QueryRow("SELECT user_id FROM api_keys WHERE id = ?", keyID).Scan(&ownerID)
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

	credentials.pruneByUserID(ownerID)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/apikeys/renew — self-service rotation for a key nearing its
// expiry, authenticated by presenting that same key. Lets headless
// integrations (e.g. a WordPress plugin holding a publisher key) rotate
// without an admin/user session. Only keys with an expires_at set can be
// renewed, and only before they expire — an already-expired key needs a
// human to reissue it via regenerate-key.
func renewAPIKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	authHeader := r.Header.Get("Authorization")
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid authorization header format. Use 'Bearer <api_key>'"})
		return
	}
	presentedKey := parts[1]

	var keyID, userID int
	var name, createdAtStr string
	var expiresAt sql.NullString
	err := db.QueryRow(
		"SELECT api_keys.id, api_keys.user_id, api_keys.name, api_keys.created_at, api_keys.expires_at FROM api_keys JOIN users ON api_keys.user_id = users.id WHERE api_keys.api_key = ? AND users.disabled = 0",
		hashAPIKey(presentedKey),
	).Scan(&keyID, &userID, &name, &createdAtStr, &expiresAt)
	if err == sql.ErrNoRows {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid or expired credentials"})
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Internal server error"})
		return
	}
	if !expiresAt.Valid || expiresAt.String == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "This key has no expiry; renewal is not applicable"})
		return
	}

	oldExpiry, err := parseTokenExpiration(expiresAt.String)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Internal server error"})
		return
	}
	if time.Now().After(oldExpiry) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "This key has already expired; ask an admin or org member to issue a new one"})
		return
	}
	oldCreated, err := parseTokenExpiration(createdAtStr)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Internal server error"})
		return
	}

	newExpiry := time.Now().Add(oldExpiry.Sub(oldCreated))

	newKey, err := generateAPIKey()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to generate API key"})
		return
	}

	tx, err := db.Begin()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "db error"})
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM api_keys WHERE id=?", keyID); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to rotate key"})
		return
	}
	var newID int
	if err := tx.QueryRow(
		"INSERT INTO api_keys (user_id, name, api_key, expires_at) VALUES (?, ?, ?, ?) RETURNING id",
		userID, name, hashAPIKey(newKey), newExpiry.Unix(),
	).Scan(&newID); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to rotate key"})
		return
	}
	if err := tx.Commit(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "db error"})
		return
	}

	credentials.invalidate(presentedKey)

	json.NewEncoder(w).Encode(map[string]any{
		"key_id":     newID,
		"api_key":    newKey,
		"expires_at": newExpiry.UTC().Format(time.RFC3339),
	})
}

// DELETE /api/v1/apikeys/current — self-revoke, authenticated by presenting
// the key being revoked. Lets a publisher client (e.g. wp-dansal on
// uninstall/disconnect) retire its own key without admin credentials,
// instead of leaving an orphaned live key behind.
func revokeCurrentAPIKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	authHeader := r.Header.Get("Authorization")
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid authorization header format. Use 'Bearer <api_key>'"})
		return
	}
	presentedKey := parts[1]

	var keyID int
	err := db.QueryRow(
		"SELECT api_keys.id FROM api_keys JOIN users ON api_keys.user_id = users.id WHERE api_keys.api_key = ? AND users.disabled = 0",
		hashAPIKey(presentedKey),
	).Scan(&keyID)
	if err == sql.ErrNoRows {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid or expired credentials"})
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Internal server error"})
		return
	}

	if _, err := db.Exec("DELETE FROM api_keys WHERE id = ?", keyID); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to revoke API key"})
		return
	}

	credentials.invalidate(presentedKey)
	w.WriteHeader(http.StatusNoContent)
}
