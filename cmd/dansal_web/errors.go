package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
)

// newErrorID returns a short random hex string for correlating errors across
// log entries and user-visible messages.
func newErrorID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("%08x", b)
}

// writeJSONError writes a JSON error response that includes a unique error_id
// for traceability. Errors other than 401 and 403 are logged with method,
// path, and error_id so they can be correlated from a user report.
func writeJSONError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	id := newErrorID()
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		log.Printf("error_id=%s status=%d method=%s path=%s: %s", id, status, r.Method, r.URL.Path, msg)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg, "error_id": id})
}

// logHTTPError logs an error with a unique error_id and writes a plain-text
// HTTP error response. The error_id is appended to the response body so users
// can quote it when reporting issues.
func logHTTPError(w http.ResponseWriter, r *http.Request, msg string, code int) {
	id := newErrorID()
	log.Printf("error_id=%s status=%d method=%s path=%s: %s", id, code, r.Method, r.URL.Path, msg)
	http.Error(w, msg+" (error_id: "+id+")", code)
}

// intPathValueOr404 parses the named path value as an integer, writing a 404
// and returning ok=false on failure. Consolidates the ~84 hand-rolled
// `id, err := strconv.Atoi(r.PathValue(key)); if err != nil { http.NotFound(w, r) }`
// sites (#1320) used by HTML admin pages, where a bad/missing ID means "no
// such page" rather than a structured API error.
func intPathValueOr404(w http.ResponseWriter, r *http.Request, key string) (int, bool) {
	v, err := strconv.Atoi(r.PathValue(key))
	if err != nil {
		http.NotFound(w, r)
		return 0, false
	}
	return v, true
}

// intPathValueOr400 is intPathValueOr404's sibling for the smaller cluster of
// JSON-style admin endpoints (#1320) that respond with a plain-text 400
// instead of a 404 on a bad ID.
func intPathValueOr400(w http.ResponseWriter, r *http.Request, key, errMsg string) (int, bool) {
	v, err := strconv.Atoi(r.PathValue(key))
	if err != nil {
		http.Error(w, errMsg, http.StatusBadRequest)
		return 0, false
	}
	return v, true
}
