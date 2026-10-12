package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
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

// apiKeyCols is the single column list for api_keys rows returned by
// listAPIKeys, shared across its two query branches (#1509).
const apiKeyCols = "id, user_id, name, expires_at, created_at"

func scanAPIKey(row interface{ Scan(...any) error }) (APIKey, error) {
	var k APIKey
	var exp sql.NullString
	err := row.Scan(&k.ID, &k.UserID, &k.Name, &exp, &k.CreatedAt)
	if err == nil && exp.Valid {
		k.ExpiresAt = epochStrToRFC3339(exp.String)
	}
	return k, err
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
	return sha256Hex(key)
}

// validateAPIKey checks an API key and returns the associated user plus the
// api_keys row id (#1366: needed by the signature-verification middleware to
// look up that key's signing secret; 0 would be ambiguous with "no key" so
// callers that don't need it can just discard the fourth return value).
// Results are cached for up to credCacheTTL; the cache is invalidated on deletion.
func validateAPIKey(key string) (int, string, int, error) {
	if userID, role, _, apiKeyID, ok := credentials.get(key, ""); ok { // API keys are never IP-pinned
		return userID, role, apiKeyID, nil
	}

	var userID, apiKeyID int
	var userRole string
	var expiresAt sql.NullString

	err := db.QueryRow(
		"SELECT users.id, users.role, api_keys.id, api_keys.expires_at FROM api_keys JOIN users ON api_keys.user_id = users.id WHERE api_keys.api_key = ? AND users.disabled = 0",
		hashAPIKey(key),
	).Scan(&userID, &userRole, &apiKeyID, &expiresAt)

	if err == sql.ErrNoRows {
		return 0, "", 0, fmt.Errorf("invalid api key")
	}
	if err != nil {
		return 0, "", 0, err
	}

	var expTime time.Time
	if expiresAt.Valid && expiresAt.String != "" {
		expTime, err = parseTokenExpiration(expiresAt.String)
		if err != nil || time.Now().After(expTime) {
			return 0, "", 0, fmt.Errorf("api key expired")
		}
	}

	credentials.set(key, userID, userRole, 0, apiKeyID, expTime, "") // tokenID 0 = API key, expTime zero → TTL cap only
	return userID, userRole, apiKeyID, nil
}

func listAPIKeys(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	var query string
	var args []any

	if callerRole == RoleAdmin {
		query = "SELECT " + apiKeyCols + " FROM api_keys"
	} else {
		// Non-admins see their own keys plus keys for publishers in the same org(s).
		query = `
			SELECT ak.id, ak.user_id, ak.name, ak.expires_at, ak.created_at FROM api_keys ak
			WHERE ak.user_id = ?
			UNION
			SELECT ak.id, ak.user_id, ak.name, ak.expires_at, ak.created_at FROM api_keys ak
			JOIN users u ON u.id = ak.user_id AND u.role = 'publisher'
			JOIN organization_members om1 ON om1.user_id = ak.user_id
			JOIN organization_members om2 ON om2.organization_id = om1.organization_id AND om2.user_id = ?`
		args = []any{callerID, callerID}
	}
	writeTotalCountHeader(w, query, args)
	applyListPagination(r, "id", &query, &args)

	rows, err := db.Query(query, args...)
	if err != nil {
		writeError(w, "Failed to fetch API keys", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	keys := []APIKey{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			continue
		}
		keys = append(keys, k)
	}

	writeJSON(w, keys)
}

func createAPIKey(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	var req CreateAPIKeyRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	if req.Name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}

	var expiresAt *int64
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			writeError(w, "expires_at must be RFC3339", http.StatusBadRequest)
			return
		}
		epoch := t.Unix()
		expiresAt = &epoch
	}

	targetUserID := callerID
	if req.UserID != nil {
		if callerRole != RoleAdmin && *req.UserID != callerID {
			writeError(w, "Only admins can create API keys for other users", http.StatusForbidden)
			return
		}
		targetUserID = *req.UserID
	}

	key, err := generateAPIKey()
	if err != nil {
		writeError(w, "Failed to generate API key", http.StatusInternalServerError)
		return
	}

	var id int
	var createdAt string
	err = db.QueryRow(
		"INSERT INTO api_keys (user_id, name, api_key, expires_at) VALUES (?, ?, ?, ?) RETURNING id, created_at",
		targetUserID, req.Name, hashAPIKey(key), expiresAt,
	).Scan(&id, &createdAt)
	if err != nil {
		writeError(w, "Failed to create API key", http.StatusInternalServerError)
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
	writeJSONStatus(w, http.StatusCreated, resp)
}

func deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	callerID, callerRole := callerFromRequest(r)

	keyID, ok := requireIntPathValue(w, r, "id", "Invalid API key ID")
	if !ok {
		return
	}

	var ownerID int
	err := db.QueryRow("SELECT user_id FROM api_keys WHERE id = ?", keyID).Scan(&ownerID)
	if !rowOr404(w, err, "API key not found") {
		return
	}

	if callerRole != RoleAdmin && callerID != ownerID {
		writeError(w, "Cannot delete another user's API key", http.StatusForbidden)
		return
	}

	if _, err = db.Exec("DELETE FROM api_keys WHERE id = ?", keyID); err != nil {
		writeError(w, "Failed to delete API key", http.StatusInternalServerError)
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
	authHeader := r.Header.Get("Authorization")
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		writeError(w, "Invalid authorization header format. Use 'Bearer <api_key>'", http.StatusUnauthorized)
		return
	}
	presentedKey := parts[1]

	var keyID, userID, requireSignature int
	var name, createdAtStr string
	var expiresAt, signingSecretEnc sql.NullString
	err := db.QueryRow(
		"SELECT api_keys.id, api_keys.user_id, api_keys.name, api_keys.created_at, api_keys.expires_at, api_keys.signing_secret_enc, api_keys.require_signature FROM api_keys JOIN users ON api_keys.user_id = users.id WHERE api_keys.api_key = ? AND users.disabled = 0",
		hashAPIKey(presentedKey),
	).Scan(&keyID, &userID, &name, &createdAtStr, &expiresAt, &signingSecretEnc, &requireSignature)
	if err == sql.ErrNoRows {
		writeError(w, "Invalid or expired credentials", http.StatusUnauthorized)
		return
	}
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !expiresAt.Valid || expiresAt.String == "" {
		writeError(w, "This key has no expiry; renewal is not applicable", http.StatusBadRequest)
		return
	}

	oldExpiry, err := parseTokenExpiration(expiresAt.String)
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	// #1189: tolerate a short grace window past expires_at so a client
	// polling on its own lead-time schedule (daily/weekly) doesn't lose the
	// connection to a timing race — it can still recover by renewing shortly
	// after the clock ticks over, rather than needing a human to reissue the
	// key via regenerate-key the instant expiry passes.
	graceUntil := oldExpiry.Add(time.Duration(config.Server.APIKeyRenewGraceHours) * time.Hour)
	if time.Now().After(graceUntil) {
		writeError(w, "This key has already expired; ask an admin or org member to issue a new one", http.StatusUnauthorized)
		return
	}
	oldCreated, err := parseTokenExpiration(createdAtStr)
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	newExpiry := time.Now().Add(oldExpiry.Sub(oldCreated))

	newKey, err := generateAPIKey()
	if err != nil {
		writeError(w, "Failed to generate API key", http.StatusInternalServerError)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM api_keys WHERE id=?", keyID); err != nil {
		writeError(w, "Failed to rotate key", http.StatusInternalServerError)
		return
	}
	// #1366: carry the signing secret/enforcement flag over to the new row —
	// renew only extends expiry, it must not silently disable signature
	// enforcement (or orphan a secret the client already has cached) just
	// because the underlying row id changed.
	var newID int
	if err := tx.QueryRow(
		"INSERT INTO api_keys (user_id, name, api_key, expires_at, signing_secret_enc, require_signature) VALUES (?, ?, ?, ?, ?, ?) RETURNING id",
		userID, name, hashAPIKey(newKey), newExpiry.Unix(), signingSecretEnc, requireSignature,
	).Scan(&newID); err != nil {
		writeError(w, "Failed to rotate key", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, "db error", http.StatusInternalServerError)
		return
	}

	credentials.invalidate(presentedKey)

	writeJSON(w, map[string]any{
		"key_id":     newID,
		"api_key":    newKey,
		"expires_at": newExpiry.UTC().Format(time.RFC3339),
	})
}

// POST /api/v1/apikeys/rotate-signing-secret — publisher self-service
// rotation of their own signing secret (#1366), authenticated the same way
// as renewAPIKey: by presenting the API key itself, not a session token.
// Simplification vs. the original proposal: this is an immediate hard swap,
// not a dual-secret grace window — the old secret stops verifying the
// instant this call succeeds, so a client should update its stored secret
// before its next signed request, not "eventually".
//
// Security note: once a key has require_signature=1, rotation additionally
// requires a valid signature made with the *current* secret (checked below
// via checkRequestSignature). Without this, a leaked bearer token alone —
// with no signing secret at all — could mint itself a fresh secret here and
// then satisfy the signature check on every other write, defeating the
// entire point of requiring a second, separately-held credential.
func rotateSigningSecret(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		writeError(w, "Invalid authorization header format. Use 'Bearer <api_key>'", http.StatusUnauthorized)
		return
	}
	presentedKey := parts[1]

	if !config.Server.Signing.Enabled {
		writeError(w, "request signing is not enabled on this server", http.StatusBadRequest)
		return
	}

	var keyID int
	err := db.QueryRow(
		"SELECT api_keys.id FROM api_keys JOIN users ON api_keys.user_id = users.id WHERE api_keys.api_key = ? AND users.disabled = 0",
		hashAPIKey(presentedKey),
	).Scan(&keyID)
	if err == sql.ErrNoRows {
		writeError(w, "Invalid or expired credentials", http.StatusUnauthorized)
		return
	}
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := checkRequestSignature(r, keyID); err != nil {
		writeError(w, "request signature invalid: "+err.Error(), http.StatusUnauthorized)
		return
	}

	secret, err := generateSigningSecret()
	if err != nil {
		writeError(w, "Failed to generate signing secret", http.StatusInternalServerError)
		return
	}
	if _, err := db.Exec("UPDATE api_keys SET signing_secret_enc=? WHERE id=?", signingSecretEncrypt(secret), keyID); err != nil {
		writeError(w, "Failed to store signing secret", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"signing_secret": secret})
}

// DELETE /api/v1/apikeys/current — self-revoke, authenticated by presenting
// the key being revoked. Lets a publisher client (e.g. wp-dansal on
// uninstall/disconnect) retire its own key without admin credentials,
// instead of leaving an orphaned live key behind.
func revokeCurrentAPIKey(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		writeError(w, "Invalid authorization header format. Use 'Bearer <api_key>'", http.StatusUnauthorized)
		return
	}
	presentedKey := parts[1]

	var keyID int
	err := db.QueryRow(
		"SELECT api_keys.id FROM api_keys JOIN users ON api_keys.user_id = users.id WHERE api_keys.api_key = ? AND users.disabled = 0",
		hashAPIKey(presentedKey),
	).Scan(&keyID)
	if err == sql.ErrNoRows {
		writeError(w, "Invalid or expired credentials", http.StatusUnauthorized)
		return
	}
	if err != nil {
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if _, err := db.Exec("DELETE FROM api_keys WHERE id = ?", keyID); err != nil {
		writeError(w, "Failed to revoke API key", http.StatusInternalServerError)
		return
	}

	credentials.invalidate(presentedKey)
	w.WriteHeader(http.StatusNoContent)
}
