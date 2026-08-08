package main

import (
	"encoding/json"
	"net/http"
)

// writeJSONResponse writes v as an application/json response.
func writeJSONResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
