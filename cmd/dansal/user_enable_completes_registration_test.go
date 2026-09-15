package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestEnablingUserCompletesJoinOrgRegistration covers a real prod bug: a
// join_org pending_registrations row carries the org the registrant asked to
// join, but only approveRegHandler's dedicated /admin/registrations approve
// action ever read it. An admin who instead enabled the account the generic
// way (updateUser, e.g. from /admin/users) got an active user with no org
// membership at all, and a pending_registrations row that lingered forever
// -- reported live for a Folkclub Marburg join_org registration. Any
// disabled->enabled transition through updateUser must finish that same
// join_org work, regardless of which admin screen triggered it.
func TestEnablingUserCompletesJoinOrgRegistration(t *testing.T) {
	setupDedupTestDB(t)

	res, err := db.Exec("INSERT INTO organizations (name) VALUES ('Folkclub Marburg')")
	if err != nil {
		t.Fatalf("insert org: %v", err)
	}
	orgID, _ := res.LastInsertId()

	res, err = db.Exec(
		"INSERT INTO users (email, display_name, password_hash, role, disabled) VALUES (?, ?, '', 'user', 1)",
		"zuk@folkclub-marburg.de", "zuk",
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, _ := res.LastInsertId()

	_, err = db.Exec(
		`INSERT INTO pending_registrations
		 (verification_token, approval_token, email, reg_type, org_id, verification_channel, expires_at, user_id)
		 VALUES ('vtok', 'atok', ?, 'join_org', ?, 'email', strftime('%s','now')+86400, ?)`,
		"zuk@folkclub-marburg.de", orgID, userID,
	)
	if err != nil {
		t.Fatalf("insert pending registration: %v", err)
	}

	disabled := false
	body, _ := json.Marshal(UserUpdateRequest{Disabled: &disabled})
	req := httptest.NewRequest("PUT", "/api/v1/users/"+strconv.FormatInt(userID, 10), bytes.NewReader(body))
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", RoleAdmin)
	req.SetPathValue("id", strconv.FormatInt(userID, 10))

	rec := httptest.NewRecorder()
	updateUser(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var memberOrgID int
	if err := db.QueryRow("SELECT organization_id FROM organization_members WHERE user_id=?", userID).Scan(&memberOrgID); err != nil {
		t.Fatalf("expected the user to be assigned to the org they registered to join: %v", err)
	}
	if int64(memberOrgID) != orgID {
		t.Errorf("assigned org_id = %d, want %d", memberOrgID, orgID)
	}

	var n int
	db.QueryRow("SELECT COUNT(*) FROM pending_registrations WHERE user_id=?", userID).Scan(&n)
	if n != 0 {
		t.Errorf("expected the pending_registrations row to be cleaned up, %d still present", n)
	}
}

// TestEnablingUserWithoutPendingRegistrationIsANoop confirms the safety net
// only fires when there's actually an outstanding join_org registration --
// enabling an ordinary user (no pending row at all) must not error or assign
// any org membership.
func TestEnablingUserWithoutPendingRegistrationIsANoop(t *testing.T) {
	setupDedupTestDB(t)

	res, err := db.Exec(
		"INSERT INTO users (email, display_name, password_hash, role, disabled) VALUES (?, ?, '', 'user', 1)",
		"plain@example.test", "plain",
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, _ := res.LastInsertId()

	disabled := false
	body, _ := json.Marshal(UserUpdateRequest{Disabled: &disabled})
	req := httptest.NewRequest("PUT", "/api/v1/users/"+strconv.FormatInt(userID, 10), bytes.NewReader(body))
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", RoleAdmin)
	req.SetPathValue("id", strconv.FormatInt(userID, 10))

	rec := httptest.NewRecorder()
	updateUser(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM organization_members WHERE user_id=?", userID).Scan(&n)
	if n != 0 {
		t.Errorf("expected no org membership assigned, got %d rows", n)
	}
}
