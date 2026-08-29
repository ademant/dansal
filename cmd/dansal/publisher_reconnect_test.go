package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
)

// TestInviteLinksTargetUserIDMigration is a smoke test for the #1190
// migration (safety-net pattern from CLAUDE.md): createTables + migrateDB,
// run migrateDB a second time to confirm idempotency, then confirm the
// column exists either way.
func TestInviteLinksTargetUserIDMigration(t *testing.T) {
	setupDedupTestDB(t)
	migrateDB() // idempotency check

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('invite_links') WHERE name='target_user_id'").Scan(&n); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if n != 1 {
		t.Fatalf("invite_links.target_user_id column missing after migrateDB (n=%d)", n)
	}
}

// setupReconnectTestConfig wires up an invite-signing key (reconnect
// invites are JWTs, same as regular publisher invites) plus the defaults
// createPublisherReconnectInviteRecord needs.
func setupReconnectTestConfig(t *testing.T) {
	t.Helper()
	prevConfig := config
	config = &Config{}
	config.Server.BaseURL = "https://dansal.example.test"
	config.Server.InvitePublisherExpiryMinutes = 30
	config.Server.InviteSigningKeyPath = filepath.Join(t.TempDir(), "invite_signing_key.pem")
	t.Cleanup(func() { config = prevConfig })

	if err := loadOrGenerateInviteSigningKey(); err != nil {
		t.Fatalf("loadOrGenerateInviteSigningKey: %v", err)
	}
}

func reconnectReq(callerID int, callerRole string, targetID int) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/publishers/"+strconv.Itoa(targetID)+"/reconnect-invite", nil)
	r.Header.Set("X-User-ID", strconv.Itoa(callerID))
	r.Header.Set("X-User-Role", callerRole)
	r.SetPathValue("id", strconv.Itoa(targetID))
	return r
}

// TestPublisherReconnectInviteRoundTrip covers #1190: an admin mints a
// reconnect invite for an existing publisher, and redeeming it rotates that
// publisher's key in place (old key stops validating, new key works)
// without creating a second user or org-membership row.
func TestPublisherReconnectInviteRoundTrip(t *testing.T) {
	setupDedupTestDB(t)
	setupReconnectTestConfig(t)

	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'admin@example.test', 'Admin', 'admin')")
	db.Exec("INSERT INTO organizations (id, name, actor_name) VALUES (1, 'Test Org', 'test-org')")
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (2, '', 'wp-dansal @ example.com', 'publisher')")
	db.Exec("INSERT INTO organization_members (organization_id, user_id) VALUES (1, 2)")

	oldKey, err := generateAPIKey()
	if err != nil {
		t.Fatalf("generateAPIKey: %v", err)
	}
	db.Exec("INSERT INTO api_keys (user_id, name, api_key) VALUES (2, 'old key', ?)", hashAPIKey(oldKey))

	if _, _, err := validateAPIKey(oldKey); err != nil {
		t.Fatalf("precondition: old key should validate before reconnect, got %v", err)
	}

	var usersBefore int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&usersBefore)

	// Admin mints the reconnect invite.
	w := httptest.NewRecorder()
	createPublisherReconnectInvite(w, reconnectReq(1, RoleAdmin, 2))
	if w.Code != http.StatusCreated {
		t.Fatalf("create reconnect invite status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	var link InviteLink
	if err := json.Unmarshal(w.Body.Bytes(), &link); err != nil {
		t.Fatalf("decode invite link: %v", err)
	}
	if link.Token == "" {
		t.Fatal("expected a non-empty invite token")
	}

	var targetUserID int
	if err := db.QueryRow("SELECT target_user_id FROM invite_links WHERE token=?", link.Token).Scan(&targetUserID); err != nil {
		t.Fatalf("query target_user_id: %v", err)
	}
	if targetUserID != 2 {
		t.Fatalf("invite_links.target_user_id = %d, want 2", targetUserID)
	}

	// Redeem it via the same public endpoint a fresh publisher invite uses.
	redeemReq := httptest.NewRequest(http.MethodPost, "/api/v1/invites/"+link.Token+"/publisher", nil)
	redeemReq.SetPathValue("token", link.Token)
	redeemW := httptest.NewRecorder()
	redeemPublisherInvite(redeemW, redeemReq)
	if redeemW.Code != http.StatusCreated {
		t.Fatalf("redeem status = %d, want 201 (body=%s)", redeemW.Code, redeemW.Body.String())
	}
	var resp struct {
		UserID int    `json:"user_id"`
		APIKey string `json:"api_key"`
		OrgID  int    `json:"org_id"`
	}
	if err := json.Unmarshal(redeemW.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode redeem response: %v", err)
	}
	if resp.UserID != 2 {
		t.Fatalf("resp.UserID = %d, want 2 (the existing publisher, not a new account)", resp.UserID)
	}
	if resp.OrgID != 1 {
		t.Fatalf("resp.OrgID = %d, want 1", resp.OrgID)
	}
	if resp.APIKey == "" || resp.APIKey == oldKey {
		t.Fatal("expected a freshly rotated api_key, distinct from the old one")
	}

	// No new user/org-membership was created.
	var usersAfter int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&usersAfter)
	if usersAfter != usersBefore {
		t.Fatalf("users count changed from %d to %d; reconnect must not create a new account", usersBefore, usersAfter)
	}

	// Old key no longer validates; new key does.
	if _, _, err := validateAPIKey(oldKey); err == nil {
		t.Error("old key should no longer validate after reconnect")
	}
	if _, _, err := validateAPIKey(resp.APIKey); err != nil {
		t.Errorf("new key should validate after reconnect, got %v", err)
	}

	// The invite is single-use.
	redeemReq2 := httptest.NewRequest(http.MethodPost, "/api/v1/invites/"+link.Token+"/publisher", nil)
	redeemReq2.SetPathValue("token", link.Token)
	redeemW2 := httptest.NewRecorder()
	redeemPublisherInvite(redeemW2, redeemReq2)
	if redeemW2.Code == http.StatusCreated {
		t.Error("expected the reconnect invite to be single-use")
	}
}

// TestPublisherReconnectInviteForbidsUnrelatedUser covers #1190
// authorization: a non-admin user with no shared org with the target
// publisher cannot mint a reconnect invite for them.
func TestPublisherReconnectInviteForbidsUnrelatedUser(t *testing.T) {
	setupDedupTestDB(t)
	setupReconnectTestConfig(t)

	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (1, 'outsider@example.test', 'Outsider', 'user')")
	db.Exec("INSERT INTO organizations (id, name, actor_name) VALUES (1, 'Test Org', 'test-org')")
	db.Exec("INSERT INTO users (id, email, display_name, role) VALUES (2, '', 'Publisher', 'publisher')")
	db.Exec("INSERT INTO organization_members (organization_id, user_id) VALUES (1, 2)")

	w := httptest.NewRecorder()
	createPublisherReconnectInvite(w, reconnectReq(1, RoleUser, 2))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a caller sharing no org with the publisher (body=%s)", w.Code, w.Body.String())
	}
}
