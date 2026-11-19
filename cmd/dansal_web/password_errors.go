package main

import (
	"errors"
	"net/http"

	"github.com/ademant/dansal/internal/logging"
)

// passwordErrorMessage translates an API password-rule rejection
// (writePasswordError in cmd/dansal/password.go: error_code too_short,
// too_long, classes, breached) into a message naming the failed rule in the
// user's language, with the instance policy's numbers filled in (#1876).
// Returns "" when err is not such a rejection, so callers can fall back to
// their own handling.
func passwordErrorMessage(s I18nStrings, err error) string {
	var ae *apiHTTPError
	if !errors.As(err, &ae) {
		return ""
	}
	p := currentPasswordPolicy()
	switch ae.ErrorCode {
	case "too_short":
		return s.TF("pw_error_too_short", p.MinLength)
	case "too_long":
		return s.TF("pw_error_too_long", p.MaxLength)
	case "classes":
		return s.TF("pw_error_classes", p.RequireClasses)
	case "breached":
		return s.T("pw_error_breached")
	}
	return ""
}

// inviteRedeemError maps a failed invite redemption (POST /api/v1/invites/{token})
// to the status and translated message shown on the invite page (#1876),
// instead of the raw "dansal API 4xx: …" string. The API status is passed
// through for 4xx; anything else (network error, 5xx) becomes 502.
func inviteRedeemError(s I18nStrings, err error) (int, string) {
	if msg := passwordErrorMessage(s, err); msg != "" {
		return http.StatusUnprocessableEntity, msg
	}
	var ae *apiHTTPError
	if !errors.As(err, &ae) || ae.StatusCode < 400 || ae.StatusCode >= 500 {
		return http.StatusBadGateway, s.T("invite_error_generic")
	}
	switch ae.StatusCode {
	case http.StatusConflict:
		return ae.StatusCode, s.T("invite_error_name_taken")
	case http.StatusUnprocessableEntity:
		return ae.StatusCode, s.T("invite_error_email_invalid")
	case http.StatusNotFound, http.StatusGone:
		return ae.StatusCode, s.T("invite_expired")
	}
	return ae.StatusCode, s.T("invite_error_generic")
}

// logInviteRedeemRejected records why an invite redemption failed — without
// the password or email — so a stuck invitee shows up in the journal (#1876).
func logInviteRedeemRejected(err error) {
	var ae *apiHTTPError
	if errors.As(err, &ae) {
		logging.Warnf("invite redeem rejected status=%d code=%q msg=%q", ae.StatusCode, ae.ErrorCode, ae.Message)
		return
	}
	logging.Warnf("invite redeem rejected err=%v", err)
}
