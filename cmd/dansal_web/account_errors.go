package main

import (
	"errors"
	"net/http"

	"github.com/ademant/dansal/internal/logging"
)

// accountErrorKeys maps the API's error_code for a password or invite step to
// the i18n key that explains it to the visitor (#1876). The password codes come
// from cmd/dansal/password.go, the invite codes from cmd/dansal/invites.go.
var accountErrorKeys = map[string]string{
	"too_short":          "pw_err_too_short",
	"too_long":           "pw_err_too_long",
	"classes":            "pw_err_classes",
	"breached":           "pw_err_breached",
	"invite_not_found":   "invite_err_not_found",
	"invite_used":        "invite_err_used",
	"invite_expired":     "invite_err_expired",
	"invite_bad_token":   "invite_err_bad_token",
	"display_name_taken": "invite_err_name_taken",
	"email_exists":       "invite_err_email_exists",
	"invalid_email":      "invite_err_invalid_email",
}

// accountErrorMessage explains a failed password or invite step: the rule that
// failed, with this instance's numbers, in the visitor's language (#1876). The
// returned status is the API's, so a 409 stays a 409 and a 422 stays a 422. An
// API error without a known code keeps its own message, as before; a failure
// that never reached the API gets the generic text.
func accountErrorMessage(r *http.Request, i18n *I18n, err error) (string, int) {
	strs := i18n.Strings(i18n.detectLang(r))
	var ae *apiHTTPError
	if !errors.As(err, &ae) {
		return strs.T("account_err_generic"), http.StatusBadGateway
	}
	status := ae.StatusCode
	if status == 0 {
		status = http.StatusBadRequest
	}
	key, ok := accountErrorKeys[ae.ErrorCode]
	if !ok {
		return apiErrUserMessage(err), status
	}
	p := currentPasswordPolicy()
	switch ae.ErrorCode {
	case "too_short":
		return strs.TF(key, p.MinLength), status
	case "too_long":
		return strs.TF(key, p.MaxLength), status
	case "classes":
		return strs.TF(key, p.RequireClasses), status
	}
	return strs.T(key), status
}

// invitePasswordFailure is what the invite password step answers when the
// redemption is refused: a short message and the API's status. The rejection
// is logged so it can be found later; the log carries the code and status only,
// never the email or password (#1876).
func invitePasswordFailure(r *http.Request, i18n *I18n, err error) (string, int) {
	msg, status := accountErrorMessage(r, i18n, err)
	var ae *apiHTTPError
	code := ""
	if errors.As(err, &ae) {
		code = ae.ErrorCode
	}
	logging.Warnf("invite redeem rejected status=%d code=%s", status, code)
	return msg, status
}
