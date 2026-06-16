package main

import (
	"net/http"
	"strings"
)

// doNotTrack returns true when the request signals the user doesn't want tracking.
// Checks DNT and Sec-GPC headers.
func doNotTrack(r *http.Request) bool {
	dnt := r.Header.Get("DNT")
	if dnt == "1" || strings.ToLower(dnt) == "1" {
		return true
	}
	gpc := r.Header.Get("Sec-GPC")
	if gpc == "1" {
		return true
	}
	return false
}

// saveDataOn returns true if the client requested reduced data usage.
func saveDataOn(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Save-Data"), "on")
}
