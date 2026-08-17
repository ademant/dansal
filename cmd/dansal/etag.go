package main

import (
	"fmt"
	"net/http"
)

// weakEtag formats a row's change-timestamp (Unix epoch seconds) as an ETag
// value, for both emitting on GET and comparing against a client's If-Match
// on PUT/PATCH (#1128).
func weakEtag(epoch int64) string {
	return fmt.Sprintf(`"%d"`, epoch)
}

// checkIfMatchConflict reports whether the request carries an If-Match header
// that does not match currentEpoch, writing a 412 Precondition Failed and
// returning true in that case. When If-Match is absent, returns false
// (today's last-write-wins behavior is preserved) — this is purely additive,
// opt-in-only concurrency protection. Callers must return immediately when
// this returns true.
func checkIfMatchConflict(w http.ResponseWriter, r *http.Request, currentEpoch int64) bool {
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		return false
	}
	if ifMatch == weakEtag(currentEpoch) || ifMatch == "*" {
		return false
	}
	writeError(w, "resource has been modified since it was fetched (If-Match mismatch)", http.StatusPreconditionFailed)
	return true
}
