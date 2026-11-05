package main

import (
	"net/http/httptest"
	"strconv"
	"testing"
)

// #1810: an applicant who confirmed their address during registration must
// come out of approval with email_verified=1. Before the fix approval only
// enabled the account, so settings showed "not verified" and asked them to
// confirm an address they had already confirmed.
func TestApprovalMarksConfirmedEmailAsVerified(t *testing.T) {
	// approveRegHandler notifies the applicant in a goroutine, which reads
	// config; a non-nil config with no SMTP means nothing is sent.
	oldCfg := config
	config = &Config{}
	defer func() { config = oldCfg }()
	cases := []struct {
		name     string
		channel  string
		verified int
		email    string
		want     int
	}{
		{"email channel, confirmed", "email", 1, "anna@example.test", 1},
		{"telegram channel is not an email confirmation", "telegram", 1, "anna@example.test", 0},
		{"email channel, not confirmed", "email", 0, "anna@example.test", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupDedupTestDB(t)
			res, err := db.Exec(
				"INSERT INTO users (email, display_name, password_hash, role, email_verified, disabled) VALUES (?, 'anna', 'x', 'user', 0, 1)",
				nullableEmail(tc.email),
			)
			if err != nil {
				t.Fatalf("insert user: %v", err)
			}
			userID, _ := res.LastInsertId()
			orgRes, err := db.Exec("INSERT INTO organizations (name) VALUES ('Test Org')")
			if err != nil {
				t.Fatalf("insert org: %v", err)
			}
			orgID, _ := orgRes.LastInsertId()
			res, err = db.Exec(
				`INSERT INTO pending_registrations
				 (verification_token, approval_token, email, reg_type, org_id, verification_channel, verified, expires_at, user_id)
				 VALUES ('vtok', 'atok', ?, 'join_org', ?, ?, ?, strftime('%s','now')+86400, ?)`,
				tc.email, orgID, tc.channel, tc.verified, userID,
			)
			if err != nil {
				t.Fatalf("insert pending registration: %v", err)
			}
			pendingID, _ := res.LastInsertId()

			req := httptest.NewRequest("POST", "/api/v1/registrations/"+strconv.FormatInt(pendingID, 10)+"/approve", nil)
			req.Header.Set("X-User-ID", "1")
			req.Header.Set("X-User-Role", RoleAdmin)
			req.SetPathValue("id", strconv.FormatInt(pendingID, 10))
			rec := httptest.NewRecorder()
			approveRegHandler(rec, req)
			if rec.Code != 201 {
				t.Fatalf("approve status=%d, want 201; body=%s", rec.Code, rec.Body.String())
			}

			var got int
			db.QueryRow("SELECT email_verified FROM users WHERE id=?", userID).Scan(&got)
			if got != tc.want {
				t.Errorf("email_verified=%d, want %d", got, tc.want)
			}
		})
	}
}

func nullableEmail(s string) any {
	if s == "" {
		return nil
	}
	return s
}
