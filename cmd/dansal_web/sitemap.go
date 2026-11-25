package main

import (
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"github.com/ademant/dansal/internal/logging"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// The sitemap is an index (#1900) pointing to two child sitemaps:
// /sitemap-current.xml holds all non-event pages plus events that are upcoming
// or started within the last sitemapRecentMonths; /sitemap-archive.xml holds
// older events and cancelled events. Both stay reachable: no event is noindexed.
const (
	sitemapIndexName    = "index"
	sitemapCurrentName  = "current"
	sitemapArchiveName  = "archive"
	sitemapRecentMonths = 12
)

// sitemapXML is the root element of a sitemap document.
type sitemapXML struct {
	XMLName xml.Name     `xml:"urlset"`
	NS      string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

// sitemapIndexXML is the root element of a sitemap index (#1900).
type sitemapIndexXML struct {
	XMLName  xml.Name     `xml:"sitemapindex"`
	NS       string       `xml:"xmlns,attr"`
	Sitemaps []sitemapRef `xml:"sitemap"`
}

type sitemapRef struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// sitemapURL has no changefreq: Google ignores it (#1900).
type sitemapURL struct {
	Loc      string `xml:"loc"`
	LastMod  string `xml:"lastmod,omitempty"`
	Priority string `xml:"priority,omitempty"`
}

// sitemapPart is one generated document with its validator.
type sitemapPart struct {
	data []byte
	etag string
}

type sitemapCache struct {
	mu        sync.Mutex
	parts     map[string]sitemapPart
	fetchedAt time.Time
}

var smCache sitemapCache

const sitemapTTL = 30 * time.Minute

// load returns the named sitemap document, rebuilding all of them when the
// cache is older than sitemapTTL. The fetch time bounds freshness and serves as
// Last-Modified (#1129).
func (c *sitemapCache) load(r *http.Request, cfg *Config, client *DansalClient, name string) (sitemapPart, time.Time, error) {
	c.mu.Lock()
	if time.Since(c.fetchedAt) < sitemapTTL {
		if p, ok := c.parts[name]; ok {
			fetchedAt := c.fetchedAt
			c.mu.Unlock()
			return p, fetchedAt, nil
		}
	}
	c.mu.Unlock()

	parts, err := buildSitemaps(r, cfg, client)
	if err != nil {
		return sitemapPart{}, time.Time{}, err
	}
	fetchedAt := time.Now()
	c.mu.Lock()
	c.parts = parts
	c.fetchedAt = fetchedAt
	c.mu.Unlock()
	return parts[name], fetchedAt, nil
}

func sitemapHandler(cfg *Config, client *DansalClient, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		part, fetchedAt, err := smCache.load(r, cfg, client, name)
		if err != nil {
			http.Error(w, "could not generate sitemap", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=1800")
		w.Header().Set("ETag", part.etag)
		if r.Header.Get("If-None-Match") == part.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if checkLastModified(w, r, fetchedAt) {
			return
		}
		w.Write(part.data)
	}
}

// buildSitemaps generates the index and both child sitemaps in one pass, so
// they always agree on the event split.
func buildSitemaps(r *http.Request, cfg *Config, client *DansalClient) (map[string]sitemapPart, error) {
	base := cfg.publicBaseURL()
	today := time.Now().UTC().Format("2006-01-02")
	ctx := r.Context()
	epoch := time.Unix(0, 0)

	urls := []sitemapURL{
		{Loc: base + "/", LastMod: today, Priority: "1.0"},
		{Loc: base + "/musicians", Priority: "0.6"},
		{Loc: base + "/instructors", Priority: "0.6"},
		{Loc: base + "/organizations", Priority: "0.6"},
		{Loc: base + "/festivals", Priority: "0.6"},
	}

	// /board — lastmod is the newest active post's created_at (daily changing).
	boardPosts, bErr := client.GetAllContactPosts(ctx, url.Values{})
	if bErr != nil {
		logging.Errorf("sitemap: could not load board posts: %v", bErr)
	}
	boardLastMod := today
	for _, p := range boardPosts {
		if t, err := parseUnixOrRFC3339(p.CreatedAt); err == nil && t.After(epoch) {
			d := t.UTC().Format("2006-01-02")
			if d > boardLastMod {
				boardLastMod = d
			}
		}
	}
	urls = append(urls, sitemapURL{Loc: base + "/board", LastMod: boardLastMod, Priority: "0.7"})

	// Static/legal pages
	for _, page := range []string{"/impressum", "/privacy", "/terms"} {
		urls = append(urls, sitemapURL{Loc: base + page, Priority: "0.3"})
	}

	// Events — all published (past + future) so archived pages stay indexed,
	// split between the two child sitemaps below.
	events, err := client.GetAllEvents(ctx)
	if err != nil {
		logging.Errorf("sitemap: could not load events: %v", err)
	}
	now := time.Now()
	nowUnix := now.Unix()
	recentFrom := now.AddDate(0, -sitemapRecentMonths, 0)

	// Entities that have events, and the newest event change per venue (#1895).
	eventLocs := map[int]bool{}
	locChanged := map[int]string{}
	eventMusicians := map[int]bool{}
	eventInstructors := map[int]bool{}

	var currentEvents, archiveEvents []sitemapURL
	for _, e := range events {
		changed := ""
		if e.ChangedAt != "" && e.ChangedAt != "0" {
			if t, err := parseUnixOrRFC3339(e.ChangedAt); err == nil && t.After(epoch) {
				changed = t.UTC().Format("2006-01-02")
			}
		}
		for _, id := range eventLocationIDs(e) {
			eventLocs[id] = true
			if changed > locChanged[id] {
				locChanged[id] = changed
			}
		}
		for _, m := range e.Musicians {
			eventMusicians[m.ID] = true
		}
		for _, ins := range e.Instructors {
			eventInstructors[ins.ID] = true
		}

		lastMod := ""
		// BoardLatestAt (#1722): a new board post is fresh content; use it when
		// it's newer than the last event edit so search engines re-crawl promptly.
		if e.BoardLatestAt > 0 {
			lastMod = time.Unix(e.BoardLatestAt, 0).UTC().Format("2006-01-02")
		}
		if changed > lastMod {
			lastMod = changed
		}
		if lastMod == "" && e.CreatedAt != "" {
			if t, err := parseUnixOrRFC3339(e.CreatedAt); err == nil && t.After(epoch) {
				lastMod = t.UTC().Format("2006-01-02")
			}
		}
		// Priority: upcoming events rank higher than past ones; cancelled lowest.
		priority := "0.8"
		st, stErr := parseUnixOrRFC3339(e.StartTime)
		if e.IsCancelled {
			priority = "0.3"
		} else if stErr == nil && st.Unix() < nowUnix {
			priority = "0.4"
		}
		u := sitemapURL{
			Loc:      base + fmt.Sprintf("/events/%d", e.ID),
			LastMod:  lastMod,
			Priority: priority,
		}
		// Cancelled events and events older than the recent window go to the archive.
		if e.IsCancelled || (stErr == nil && st.Before(recentFrom)) {
			archiveEvents = append(archiveEvents, u)
		} else {
			currentEvents = append(currentEvents, u)
		}
	}

	// Locations (#1895): a venue is listed when it has events or its own notes;
	// a room only when it has events of its own; a building also when one of its
	// rooms has events.
	locs, err := client.GetLocations(ctx)
	if err != nil {
		logging.Errorf("sitemap: could not load locations: %v", err)
	}
	roomWithEvents := map[int]bool{}
	for _, l := range locs {
		if l.ParentID != nil && eventLocs[l.ID] {
			roomWithEvents[*l.ParentID] = true
		}
	}
	for _, l := range locs {
		if l.ParentID != nil {
			if !eventLocs[l.ID] {
				continue
			}
		} else if !eventLocs[l.ID] && l.NotesMd == "" && !roomWithEvents[l.ID] {
			continue
		}
		urls = append(urls, sitemapURL{
			Loc:      base + fmt.Sprintf("/location/%d", l.ID),
			LastMod:  locChanged[l.ID],
			Priority: "0.6",
		})
	}

	// Tags — category landing pages for long-tail search traffic.
	tags, err := client.GetTags(ctx)
	if err != nil {
		logging.Errorf("sitemap: could not load tags: %v", err)
	}
	for _, t := range tags {
		urls = append(urls, sitemapURL{Loc: base + "/tags/" + t.Slug, Priority: "0.6"})
	}

	// Cities — hub pages for each town with upcoming events.
	cities, err := client.GetCities(ctx, false)
	if err != nil {
		logging.Errorf("sitemap: could not load cities: %v", err)
	}
	for _, c := range cities {
		urls = append(urls, sitemapURL{Loc: base + "/city/" + townSlug(c.Town), Priority: "0.7"})
	}

	// Organizations — every org has a public page at effectiveSlug(o),
	// whether or not it has an explicit ActorName.
	orgs, err := client.GetOrganizations(ctx)
	if err != nil {
		logging.Errorf("sitemap: could not load organizations: %v", err)
	}
	for _, o := range orgs {
		urls = append(urls, sitemapURL{Loc: base + "/org/" + effectiveSlug(o), Priority: "0.7"})
	}

	// Musicians (#1895): listed when they have events or a description/biography.
	musicians, err := client.GetMusicians(ctx)
	if err != nil {
		logging.Errorf("sitemap: could not load musicians: %v", err)
	}
	for _, m := range musicians {
		if !eventMusicians[m.ID] && m.Description == "" && m.Biography == "" {
			continue
		}
		urls = append(urls, sitemapURL{Loc: base + fmt.Sprintf("/musicians/%d", m.ID), Priority: "0.5"})
	}

	// Instructors (#1895): listed when they have events or a bio.
	instructors, err := client.GetInstructors(ctx)
	if err != nil {
		logging.Errorf("sitemap: could not load instructors: %v", err)
	}
	for _, ins := range instructors {
		if !eventInstructors[ins.ID] && ins.Bio == "" {
			continue
		}
		urls = append(urls, sitemapURL{Loc: base + fmt.Sprintf("/instructors/%d", ins.ID), Priority: "0.5"})
	}

	current := append(urls, currentEvents...)
	currentXML, err := marshalURLSet(current)
	if err != nil {
		return nil, err
	}
	archiveXML, err := marshalURLSet(archiveEvents)
	if err != nil {
		return nil, err
	}
	indexXML, err := xml.Marshal(sitemapIndexXML{
		NS: "http://www.sitemaps.org/schemas/sitemap/0.9",
		Sitemaps: []sitemapRef{
			{Loc: base + "/sitemap-current.xml", LastMod: today},
			{Loc: base + "/sitemap-archive.xml", LastMod: today},
		},
	})
	if err != nil {
		return nil, err
	}

	return map[string]sitemapPart{
		sitemapIndexName:   newSitemapPart(indexXML),
		sitemapCurrentName: newSitemapPart(currentXML),
		sitemapArchiveName: newSitemapPart(archiveXML),
	}, nil
}

// marshalURLSet renders a urlset body (without the XML header, which
// newSitemapPart adds). No indentation: sitemaps are machine-only, and gzip
// already removes most of the whitespace anyway.
func marshalURLSet(urls []sitemapURL) ([]byte, error) {
	return xml.Marshal(sitemapXML{
		NS:   "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs: urls,
	})
}

// newSitemapPart attaches the content-derived ETag to a generated document.
func newSitemapPart(body []byte) sitemapPart {
	doc := append([]byte(xml.Header), body...)
	sum := sha256.Sum256(doc)
	return sitemapPart{data: doc, etag: fmt.Sprintf(`"%x"`, sum[:])}
}

// eventLocationIDs returns the venue IDs an event takes place at.
func eventLocationIDs(e Event) []int {
	var ids []int
	if e.Location != nil && e.Location.ID > 0 {
		ids = append(ids, e.Location.ID)
	}
	for _, l := range e.Locations {
		if l.ID > 0 {
			ids = append(ids, l.ID)
		}
	}
	return ids
}

// parseUnixOrRFC3339 accepts a Unix timestamp string or an RFC3339 datetime.
func parseUnixOrRFC3339(s string) (time.Time, error) {
	if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(sec, 0), nil
	}
	return time.Parse(time.RFC3339, s)
}
