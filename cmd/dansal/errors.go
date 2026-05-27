package main

import (
	"encoding/json"
	"log"
	"net/http"
)

func writeError(w http.ResponseWriter, msg string, code int) {
	if code >= 500 {
		log.Printf("error %d: %s", code, msg)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writeJSON sets Content-Type and encodes v as JSON with HTTP 200.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
