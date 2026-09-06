package main

import (
	"database/sql"
	"net/http/httptest"
	"testing"
)

// #1275: requireEventOrg used to unconditionally deny any non-admin caller
// touching an event with no organization at all (existingOrgID invalid),
// before ever looking at what org they were trying to assign it to — only
// an admin could move an unassigned (e.g. feed-imported) event into an
// organization. These tests cover the fix: a non-admin may now claim such
// an event into any org they belong to, but only when they actually name
// that target org; an event that already belongs to a *different* org, or
// a request that names no target org at all, must keep denying exactly as
// before.
func setupOrgClaimTestDB(t *testing.T) (userA, userB, orgA, orgB int) {
	t.Helper()
	setupDedupTestDB(t)

	mustExec := func(query string, args ...any) int64 {
		res, err := db.Exec(query, args...)
		if err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	orgAID := mustExec("INSERT INTO organizations (name) VALUES ('Org A')")
	orgBID := mustExec("INSERT INTO organizations (name) VALUES ('Org B')")
	userAID := mustExec("INSERT INTO users (email, display_name, role) VALUES ('a@example.test', 'User A', 'user')")
	userBID := mustExec("INSERT INTO users (email, display_name, role) VALUES ('b@example.test', 'User B', 'user')")
	mustExec("INSERT INTO organization_members (organization_id, user_id) VALUES (?, ?)", orgAID, userAID)
	mustExec("INSERT INTO organization_members (organization_id, user_id) VALUES (?, ?)", orgBID, userBID)

	return int(userAID), int(userBID), int(orgAID), int(orgBID)
}

func TestRequireEventOrgClaimsOrglessEventIntoOwnOrg(t *testing.T) {
	userA, _, orgA, _ := setupOrgClaimTestDB(t)

	w := httptest.NewRecorder()
	ok := requireEventOrg(w, RoleUser, userA, sql.NullInt64{}, &orgA, true)
	if !ok {
		t.Fatalf("expected claim to succeed, got denied: %s", w.Body.String())
	}
}

func TestRequireEventOrgDeniesClaimIntoOrgNotAMember(t *testing.T) {
	userA, _, _, orgB := setupOrgClaimTestDB(t)

	w := httptest.NewRecorder()
	ok := requireEventOrg(w, RoleUser, userA, sql.NullInt64{}, &orgB, true)
	if ok {
		t.Fatal("expected claim into a non-member org to be denied")
	}
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// Naming no target org at all — timetableAuthCheck's call shape (no
// target-org concept), or a PUT that omits organization_id — must keep
// denying an org-less event to non-admins exactly as before #1275: there's
// nothing named to claim it into.
func TestRequireEventOrgNoTargetNamedStillDeniesOrglessEvent(t *testing.T) {
	userA, _, _, _ := setupOrgClaimTestDB(t)

	w := httptest.NewRecorder()
	if requireEventOrg(w, RoleUser, userA, sql.NullInt64{}, nil, true) {
		t.Error("requireTarget=true, no target named: expected denial")
	}

	w2 := httptest.NewRecorder()
	if requireEventOrg(w2, RolePublisher, userA, sql.NullInt64{}, nil, false) {
		t.Error("requireTarget=false, no target named: expected denial (nothing to claim into)")
	}
}

// An event that already belongs to a different org must stay fully
// protected — #1275 only relaxes the org-less case.
func TestRequireEventOrgStillDeniesNonMemberOfExistingOrg(t *testing.T) {
	_, userB, orgA, _ := setupOrgClaimTestDB(t)

	w := httptest.NewRecorder()
	if requireEventOrg(w, RoleUser, userB, sql.NullInt64{Int64: int64(orgA), Valid: true}, &orgA, true) {
		t.Error("expected denial: userB does not belong to orgA")
	}
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// Admin remains unrestricted regardless of org state.
func TestRequireEventOrgAdminUnrestricted(t *testing.T) {
	_, _, _, orgB := setupOrgClaimTestDB(t)

	w := httptest.NewRecorder()
	if !requireEventOrg(w, RoleAdmin, 999, sql.NullInt64{}, nil, true) {
		t.Error("admin should never be denied")
	}
	w2 := httptest.NewRecorder()
	if !requireEventOrg(w2, RoleAdmin, 999, sql.NullInt64{}, &orgB, true) {
		t.Error("admin should never be denied")
	}
}

// An existing org member editing their own org's already-assigned event
// (the pre-#1275, unaffected path) must keep working.
func TestRequireEventOrgUnchangedForOwnedEvent(t *testing.T) {
	userA, _, orgA, _ := setupOrgClaimTestDB(t)

	w := httptest.NewRecorder()
	if !requireEventOrg(w, RoleUser, userA, sql.NullInt64{Int64: int64(orgA), Valid: true}, &orgA, true) {
		t.Fatalf("expected member editing their own org's event to succeed: %s", w.Body.String())
	}
}
