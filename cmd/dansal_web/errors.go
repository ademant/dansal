package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
