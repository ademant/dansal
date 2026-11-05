package main

import (
	"net/http/httptest"
	"strconv"
	"testing"
)

// #1812: an approved registration stays as a tombstone, so the applicant
// sees "approved" on /register. It must never count as pending, never be
// approved twice, and never take its (now active) user with it on expiry.
func TestApprovedRegistrationIsATombstoneThatKeepsTheUser(t *testing.T) {
	oldCfg := config
	config = &Config{}
	defer func() { config = oldCfg }()
	setupDedupTestDB(t)

	res, err := db.Exec("INSERT INTO users (email, display_name, password_hash, role, email_verified, disabled) VALUES ('ben@example.test', 'ben', 'x', 'user', 1, 1)")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, _ := res.LastInsertId()
	orgRes, err := db.Exec("INSERT INTO organizations (name) VALUES ('Ben Org')")
	if err != nil {
		t.Fatalf("insert org: %v", err)
	}
	orgID, _ := orgRes.LastInsertId()
	res, err = db.Exec(
		`INSERT INTO pending_registrations
		 (verification_token, approval_token, email, reg_type, org_id, verification_channel, verified, expires_at, user_id)
		 VALUES ('vtok2', 'atok2', 'ben@example.test', 'join_org', ?, 'email', 1, strftime('%s','now')+86400, ?)`, orgID, userID)
	if err != nil {
		t.Fatalf("insert pending registration: %v", err)
	}
	pendingID, _ := res.LastInsertId()

	approve := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/registrations/"+strconv.FormatInt(pendingID, 10)+"/approve", nil)
		req.Header.Set("X-User-ID", "1")
		req.Header.Set("X-User-Role", RoleAdmin)
		req.SetPathValue("id", strconv.FormatInt(pendingID, 10))
		rec := httptest.NewRecorder()
		approveRegHandler(rec, req)
		return rec
	}

	if rec := approve(); rec.Code != 201 {
		t.Fatalf("first approve status=%d; body=%s", rec.Code, rec.Body.String())
	}
	if n := count(t, "SELECT COUNT(*) FROM pending_registrations WHERE id=? AND approved=1", pendingID); n != 1 {
		t.Fatalf("approved row should be kept as a tombstone")
	}
	if n := count(t, "SELECT COUNT(*) FROM users WHERE id=? AND disabled=0", userID); n != 1 {
		t.Fatalf("applicant should be enabled")
	}
	if rec := approve(); rec.Code != 409 {
		t.Fatalf("second approve status=%d, want 409", rec.Code)
	}

	// Not pending: the admin pending count leaves it out.
	req := httptest.NewRequest("GET", "/api/v1/pending-registrations/count", nil)
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-User-Role", RoleAdmin)
	rec := httptest.NewRecorder()
	pendingRegCountHandler(rec, req)
	if got := rec.Body.String(); got != "{\"count\":0}\n" && got != "{\"count\":0}" {
		t.Errorf("pending count body = %q, want count 0", got)
	}

	// Expiry: the tombstone's week ends, the row goes, the user stays.
	db.Exec("UPDATE pending_registrations SET expires_at=strftime('%s','now')-1 WHERE id=?", pendingID)
	processExpiredRegistrations()
	if n := count(t, "SELECT COUNT(*) FROM pending_registrations WHERE id=?", pendingID); n != 0 {
		t.Errorf("expired tombstone should be swept")
	}
	if n := count(t, "SELECT COUNT(*) FROM users WHERE id=? AND disabled=0", userID); n != 1 {
		t.Errorf("expiry must not remove the approved applicant")
	}
}
