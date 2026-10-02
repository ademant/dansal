package main

import (
	"errors"
	"net/http"
)

// SaveError is what an admin form shows when a save failed (#1421). Before,
// every admin save path showed a bare "Save failed." and only the log had the
// reason — e.g. the API's per-account rate limit (429), where simply waiting
// helps. Key is the i18n key; Detail is the API's own validation message
// (English, as the API sends it); Ref is the error_id to quote when
// reporting the problem.
type SaveError struct {
	Key    string
	Detail string
	Ref    string
}

// adminSaveError classifies a failed save: 429 and 403 get their own
// translated explanation (the cases where the admin can act — wait, or ask
// for access), validation rejections (400/409/422) show the API's message,
// anything else the generic text plus the reference. A non-API error (local
// DB, network) gets the generic text only.
func adminSaveError(err error) SaveError {
	se := SaveError{Key: "admin_save_error"}
	var ae *apiHTTPError
	if !errors.As(err, &ae) {
		return se
	}
	se.Ref = ae.ErrorID
	switch ae.StatusCode {
	case http.StatusTooManyRequests:
		se.Key = "admin_save_error_rate_limited"
	case http.StatusForbidden:
		se.Key = "admin_save_error_forbidden"
	case http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity:
		se.Key = "admin_save_error_detail"
		se.Detail = ae.Message
	}
	return se
}
