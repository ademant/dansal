package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// sitemapXML is the root element of a sitemap document.
type sitemapXML struct {
	XMLName xml.Name     `xml:"urlset"`
	NS      string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc        string `xml:"loc"`
	LastMod    string `xml:"lastmod,omitempty"`
	ChangeFreq string `xml:"changefreq,omitempty"`
	Priority   string `xml:"priority,omitempty"`
}

type sitemapCache struct {
	mu        sync.Mutex
	data      []byte
	fetchedAt time.Time
}

var smCache sitemapCache

const sitemapTTL = 30 * time.Minute

func sitemapHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		smCache.mu.Lock()
		if time.Since(smCache.fetchedAt) < sitemapTTL && len(smCache.data) > 0 {
			data := smCache.data
			smCache.mu.Unlock()
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=1800")
			w.Write(data)
			return
		}
		smCache.mu.Unlock()

		data, err := buildSitemap(r, cfg, client)
		if err != nil {
			http.Error(w, "could not generate sitemap", http.StatusInternalServerError)
			return
		}

		smCache.mu.Lock()
		smCache.data = data
		smCache.fetchedAt = time.Now()
		smCache.mu.Unlock()

		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=1800")
		w.Write(data)
	}
}

func buildSitemap(r *http.Request, cfg *Config, client *DansalClient) ([]byte, error) {
	base := cfg.publicBaseURL()
	today := time.Now().UTC().Format("2006-01-02")

	urls := []sitemapURL{
		{Loc: base + "/", LastMod: today, ChangeFreq: "daily", Priority: "1.0"},
		{Loc: base + "/musicians", ChangeFreq: "weekly", Priority: "0.6"},
		{Loc: base + "/organizations", ChangeFreq: "weekly", Priority: "0.6"},
	}

	// Static/legal pages
	for _, page := range []string{"/impressum", "/privacy", "/terms"} {
		urls = append(urls, sitemapURL{
			Loc:        base + page,
			ChangeFreq: "monthly",
			Priority:   "0.3",
		})
	}

	// Events — all published (past + future) so archived pages stay indexed.
	ctx := r.Context()
	events, _ := client.GetAllEvents(ctx)
	epoch := time.Unix(0, 0)
	for _, e := range events {
		lastMod := ""
		if e.ChangedAt != "" && e.ChangedAt != "0" {
			if t, err := parseUnixOrRFC3339(e.ChangedAt); err == nil && t.After(epoch) {
				lastMod = t.UTC().Format("2006-01-02")
			}
		}
		if lastMod == "" && e.CreatedAt != "" {
			if t, err := parseUnixOrRFC3339(e.CreatedAt); err == nil && t.After(epoch) {
				lastMod = t.UTC().Format("2006-01-02")
			}
		}
		priority := "0.8"
		if e.IsCancelled {
			priority = "0.3"
		}
		urls = append(urls, sitemapURL{
			Loc:        base + fmt.Sprintf("/events/%d", e.ID),
			LastMod:    lastMod,
			ChangeFreq: "weekly",
			Priority:   priority,
		})
	}

	// Locations
	locs, _ := client.GetLocations(ctx)
	for _, l := range locs {
		lastMod := ""
		if l.CreatedAt != "" {
			if t, err := parseUnixOrRFC3339(l.CreatedAt); err == nil {
				lastMod = t.UTC().Format("2006-01-02")
			}
		}
		urls = append(urls, sitemapURL{
			Loc:        base + fmt.Sprintf("/location/%d", l.ID),
			LastMod:    lastMod,
			ChangeFreq: "monthly",
			Priority:   "0.6",
		})
	}

	// Organizations — every org has a public page at effectiveSlug(o),
	// whether or not it has an explicit ActorName.
	orgs, _ := client.GetOrganizations(ctx)
	for _, o := range orgs {
		urls = append(urls, sitemapURL{
			Loc:        base + "/org/" + effectiveSlug(o),
			ChangeFreq: "weekly",
			Priority:   "0.7",
		})
	}

	// Musicians
	musicians, _ := client.GetMusicians(ctx)
	for _, m := range musicians {
		urls = append(urls, sitemapURL{
			Loc:        base + fmt.Sprintf("/musicians/%d", m.ID),
			ChangeFreq: "monthly",
			Priority:   "0.5",
		})
	}

	sm := sitemapXML{
		NS:   "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs: urls,
	}
	out, err := xml.MarshalIndent(sm, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}

// parseUnixOrRFC3339 accepts a Unix timestamp string or an RFC3339 datetime.
func parseUnixOrRFC3339(s string) (time.Time, error) {
	if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(sec, 0), nil
	}
	return time.Parse(time.RFC3339, s)
}
