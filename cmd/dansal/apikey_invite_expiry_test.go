package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRedeemPublisherInviteResponseOmitsExpiresAtByDefault covers #1189
// item 1: the invite-redemption response now carries an expires_at field
// (matching POST /api/v1/apikeys/renew's field name/format), but since
// invite-created keys have no default expiry it must stay absent rather
// than render as a zero value.
func TestRedeemPublisherInviteResponseOmitsExpiresAtByDefault(t *testing.T) {
	setupDedupTestDB(t)
	oldConfig := config
	config = &Config{Server: ServerConfig{BaseURL: "https://example.test"}}
	t.Cleanup(func() { config = oldConfig })

	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")

	orgRes, err := db.Exec("INSERT INTO organizations (name, actor_name) VALUES ('Test Org', 'test-org')")
	if err != nil {
		t.Fatalf("insert org: %v", err)
	}
	orgID, _ := orgRes.LastInsertId()

	token := "test-invite-token"
	if _, err := db.Exec(
		"INSERT INTO invite_links (token, created_by, role, org_id, expires_at, invite_type) VALUES (?, 1, ?, ?, ?, 'link')",
		token, RolePublisher, orgID, time.Now().Add(time.Hour).Unix(),
	); err != nil {
		t.Fatalf("insert invite: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/invites/"+token+"/publisher", bytes.NewReader([]byte(`{"name":"Test Publisher"}`)))
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	redeemPublisherInvite(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, present := resp["expires_at"]; present {
		t.Errorf("expected no expires_at key for a key with no expiry, got %v", resp["expires_at"])
	}
	if _, present := resp["api_key"]; !present {
		t.Error("expected api_key in response")
	}
}

// TestRenewAPIKeyGraceWindow covers #1189 item 2: a key past its
// expires_at but still inside config.Server.APIKeyRenewGraceHours can
// still be renewed; one past the grace window is rejected as before.
func TestRenewAPIKeyGraceWindow(t *testing.T) {
	setupDedupTestDB(t)
	oldConfig := config
	t.Cleanup(func() { config = oldConfig })

	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'pub@example.test', 'Publisher', 'publisher')")

	newKeyFor := func(t *testing.T, createdAgo, expiredAgo time.Duration) string {
		t.Helper()
		key, err := generateAPIKey()
		if err != nil {
			t.Fatalf("generateAPIKey: %v", err)
		}
		created := time.Now().Add(-createdAgo)
		expires := time.Now().Add(-expiredAgo)
		if _, err := db.Exec(
			"INSERT INTO api_keys (user_id, name, api_key, expires_at, created_at) VALUES (1, 'test key', ?, ?, ?)",
			hashAPIKey(key), expires.Unix(), created.Unix(),
		); err != nil {
			t.Fatalf("insert api key: %v", err)
		}
		return key
	}

	renew := func(t *testing.T, key string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/apikeys/renew", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		renewAPIKey(w, req)
		return w
	}

	t.Run("within grace window", func(t *testing.T) {
		config = &Config{Server: ServerConfig{APIKeyRenewGraceHours: 6}}
		key := newKeyFor(t, 30*24*time.Hour, 2*time.Hour) // expired 2h ago, grace is 6h
		w := renew(t, key)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 within grace window (body=%s)", w.Code, w.Body.String())
		}
	})

	t.Run("past grace window", func(t *testing.T) {
		config = &Config{Server: ServerConfig{APIKeyRenewGraceHours: 6}}
		key := newKeyFor(t, 30*24*time.Hour, 7*time.Hour) // expired 7h ago, grace is 6h
		w := renew(t, key)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 past grace window (body=%s)", w.Code, w.Body.String())
		}
	})

	t.Run("grace window disabled (zero)", func(t *testing.T) {
		config = &Config{Server: ServerConfig{APIKeyRenewGraceHours: 0}}
		key := newKeyFor(t, 30*24*time.Hour, 1*time.Minute) // expired just now
		w := renew(t, key)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 with grace window disabled (body=%s)", w.Code, w.Body.String())
		}
	})
}
