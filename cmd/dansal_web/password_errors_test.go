package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestPasswordErrorMessage covers #1876: each API password-rule error_code
// maps to a translated message with the policy's number filled in.
func TestPasswordErrorMessage(t *testing.T) {
	old := currentPasswordPolicy()
	t.Cleanup(func() { setPasswordPolicy(old) })
	setPasswordPolicy(PasswordPolicy{MinLength: 12, MaxLength: 64, RequireClasses: 3, CheckBreached: true})

	s := loadI18n("").Strings("en")
	cases := []struct {
		code, want string
	}{
		{"too_short", "at least 12 characters"},
		{"too_long", "at most 64 characters"},
		{"classes", "at least 3 of"},
		{"breached", "data breach"},
	}
	for _, c := range cases {
		err := &apiHTTPError{StatusCode: 422, Message: "raw", ErrorCode: c.code}
		got := passwordErrorMessage(s, err)
		if !strings.Contains(got, c.want) || strings.Contains(got, "%!") {
			t.Errorf("%s: got %q, want it to contain %q", c.code, got, c.want)
		}
	}
	if got := passwordErrorMessage(s, &apiHTTPError{StatusCode: 409, Message: "x"}); got != "" {
		t.Errorf("no code: got %q, want empty", got)
	}
	if got := passwordErrorMessage(s, errors.New("net")); got != "" {
		t.Errorf("non-API error: got %q, want empty", got)
	}
}

// TestInviteRedeemError covers #1876: the invite page gets a translated
// message and the API's 4xx status instead of "dansal API 4xx: …" as 400.
func TestInviteRedeemError(t *testing.T) {
	s := loadI18n("").Strings("en")
	cases := []struct {
		err        error
		wantStatus int
		wantKey    string
	}{
		{&apiHTTPError{StatusCode: 422, ErrorCode: "breached"}, 422, "pw_error_breached"},
		{&apiHTTPError{StatusCode: 409, Message: "Display name is already taken"}, 409, "invite_error_name_taken"},
		{&apiHTTPError{StatusCode: 422, Message: "invalid email address"}, 422, "invite_error_email_invalid"},
		{&apiHTTPError{StatusCode: 410, Message: "Invite link already used"}, 410, "invite_expired"},
		{&apiHTTPError{StatusCode: 404, Message: "Invalid or expired invite link"}, 404, "invite_expired"},
		{&apiHTTPError{StatusCode: 400, Message: "Invalid invite link"}, 400, "invite_error_generic"},
		{&apiHTTPError{StatusCode: 500, Message: "Internal server error"}, http.StatusBadGateway, "invite_error_generic"},
		{errors.New("connection refused"), http.StatusBadGateway, "invite_error_generic"},
	}
	for _, c := range cases {
		status, msg := inviteRedeemError(s, c.err)
		if status != c.wantStatus || msg != s.T(c.wantKey) {
			t.Errorf("%v: got (%d, %q), want (%d, %q)", c.err, status, msg, c.wantStatus, s.T(c.wantKey))
		}
		if strings.Contains(msg, "dansal API") {
			t.Errorf("%v: message leaks API prefix: %q", c.err, msg)
		}
	}
}
