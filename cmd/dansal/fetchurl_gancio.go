package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// gancioStringOrSlice unmarshals a JSON value that may be either a string
// or an array of strings (Gancio's online_locations field varies by event).
type gancioStringOrSlice []string

func (s *gancioStringOrSlice) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '[' {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*s = arr
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	if str != "" {
		*s = []string{str}
	} else {
		*s = nil
	}
	return nil
}

type gancioEvent struct {
	ID              int64                `json:"id"`
	Title           string               `json:"title"`
	Slug            string               `json:"slug"`
	StartDatetime   int64                `json:"start_datetime"`
	EndDatetime     int64                `json:"end_datetime"`
	Tags            []string             `json:"tags"`
	OnlineLocations gancioStringOrSlice  `json:"online_locations"`
	MultiDate       bool                 `json:"multidate"`
	ParentID        *int64               `json:"parentId"`
	Place           struct {
		ID        int64    `json:"id"`
		Name      string   `json:"name"`
		Address   string   `json:"address"`
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	} `json:"place"`
}

// gancioBaseURL returns the scheme+host of a Gancio API URL so that event
// page URLs can be constructed as <base>/event/<slug>.
func gancioBaseURL(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return ""
}

// parseGancioAddress splits a Gancio place address string into town and
// country.  Gancio typically stores addresses like
// "Street, ZIP City, Country" (locale-dependent).  We take the last
// comma-separated segment as country and the second-to-last (minus any
// leading ZIP code) as town.
func parseGancioAddress(address string) (town, country string) {
	parts := strings.Split(address, ", ")
	if len(parts) == 0 {
		return "", ""
	}
	country = strings.TrimSpace(parts[len(parts)-1])
	if len(parts) >= 2 {
		raw := strings.TrimSpace(parts[len(parts)-2])
		// Strip leading ZIP code: "65929 Frankfurt am Main" → "Frankfurt am Main"
		if idx := strings.IndexByte(raw, ' '); idx > 0 {
			if _, err := strconv.Atoi(raw[:idx]); err == nil {
				raw = strings.TrimSpace(raw[idx+1:])
			}
		}
		town = raw
	}
	return town, country
}

func gancioEventsToRequests(events []gancioEvent, src FetchSource) []EventCreateRequest {
	base := gancioBaseURL(src.URL)
	now := time.Now().UTC()
	var reqs []EventCreateRequest

	for _, ge := range events {
		if ge.Title == "" || ge.StartDatetime == 0 {
			continue
		}

		startTime := time.Unix(ge.StartDatetime, 0).UTC()
		endTime := startTime
		if ge.EndDatetime > ge.StartDatetime {
			endTime = time.Unix(ge.EndDatetime, 0).UTC()
		}
		if endTime.Before(now) {
			continue
		}

		town, country := parseGancioAddress(ge.Place.Address)

		tags := make([]string, 0, len(ge.Tags)+len(src.Tags))
		seen := make(map[string]bool)
		for _, t := range ge.Tags {
			if t != "" && !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
		for _, t := range src.Tags {
			if t != "" && !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}

		var eventURL string
		if ge.Slug != "" && base != "" {
			eventURL = base + "/event/" + ge.Slug
		}

		var bookingURL string
		if len(ge.OnlineLocations) > 0 {
			bookingURL = ge.OnlineLocations[0]
		}

		uid := fmt.Sprintf("%d@%s", ge.ID, strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://"))

		req := EventCreateRequest{
			UID:    uid,
			Source: src.URL,
			EventWriteRequest: EventWriteRequest{
				Title:          ge.Title,
				StartTime:      startTime.Format(time.RFC3339),
				EndTime:        endTime.Format(time.RFC3339),
				Tags:           tags,
				URL:            eventURL,
				BookingURL:     bookingURL,
				OrganizationID: src.OrganizationID,
				Dances:         src.DanceIDs,
				Location: EventLocationRequest{
					Location:  ge.Place.Name,
					Address:   ge.Place.Address,
					Town:      town,
					Country:   country,
					Latitude:  ge.Place.Latitude,
					Longitude: ge.Place.Longitude,
				},
			},
		}
		reqs = append(reqs, req)
	}
	return reqs
}

// parseGancioJSONToRequests converts a Gancio /api/events JSON body to
// EventCreateRequests without touching the database. Used by the preview endpoint.
func parseGancioJSONToRequests(body []byte, src FetchSource) ([]EventCreateRequest, error) {
	var events []gancioEvent
	if err := json.Unmarshal(body, &events); err != nil {
		return nil, fmt.Errorf("parse gancio JSON: %w", err)
	}
	return gancioEventsToRequests(events, src), nil
}

func importFromGancioJSON(src FetchSource) ([]Event, bool, error) {
	resp, err := fetchClient.Get(src.URL)
	if err != nil {
		return nil, false, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("remote returned %d", resp.StatusCode)
	}

	var events []gancioEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, false, fmt.Errorf("parse gancio JSON: %w", err)
	}

	db.Exec("UPDATE fetch_sources SET last_fetched_at = ? WHERE id = ?", time.Now().UTC().Unix(), src.ID)

	var td *templateImportData
	if src.TemplateID != nil {
		td, _ = loadTemplateForSource(*src.TemplateID)
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	base := gancioBaseURL(src.URL)
	now := time.Now().UTC()
	var allEvents []Event
	allCreated := true

	for _, ge := range events {
		if ge.Title == "" || ge.StartDatetime == 0 {
			continue
		}

		startTime := time.Unix(ge.StartDatetime, 0).UTC()
		endTime := startTime
		if ge.EndDatetime > ge.StartDatetime {
			endTime = time.Unix(ge.EndDatetime, 0).UTC()
		}
		if endTime.Before(now) {
			continue
		}

		town, country := parseGancioAddress(ge.Place.Address)

		tags := make([]string, 0, len(ge.Tags)+len(src.Tags))
		seen := make(map[string]bool)
		for _, t := range ge.Tags {
			if t != "" && !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
		for _, t := range src.Tags {
			if t != "" && !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}

		var eventURL string
		if ge.Slug != "" && base != "" {
			eventURL = base + "/event/" + ge.Slug
		}

		var bookingURL string
		if len(ge.OnlineLocations) > 0 {
			bookingURL = ge.OnlineLocations[0]
		}

		uid := fmt.Sprintf("%d@%s", ge.ID, strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://"))

		req := EventCreateRequest{
			UID:           uid,
			Source:        src.URL,
			FetchSourceID: src.ID,
			EventWriteRequest: EventWriteRequest{
				Title:          ge.Title,
				StartTime:      startTime.Format(time.RFC3339),
				EndTime:        endTime.Format(time.RFC3339),
				Tags:           tags,
				URL:            eventURL,
				BookingURL:     bookingURL,
				OrganizationID: src.OrganizationID,
				Dances:         src.DanceIDs,
				Location: EventLocationRequest{
					Location:  ge.Place.Name,
					Address:   ge.Place.Address,
					Town:      town,
					Country:   country,
					Latitude:  ge.Place.Latitude,
					Longitude: ge.Place.Longitude,
				},
			},
		}

		if td != nil {
			applyTemplateToRequest(&req, *td, src.TemplateMode)
		}

		locationID, err := resolveTemplateLocation(tx, req.Location, td)
		if err != nil {
			return nil, false, err
		}

		evs, created, err := createEventFromRequest(tx, req, locationID, true, nil)
		if err != nil {
			return nil, false, err
		}
		if !created {
			allCreated = false
		}
		if td != nil {
			for _, ev := range evs {
				applyTemplateTimetable(tx, ev.ID, td.Timetable, src.TemplateMode)
			}
		}
		allEvents = append(allEvents, evs...)
	}

	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return allEvents, allCreated, nil
}

// gancioJSONProbe returns true when the URL looks like a Gancio API events endpoint.
func gancioJSONProbe(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	return strings.Contains(lower, "/api/events")
}
