package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	ics "github.com/arran4/golang-ical"
)

// POST /api/v1/events/preview — parse iCal or folkdance-JSON without saving.
// Accepts multipart/form-data with fields: file, url, type, organization_id.
func previewEventsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	callerID, callerRole := callerFromRequest(r)

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeError(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	feedType := r.FormValue("type")
	if feedType == "" {
		feedType = "ical"
	}

	// Non-admins must belong to the target organisation.
	var orgID *int
	if callerRole != RoleAdmin {
		orgStr := r.FormValue("organization_id")
		if orgStr == "" {
			orgStr = r.URL.Query().Get("organization_id")
		}
		if orgStr == "" {
			writeError(w, "organization_id is required", http.StatusBadRequest)
			return
		}
		n, err := strconv.Atoi(orgStr)
		if err != nil || !isOrgMember(callerID, n) {
			writeError(w, "Forbidden: not a member of the specified organization", http.StatusForbidden)
			return
		}
		orgID = &n
	}

	src := FetchSource{Type: feedType, OrganizationID: orgID}

	var body []byte

	if feedURL := r.FormValue("url"); feedURL != "" {
		parsed, err := url.Parse(feedURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			writeError(w, "URL must use http or https scheme", http.StatusBadRequest)
			return
		}
		src.URL = feedURL
		if feedType == "ical" {
			src.Type = detectFetchType(feedURL)
		}
		resp, err := safeClient.Get(feedURL)
		if err != nil {
			writeError(w, "fetch failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			writeError(w, fmt.Sprintf("remote returned %d", resp.StatusCode), http.StatusBadGateway)
			return
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if err != nil {
			writeError(w, "read failed", http.StatusBadGateway)
			return
		}
	} else {
		file, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, "file or url is required", http.StatusBadRequest)
			return
		}
		defer file.Close()
		body, err = io.ReadAll(io.LimitReader(file, 10<<20))
		if err != nil {
			writeError(w, "read failed", http.StatusBadRequest)
			return
		}
	}

	reqs, err := parseBodyToRequests(body, src)
	if err != nil {
		writeError(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if reqs == nil {
		reqs = []EventCreateRequest{}
	}
	json.NewEncoder(w).Encode(reqs)
}

func parseBodyToRequests(body []byte, src FetchSource) ([]EventCreateRequest, error) {
	switch src.Type {
	case "folkdance-json":
		return parseFolkdanceJSONToRequests(body, src)
	default:
		cal, err := ics.ParseCalendar(strings.NewReader(string(body)))
		if err != nil {
			return nil, fmt.Errorf("parse iCal: %w", err)
		}
		return parseICalToRequests(cal, src), nil
	}
}
