package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type tecEvent struct {
	ID       int    `json:"id"`
	GlobalID string `json:"global_id"`
	Title    string `json:"title"`
	// Description is HTML; stripped to plain text before storage.
	Description string `json:"description"`
	URL         string `json:"url"`
	StartDate   string `json:"start_date"` // "2026-05-31 19:00:00"
	EndDate     string `json:"end_date"`
	Timezone    string `json:"timezone"`
	Cost        string `json:"cost"`
	Venue       struct {
		Venue   string `json:"venue"`
		Address string `json:"address"`
		City    string `json:"city"`
		Zip     string `json:"zip"`
	} `json:"venue"`
	Categories []struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"categories"`
	Organizer []struct {
		Organizer string `json:"organizer"`
	} `json:"organizer"`
}

type tecResponse struct {
	Events     []tecEvent `json:"events"`
	TotalPages int        `json:"total_pages"`
}

var htmlTagRE = regexp.MustCompile(`<[^>]+>`)

func tecStripHTML(s string) string {
	s = htmlTagRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	return strings.TrimSpace(s)
}

// tecJSONProbe returns true when the URL looks like a WordPress The Events Calendar REST API endpoint.
func tecJSONProbe(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	return strings.Contains(lower, "wp-json/tribe/events") ||
		strings.Contains(lower, "wp-json/the-events-calendar")
}

func parseTECTime(dateStr, timezone string) (string, error) {
	loc := time.UTC
	if timezone != "" {
		if l, err := time.LoadLocation(timezone); err == nil {
			loc = l
		}
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", dateStr, loc)
	if err != nil {
		return "", fmt.Errorf("parse TEC time %q: %w", dateStr, err)
	}
	return t.UTC().Format(time.RFC3339), nil
}

func importFromTECJSON(src FetchSource) ([]Event, bool, error) {
	var allTECEvents []tecEvent
	for page := 1; ; page++ {
		u, err := url.Parse(src.URL)
		if err != nil {
			return nil, false, fmt.Errorf("parse URL: %w", err)
		}
		q := u.Query()
		q.Set("page", fmt.Sprintf("%d", page))
		q.Set("per_page", "50")
		u.RawQuery = q.Encode()

		resp, err := fetchClient.Get(u.String())
		if err != nil {
			return nil, false, fmt.Errorf("fetch page %d: %w", page, err)
		}
		var payload tecResponse
		decErr := json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if decErr != nil {
			return nil, false, fmt.Errorf("parse page %d: %w", page, decErr)
		}
		allTECEvents = append(allTECEvents, payload.Events...)
		if page >= payload.TotalPages || payload.TotalPages == 0 {
			break
		}
	}

	db.Exec("UPDATE fetch_sources SET last_fetched_at = ? WHERE id = ?", time.Now().UTC().Unix(), src.ID)

	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	var allEvents []Event
	allCreated := true

	for _, te := range allTECEvents {
		if te.Title == "" || te.StartDate == "" {
			continue
		}

		startTime, err := parseTECTime(te.StartDate, te.Timezone)
		if err != nil {
			continue
		}
		endTime := startTime
		if te.EndDate != "" {
			if et, err2 := parseTECTime(te.EndDate, te.Timezone); err2 == nil {
				endTime = et
			}
		}
		if et, err2 := time.Parse(time.RFC3339, endTime); err2 == nil && et.Before(now) {
			continue
		}

		tags := make([]string, 0, len(te.Categories)+len(src.Tags))
		seen := make(map[string]bool)
		for _, c := range te.Categories {
			slug := c.Slug
			if slug == "" {
				slug = strings.ToLower(strings.ReplaceAll(c.Name, " ", "-"))
			}
			if slug != "" && !seen[slug] {
				seen[slug] = true
				tags = append(tags, slug)
			}
		}
		for _, t := range src.Tags {
			if t != "" && !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}

		var orgID *int
		if src.OrganizationID != nil {
			orgID = src.OrganizationID
		} else if len(te.Organizer) > 0 && te.Organizer[0].Organizer != "" {
			orgID = ensureOrgByName(te.Organizer[0].Organizer)
		}

		uid := te.GlobalID
		if uid == "" {
			host := strings.TrimPrefix(strings.TrimPrefix(src.URL, "https://"), "http://")
			uid = fmt.Sprintf("tec-%d@%s", te.ID, host)
		}

		req := EventCreateRequest{
			UID:           uid,
			Source:        src.URL,
			FetchSourceID: src.ID,
			EventWriteRequest: EventWriteRequest{
				Title:          te.Title,
				Description:    tecStripHTML(te.Description),
				StartTime:      startTime,
				EndTime:        endTime,
				URL:            te.URL,
				Tags:           tags,
				OrganizationID: orgID,
				Dances:         src.DanceIDs,
				Location: EventLocationRequest{
					Location: te.Venue.Venue,
					Address:  te.Venue.Address,
					Town:     te.Venue.City,
					Zipcode:  te.Venue.Zip,
				},
			},
		}

		if src.TemplateID != nil {
			if td, err := loadTemplateForSource(*src.TemplateID); err == nil && td != nil {
				applyTemplateToRequest(&req, *td, src.TemplateMode)
			}
		}

		locationID, err := ensureLocation(tx, req.Location)
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
		allEvents = append(allEvents, evs...)
	}

	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return allEvents, allCreated, nil
}
