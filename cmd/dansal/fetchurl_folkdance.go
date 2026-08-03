package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type folkdanceEvent struct {
	Name         string   `json:"name"`
	Links        []string `json:"links"`
	Start        string   `json:"start"`      // RFC3339 with offset, e.g. "2026-05-13T20:00:00+02:00"
	End          string   `json:"end"`        // RFC3339, optional
	StartDate    string   `json:"start_date"` // date-only fallback, e.g. "2026-05-22"
	EndDate      string   `json:"end_date"`   // date-only fallback
	Country      string   `json:"country"`
	City         string   `json:"city"`
	State        string   `json:"state"` // AU/US region
	Styles       []string `json:"styles"`
	Workshop     bool     `json:"workshop"`
	Social       bool     `json:"social"`
	Bands        []string `json:"bands"`
	Callers      []string `json:"callers"`
	Organisation string   `json:"organisation"`
	Price        string   `json:"price"`
	Details      string   `json:"details"`
	Cancelled    bool     `json:"cancelled"`
	Source       string   `json:"source"` // internal YAML path, ignored
}

func parseFolkdanceTime(datetime, date string) (string, error) {
	if datetime != "" {
		t, err := time.Parse(time.RFC3339, datetime)
		if err != nil {
			return "", err
		}
		return t.UTC().Format(time.RFC3339), nil
	}
	if date != "" {
		t, err := time.Parse("2006-01-02", date)
		if err != nil {
			return "", err
		}
		return t.UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("no time value")
}

// parseFolkdancePrice converts a folkdance.page price string (e.g. "€12-€18",
// "$15", "donation", "10 PLN") into a structured Pricing, or nil when the
// string is empty or unparseable.
func parseFolkdancePrice(s string) *Pricing {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	lower := strings.ToLower(s)
	if lower == "free" {
		return &Pricing{Type: "free"}
	}
	if lower == "donation" {
		return &Pricing{Type: "donation"}
	}

	// stripSym strips a leading currency symbol and returns the remainder plus ISO code.
	stripSym := func(t string) (string, string) {
		switch {
		case strings.HasPrefix(t, "€"):
			return t[len("€"):], "EUR"
		case strings.HasPrefix(t, "£"):
			return t[len("£"):], "GBP"
		case strings.HasPrefix(t, "$"):
			return t[1:], "USD"
		}
		return t, ""
	}

	rest, currency := stripSym(s)

	// Range: "€12-€18" or "$15-$20" — hi may also carry a currency prefix.
	if idx := strings.Index(rest, "-"); idx > 0 {
		hiStr, _ := stripSym(rest[idx+1:])
		lo, errLo := strconv.ParseFloat(rest[:idx], 64)
		hi, errHi := strconv.ParseFloat(hiStr, 64)
		if errLo == nil && errHi == nil {
			return &Pricing{
				Type:     "multiple",
				Currency: currency,
				Prices:   []Price{{Label: "from", Amount: lo}, {Label: "to", Amount: hi}},
			}
		}
	}

	// Single amount with symbol prefix: "€98", "£7.50".
	if currency != "" {
		if amt, err := strconv.ParseFloat(rest, 64); err == nil {
			return &Pricing{Type: "single", Amount: amt, Currency: currency}
		}
	}

	// "10 PLN" style: numeric amount followed by a currency code.
	if parts := strings.Fields(s); len(parts) == 2 {
		if amt, err := strconv.ParseFloat(parts[0], 64); err == nil {
			return &Pricing{Type: "single", Amount: amt, Currency: parts[1]}
		}
	}

	return nil
}

func folkdanceLocationString(city, state, country string) string {
	parts := []string{city}
	if state != "" {
		parts = append(parts, state)
	}
	if country != "" {
		parts = append(parts, country)
	}
	return strings.Join(parts, ", ")
}

// ensureMusician returns the id of an existing musician matching bandname,
// or inserts a new one with just the bandname set.
func ensureMusician(q querier, bandname string) (int64, error) {
	if bandname == "" {
		return 0, nil
	}
	var id int64
	err := q.QueryRow("SELECT id FROM musicians WHERE bandname = ?", bandname).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	result, err := q.Exec("INSERT INTO musicians (bandname) VALUES (?)", bandname)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// parseFolkdanceJSONToRequests converts a folkdance JSON body to
// EventCreateRequests without touching the database. Used by the preview
// endpoint; musician IDs are omitted since ensureMusician requires a DB.
func parseFolkdanceJSONToRequests(body []byte, src FetchSource) ([]EventCreateRequest, error) {
	var payload struct {
		Events []folkdanceEvent `json:"events"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	var reqs []EventCreateRequest
	now := time.Now().UTC()

	for _, fe := range payload.Events {
		if fe.Name == "" {
			continue
		}
		startTime, err := parseFolkdanceTime(fe.Start, fe.StartDate)
		if err != nil {
			continue
		}
		endTime := startTime
		if fe.End != "" {
			endTime, _ = parseFolkdanceTime(fe.End, "")
		} else if fe.EndDate != "" {
			if t, err := time.Parse("2006-01-02", fe.EndDate); err == nil {
				endTime = t.Add(24*time.Hour - time.Second).UTC().Format(time.RFC3339)
			}
		}
		if et, err := time.Parse(time.RFC3339, endTime); err == nil && et.Before(now) {
			continue
		}

		tags := make([]string, 0, len(fe.Styles)+len(src.Tags))
		seen := make(map[string]bool)
		for _, s := range fe.Styles {
			if s != "" && !seen[s] {
				seen[s] = true
				tags = append(tags, s)
			}
		}
		for _, s := range src.Tags {
			if s != "" && !seen[s] {
				seen[s] = true
				tags = append(tags, s)
			}
		}

		var eventURL string
		if len(fe.Links) > 0 {
			eventURL = fe.Links[0]
		}

		reqs = append(reqs, EventCreateRequest{
			Source: src.URL,
			EventWriteRequest: EventWriteRequest{
				Title:          fe.Name,
				Description:    fe.Details,
				StartTime:      startTime,
				EndTime:        endTime,
				HasBall:        fe.Social,
				HasWorkshop:    fe.Workshop,
				IsCancelled:    fe.Cancelled,
				Tags:           tags,
				URL:            eventURL,
				OrganizationID: src.OrganizationID,
				Dances:         src.DanceIDs,
				Pricing:        parseFolkdancePrice(fe.Price),
				Location: EventLocationRequest{
					Location: folkdanceLocationString(fe.City, fe.State, fe.Country),
					Town:     fe.City,
					Country:  fe.Country,
				},
			},
		})
	}
	return reqs, nil
}

func importFromFolkdanceJSON(ctx context.Context, src FetchSource) ([]Event, ImportCounts, error) {
	resp, err := getWithRetry(ctx, safeClient, src.URL)
	if err != nil {
		return nil, ImportCounts{}, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ImportCounts{}, fmt.Errorf("remote returned %d", resp.StatusCode)
	}

	var payload struct {
		Events []folkdanceEvent `json:"events"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&payload); err != nil {
		return nil, ImportCounts{}, fmt.Errorf("parse JSON: %w", err)
	}

	db.Exec("UPDATE fetch_sources SET last_fetched_at = ? WHERE id = ?", time.Now().UTC().Unix(), src.ID)

	td := parseTemplateData(src.TemplateData)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ImportCounts{}, err
	}
	defer tx.Rollback()

	var allEvents []Event
	var counts ImportCounts
	now := time.Now().UTC()

	for _, fe := range payload.Events {
		if fe.Name == "" {
			continue
		}

		startTime, err := parseFolkdanceTime(fe.Start, fe.StartDate)
		if err != nil {
			continue
		}

		// For date-only end values use end-of-day so multi-day events have a
		// sensible duration even when start and end fall on the same calendar date.
		endTime := startTime
		if fe.End != "" {
			endTime, _ = parseFolkdanceTime(fe.End, "")
		} else if fe.EndDate != "" {
			if t, err := time.Parse("2006-01-02", fe.EndDate); err == nil {
				endTime = t.Add(24*time.Hour - time.Second).UTC().Format(time.RFC3339)
			}
		}

		if et, err := time.Parse(time.RFC3339, endTime); err == nil && et.Before(now) {
			continue
		}

		// Merge feed styles with any tags configured on the source.
		tags := make([]string, 0, len(fe.Styles)+len(src.Tags))
		seen := make(map[string]bool)
		for _, s := range fe.Styles {
			if s != "" && !seen[s] {
				seen[s] = true
				tags = append(tags, s)
			}
		}
		for _, s := range src.Tags {
			if s != "" && !seen[s] {
				seen[s] = true
				tags = append(tags, s)
			}
		}

		var eventURL string
		if len(fe.Links) > 0 {
			eventURL = fe.Links[0]
		}

		locStr := folkdanceLocationString(fe.City, fe.State, fe.Country)

		eventReq := EventCreateRequest{
			Source:        src.URL,
			FetchSourceID: src.ID,
			EventWriteRequest: EventWriteRequest{
				Title:       fe.Name,
				Description: fe.Details,
				StartTime:   startTime,
				EndTime:     endTime,
				HasBall:     fe.Social,
				HasWorkshop: fe.Workshop,
				IsCancelled: fe.Cancelled,
				Tags:        tags,
				URL:         eventURL,
				OrganizationID: func() *int {
					if src.OrganizationID != nil {
						return src.OrganizationID
					}
					return ensureOrgByName(fe.Organisation)
				}(),
				Dances:  src.DanceIDs,
				Pricing: parseFolkdancePrice(fe.Price),
				Location: EventLocationRequest{
					Location: locStr,
					Town:     fe.City,
					Country:  fe.Country,
				},
			},
		}

		if err := withEntrySavepoint(tx, func() error {
			var musicianIDs []int
			seenMusician := make(map[string]bool)
			for _, name := range append(fe.Bands, fe.Callers...) {
				if name == "" || seenMusician[name] {
					continue
				}
				seenMusician[name] = true
				id, err := ensureMusician(tx, name)
				if err != nil {
					return fmt.Errorf("ensureMusician %q: %w", name, err)
				}
				if id > 0 {
					musicianIDs = append(musicianIDs, int(id))
				}
			}
			eventReq.Musicians = musicianIDs
			_, err := importSingleEvent(tx, eventReq, td, src.TemplateMode, &counts, &allEvents)
			return err
		}); err != nil {
			counts.Failed++
			logFailedImportEntry(src, eventReq, err)
			continue
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, ImportCounts{}, err
	}
	return allEvents, counts, nil
}

// folkdanceJSONProbe returns true when the URL looks like a folkdance.page JSON feed.
func folkdanceJSONProbe(rawURL string) bool {
	return strings.Contains(rawURL, "folkdance.page") && strings.Contains(rawURL, ".json")
}

// httpContentType fetches only the Content-Type header of a URL.
func httpContentType(rawURL string) string {
	if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	resp, err := safeClient.Head(rawURL)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct)
}

// detectFetchType infers the fetch type from the URL and a HEAD request when
// the caller has not supplied an explicit type.
func detectFetchType(rawURL string) string {
	if gancioJSONProbe(rawURL) {
		return "json"
	}
	if folkdanceJSONProbe(rawURL) {
		return "json"
	}
	if tecJSONProbe(rawURL) {
		return "json"
	}
	lower := strings.ToLower(rawURL)
	if strings.Contains(lower, "rss") || strings.Contains(lower, "atom") ||
		strings.HasSuffix(lower, ".xml") || strings.Contains(lower, "/feed") {
		return "rss"
	}
	ct := httpContentType(rawURL)
	switch ct {
	case "application/json":
		return "json"
	case "application/rss+xml", "application/atom+xml", "application/xml", "text/xml":
		return "rss"
	default:
		return "ical"
	}
}

// validFetchType returns true for recognised fetch type strings.
// "json" is the unified public type; "folkdance-json" and "gancio-json" are kept as aliases for backwards compatibility.
func validFetchType(t string) bool {
	return t == "ical" || t == "json" || t == "folkdance-json" || t == "gancio-json" || t == "rss" || t == "kufer"
}
