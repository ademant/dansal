package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// tecVenueData holds the fields we care about from a TEC venue object.
type tecVenueData struct {
	Venue   string `json:"venue"`
	Address string `json:"address"`
	City    string `json:"city"`
	Zip     string `json:"zip"`
}

// tecVenueField handles the WordPress TEC API quirk where "venue" is an empty
// JSON array [] when unset, and an object when set.
type tecVenueField struct{ tecVenueData }

func (v *tecVenueField) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '[' {
		return nil // empty array — no venue set
	}
	return json.Unmarshal(b, &v.tecVenueData)
}

type tecEvent struct {
	ID       int    `json:"id"`
	GlobalID string `json:"global_id"`
	Title    string `json:"title"`
	// Description is HTML; stripped to plain text before storage.
	Description string        `json:"description"`
	URL         string        `json:"url"`
	StartDate   string        `json:"start_date"` // "2026-05-31 19:00:00"
	EndDate     string        `json:"end_date"`
	Timezone    string        `json:"timezone"`
	Cost        string        `json:"cost"`
	Venue       tecVenueField `json:"venue"`
	Categories  []struct {
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
var htmlNumericEntityRE = regexp.MustCompile(`&#(\d+);`)

func tecStripHTML(s string) string {
	s = htmlTagRE.ReplaceAllString(s, "")
	s = htmlNumericEntityRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := htmlNumericEntityRE.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		n, err := strconv.Atoi(sub[1])
		if err != nil || n < 0 || n > 0x10FFFF {
			return m
		}
		return string(rune(n))
	})
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

// tecEventToRequest converts a single tecEvent to an EventCreateRequest.
// orgID overrides organizer lookup when non-nil (used during actual import).
// For preview (no DB), pass orgID=src.OrganizationID and allowOrgLookup=false.
func tecEventToRequest(te tecEvent, src FetchSource, allowOrgLookup bool) (EventCreateRequest, bool) {
	if te.Title == "" || te.StartDate == "" {
		return EventCreateRequest{}, false
	}
	startTime, err := parseTECTime(te.StartDate, te.Timezone)
	if err != nil {
		return EventCreateRequest{}, false
	}
	endTime := startTime
	if te.EndDate != "" {
		if et, err2 := parseTECTime(te.EndDate, te.Timezone); err2 == nil {
			endTime = et
		}
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
	} else if allowOrgLookup && len(te.Organizer) > 0 && te.Organizer[0].Organizer != "" {
		orgID = ensureOrgByName(te.Organizer[0].Organizer)
	}

	uid := te.GlobalID
	if uid == "" {
		host := strings.TrimPrefix(strings.TrimPrefix(src.URL, "https://"), "http://")
		uid = fmt.Sprintf("tec-%d@%s", te.ID, host)
	}

	return EventCreateRequest{
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
				Location: tecStripHTML(te.Venue.Venue),
				Address:  tecStripHTML(te.Venue.Address),
				Town:     tecStripHTML(te.Venue.City),
				Zipcode:  te.Venue.Zip,
			},
		},
	}, true
}

// parseTECJSONToRequests converts a single-page TEC JSON body to EventCreateRequests
// without touching the database. Used by the preview endpoint.
func parseTECJSONToRequests(body []byte, src FetchSource) ([]EventCreateRequest, error) {
	var payload tecResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse TEC JSON: %w", err)
	}
	now := time.Now().UTC()
	var reqs []EventCreateRequest
	for _, te := range payload.Events {
		req, ok := tecEventToRequest(te, src, false)
		if !ok {
			continue
		}
		if et, err := time.Parse(time.RFC3339, req.EndTime); err == nil && et.Before(now) {
			continue
		}
		reqs = append(reqs, req)
	}
	return reqs, nil
}

func importFromTECJSON(ctx context.Context, src FetchSource) ([]Event, ImportCounts, error) {
	var allTECEvents []tecEvent
	for page := 1; ; page++ {
		u, err := url.Parse(src.URL)
		if err != nil {
			return nil, ImportCounts{}, fmt.Errorf("parse URL: %w", err)
		}
		q := u.Query()
		q.Set("page", fmt.Sprintf("%d", page))
		q.Set("per_page", "50")
		u.RawQuery = q.Encode()

		resp, err := getWithRetry(ctx, safeClient, u.String())
		if err != nil {
			return nil, ImportCounts{}, fmt.Errorf("fetch page %d: %w", page, err)
		}
		var payload tecResponse
		decErr := json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if decErr != nil {
			return nil, ImportCounts{}, fmt.Errorf("parse page %d: %w", page, decErr)
		}
		allTECEvents = append(allTECEvents, payload.Events...)
		if page >= payload.TotalPages || payload.TotalPages == 0 {
			break
		}
	}

	db.Exec("UPDATE fetch_sources SET last_fetched_at = ? WHERE id = ?", time.Now().UTC().Unix(), src.ID)

	td := parseTemplateData(src.TemplateData)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ImportCounts{}, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	var allEvents []Event
	var counts ImportCounts

	for _, te := range allTECEvents {
		req, ok := tecEventToRequest(te, src, true)
		if !ok {
			continue
		}
		if et, err2 := time.Parse(time.RFC3339, req.EndTime); err2 == nil && et.Before(now) {
			continue
		}
		if err := withEntrySavepoint(tx, func() error {
			_, err := importSingleEvent(tx, req, td, src.TemplateMode, &counts, &allEvents)
			return err
		}); err != nil {
			counts.Failed++
			logFailedImportEntry(src, req, err)
			continue
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, ImportCounts{}, err
	}
	return allEvents, counts, nil
}
