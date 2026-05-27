package main

import (
	"encoding/json"
	"log"
	"net/http"
)

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	if status >= 500 {
		log.Printf("error %d: %s", status, msg)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
