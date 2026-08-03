package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
)

// kuferKnrRe extracts course numbers ("knr") from ics.php?knr=... links
// embedded in a Kufer search-result page.
var kuferKnrRe = regexp.MustCompile(`knr=([A-Za-z0-9]+)`)

// kuferSearchConvention is one known shape of a Kufer VHS course-search
// endpoint. The exact URL/method/params are chosen by each VHS install (the
// search form lives on a site-specific TYPO3 page), so several conventions
// are probed in turn against the derived base domain (#932).
type kuferSearchConvention struct {
	name  string
	build func(base *url.URL, keyword string) (*http.Request, error)
}

var kuferSearchConventions = []kuferSearchConvention{
	{
		name: "kurssuche-get",
		build: func(base *url.URL, keyword string) (*http.Request, error) {
			u := *base
			u.Path = "/kurssuche/suche"
			q := url.Values{}
			q.Set("suchesetzen", "true")
			q.Set("kfs_stichwort_schlagwort", keyword)
			u.RawQuery = q.Encode()
			return http.NewRequest(http.MethodGet, u.String(), nil)
		},
	},
	{
		name: "versteckte-seiten-kurssuche-get",
		build: func(base *url.URL, keyword string) (*http.Request, error) {
			u := *base
			u.Path = "/versteckte-seiten/kurssuche/"
			q := url.Values{}
			q.Set("suchesetzen", "true")
			q.Set("kfs_stichwort_schlagwort", keyword)
			u.RawQuery = q.Encode()
			return http.NewRequest(http.MethodGet, u.String(), nil)
		},
	},
	{
		name: "index-id47-kathaupt26-get",
		build: func(base *url.URL, keyword string) (*http.Request, error) {
			u := *base
			u.Path = "/index.php"
			q := url.Values{}
			q.Set("id", "47")
			q.Set("kathaupt", "26")
			q.Set("clearallkatfilter", "true")
			q.Set("suchesetzen", "true")
			q.Set("kfs_stichwort_schlagwort", keyword)
			u.RawQuery = q.Encode()
			return http.NewRequest(http.MethodGet, u.String(), nil)
		},
	},
	{
		name: "index-id15-kathaupt6-post",
		build: func(base *url.URL, keyword string) (*http.Request, error) {
			u := *base
			u.Path = "/index.php"
			q := url.Values{}
			q.Set("id", "15")
			q.Set("kathaupt", "6")
			q.Set("suchesetzen", "true")
			u.RawQuery = q.Encode()
			body := url.Values{}
			body.Set("kfs_stichwort_schlagwort", keyword)
			req, err := http.NewRequest(http.MethodPost, u.String(), strings.NewReader(body.Encode()))
			if err == nil {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			return req, err
		},
	},
}

// kuferBaseURL derives the scheme+host of a Kufer VHS site from any URL on
// that site, e.g. an ics.php?knr=... example event URL.
func kuferBaseURL(exampleURL string) (*url.URL, error) {
	u, err := url.Parse(exampleURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("cannot derive base domain from %q", exampleURL)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

// kuferSearchKnrs runs a single keyword search against base, trying the
// manual override first (if provided), then each known convention in turn.
// Returns the set of course numbers ("knr") found in the result page.
func kuferSearchKnrs(ctx context.Context, base *url.URL, cfg KuferConfig, keyword string) ([]string, error) {
	tryReq := func(req *http.Request) ([]string, bool) {
		req = req.WithContext(ctx)
		resp, err := safeClient.Do(req)
		if err != nil {
			return nil, false
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, false
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
		if err != nil {
			return nil, false
		}
		matches := kuferKnrRe.FindAllStringSubmatch(string(body), -1)
		if len(matches) == 0 {
			return nil, false
		}
		seen := make(map[string]bool)
		var knrs []string
		for _, m := range matches {
			if !seen[m[1]] {
				seen[m[1]] = true
				knrs = append(knrs, m[1])
			}
		}
		return knrs, true
	}

	if cfg.SearchURL != "" {
		method := cfg.SearchMethod
		if method == "" {
			method = http.MethodGet
		}
		u, err := url.Parse(cfg.SearchURL)
		if err != nil {
			return nil, fmt.Errorf("invalid manual search_url: %w", err)
		}
		var req *http.Request
		if method == http.MethodPost {
			q := u.Query()
			q.Set("kfs_stichwort_schlagwort", keyword)
			body := url.Values{"kfs_stichwort_schlagwort": {keyword}}
			req, err = http.NewRequest(http.MethodPost, u.String(), strings.NewReader(body.Encode()))
			if err == nil {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
		} else {
			q := u.Query()
			q.Set("kfs_stichwort_schlagwort", keyword)
			u.RawQuery = q.Encode()
			req, err = http.NewRequest(http.MethodGet, u.String(), nil)
		}
		if err != nil {
			return nil, err
		}
		if knrs, ok := tryReq(req); ok {
			return knrs, nil
		}
		return nil, fmt.Errorf("manual search_url returned no matching courses for keyword %q", keyword)
	}

	for _, conv := range kuferSearchConventions {
		req, err := conv.build(base, keyword)
		if err != nil {
			continue
		}
		if knrs, ok := tryReq(req); ok {
			return knrs, nil
		}
	}
	return nil, fmt.Errorf("no known Kufer search-endpoint convention worked for %s", base.String())
}

// kuferFetchCourseEvents fetches and parses the per-course iCal export
// (ics.php?knr=<knr>) for a single course, returning EventCreateRequests
// (a course can expand to multiple sessions/VEVENTs).
func kuferFetchCourseEvents(ctx context.Context, base *url.URL, knr string, src FetchSource) ([]EventCreateRequest, error) {
	icsURL := *base
	icsURL.Path = "/fileadmin/kuferweb/webbasys/ics.php"
	q := url.Values{}
	q.Set("knr", knr)
	icsURL.RawQuery = q.Encode()

	resp, err := getWithRetry(ctx, safeClient, icsURL.String())
	if err != nil {
		return nil, fmt.Errorf("fetch knr=%s: %w", knr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("knr=%s: remote returned %d", knr, resp.StatusCode)
	}
	cal, err := ics.ParseCalendar(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, fmt.Errorf("knr=%s: parse iCal: %w", knr, err)
	}

	now := time.Now().UTC()
	var reqs []EventCreateRequest
	for _, vevent := range cal.Events() {
		prop := func(p ics.ComponentProperty) string {
			if v := vevent.GetProperty(p); v != nil {
				return v.Value
			}
			return ""
		}
		startT, err := vevent.GetStartAt()
		if err != nil {
			continue
		}
		endT := startT
		if et, err := vevent.GetEndAt(); err == nil {
			endT = et
		} else if durStr := prop(ics.ComponentPropertyDuration); durStr != "" {
			if d, err := parseICalDuration(durStr); err == nil {
				endT = startT.Add(d)
			}
		}
		title := prop(ics.ComponentPropertySummary)
		if title == "" {
			continue
		}
		baseUID := prop(ics.ComponentPropertyUniqueId)
		if baseUID == "" {
			baseUID = fmt.Sprintf("kufer-%s-%s", knr, startT.UTC().Format(time.RFC3339))
		}

		occs, _ := expandRRuleOccurrences(vevent, startT, endT)
		if occs == nil {
			occs = [][2]time.Time{{startT, endT}}
		}
		for _, occ := range occs {
			if occ[1].Before(now) {
				continue
			}
			uid := baseUID
			if len(occs) > 1 && !occ[0].Equal(startT) {
				uid = fmt.Sprintf("%s_%d", baseUID, occ[0].UTC().Unix())
			}
			reqs = append(reqs, EventCreateRequest{
				UID:           uid,
				Source:        src.URL,
				FetchSourceID: src.ID,
				EventWriteRequest: EventWriteRequest{
					Title:          title,
					StartTime:      occ[0].UTC().Format(time.RFC3339),
					EndTime:        occ[1].UTC().Format(time.RFC3339),
					Tags:           src.Tags,
					OrganizationID: src.OrganizationID,
					Dances:         src.DanceIDs,
					Location:       parseICalLocation(vevent),
				},
			})
		}
	}
	return reqs, nil
}

// importFromKuferSource discovers dance-relevant courses at a Kufer VHS site
// via keyword search, then imports each course's per-session events through
// the existing insertEvent dedup pipeline (#932).
func importFromKuferSource(ctx context.Context, src FetchSource) ([]Event, ImportCounts, error) {
	var cfg KuferConfig
	if src.KuferConfig != "" {
		if err := json.Unmarshal([]byte(src.KuferConfig), &cfg); err != nil {
			return nil, ImportCounts{}, fmt.Errorf("invalid kufer_config: %w", err)
		}
	}
	if len(cfg.Keywords) == 0 {
		return nil, ImportCounts{}, fmt.Errorf("kufer source requires at least one search keyword")
	}

	base, err := kuferBaseURL(src.URL)
	if err != nil {
		return nil, ImportCounts{}, err
	}

	seenKnr := make(map[string]bool)
	for _, kw := range cfg.Keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		knrs, err := kuferSearchKnrs(ctx, base, cfg, kw)
		if err != nil {
			// One failing keyword shouldn't sink the whole source; try the rest.
			continue
		}
		for _, knr := range knrs {
			seenKnr[knr] = true
		}
	}
	if len(seenKnr) == 0 {
		return nil, ImportCounts{}, fmt.Errorf("no courses found for any configured keyword at %s", base.String())
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
	for knr := range seenKnr {
		reqs, err := kuferFetchCourseEvents(ctx, base, knr, src)
		if err != nil {
			counts.Failed++
			log.Printf("kufer import: knr=%s: %v", knr, err)
			continue
		}
		for _, eventReq := range reqs {
			if err := withEntrySavepoint(tx, func() error {
				_, err := importSingleEvent(tx, eventReq, td, src.TemplateMode, &counts, &allEvents)
				return err
			}); err != nil {
				counts.Failed++
				logFailedImportEntry(src, eventReq, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, ImportCounts{}, err
	}
	return allEvents, counts, nil
}
