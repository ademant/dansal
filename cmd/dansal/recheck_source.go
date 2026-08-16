package main

// Recheck an event's import source (#1112): events created from a one-off
// iCal/JSON URL (or file) via the "Admin -> Import events" preview flow
// don't get the automatic change-detection that fetch_sources subscribers
// get on every poll cycle (see insertEvent's update path). This endpoint
// lets an admin/org member manually re-fetch the URL the event was
// originally imported from (events.source, already set by
// parseICalToRequests/parseGancioJSONToRequests etc.) and merge any
// changes, reusing the same parse/dedup/merge machinery as the recurring
// importer instead of adding a second copy of it.
//
// Deliberately does NOT go through fetch_sources: that table is an
// unpaginated, hand-curated list of standing feed subscriptions, and
// auto-creating one row per ad-hoc single-event import would crowd it with
// a different kind of thing entirely.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// POST /api/v1/events/{id}/recheck-source
func recheckEventSource(w http.ResponseWriter, r *http.Request) {
	callerID, userRole := callerFromRequest(r)
	id, err := intPathValue(r, "id")
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var orgID sql.NullInt64
	var source, uidStr, evURL string
	var fetchSourceID int
	var isPublished bool
	if err := db.QueryRow("SELECT organization_id, COALESCE(source,''), COALESCE(uid,''), COALESCE(url,''), COALESCE(fetch_source_id,0), is_published FROM events WHERE id=?", id).
		Scan(&orgID, &source, &uidStr, &evURL, &fetchSourceID, &isPublished); err == sql.ErrNoRows {
		writeError(w, "Event not found", http.StatusNotFound)
		return
	} else if err != nil {
		writeInternalError(w, err)
		return
	}

	if userRole != RoleAdmin && !requireExistingOrgMember(w, callerID, orgID) {
		return
	}

	if source == "" {
		writeError(w, "event has no source URL to recheck", http.StatusBadRequest)
		return
	}

	var orgIDArg *int
	if orgID.Valid {
		v := int(orgID.Int64)
		orgIDArg = &v
	}

	outcome, err := doRecheckEventSource(db, source, uidStr, evURL, fetchSourceID, isPublished, orgIDArg)
	if err != nil {
		writeError(w, "recheck failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"outcome": outcome})
}

// doRecheckEventSource fetches source, parses it with the same feed
// parsers used by imports/previews, finds the entry matching the existing
// event, and merges it in via createEventFromRequest (the same helper the
// recurring fetch importer uses per parsed entry). isPublished is passed
// through as the event's *current* publish state so a recheck never
// silently (un)publishes an event the admin explicitly set — feed sources
// don't carry publish intent.
func doRecheckEventSource(q querier, source, uidStr, evURL string, fetchSourceID int, isPublished bool, orgID *int) (string, error) {
	parsed, err := url.Parse(source)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("stored source is not a valid http(s) URL")
	}

	src := FetchSource{ID: fetchSourceID, Type: detectFetchType(source), URL: source, OrganizationID: orgID}

	resp, err := safeClient.Get(source)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("remote returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return "", fmt.Errorf("read failed: %w", err)
	}

	reqs, err := parseBodyToRequests(body, src)
	if err != nil {
		return "", err
	}
	req, ok := matchRecheckEntry(reqs, uidStr, evURL)
	if !ok {
		return "", fmt.Errorf("no matching event found at source URL")
	}

	req.Tags = resolveFeedTags(req.Tags)
	locationID, err := ensureLocation(q, req.Location)
	if err != nil {
		return "", err
	}

	_, counts, err := createEventFromRequest(q, req, locationID, isPublished, nil)
	if err != nil {
		return "", err
	}
	switch {
	case counts.Updated > 0:
		return outcomeUpdated, nil
	case counts.New > 0:
		return outcomeNew, nil
	default:
		return outcomeUnchanged, nil
	}
}

// matchRecheckEntry picks the parsed entry that corresponds to the
// existing event: by UID first (the strongest signal, same as
// findExistingEvent's tier order), then by URL, then — when the source
// only ever described one event, as is typical for a single-event iCal
// link — the sole entry.
func matchRecheckEntry(reqs []EventCreateRequest, uid, evURL string) (EventCreateRequest, bool) {
	if uid != "" {
		for _, r := range reqs {
			if r.UID == uid {
				return r, true
			}
		}
	}
	if evURL != "" {
		for _, r := range reqs {
			if r.URL == evURL {
				return r, true
			}
		}
	}
	if len(reqs) == 1 {
		return reqs[0], true
	}
	return EventCreateRequest{}, false
}
