package main

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sitemapFakeAPI serves the list endpoints buildSitemaps reads. Each endpoint
// returns a JSON array with X-Total-Count so the paged client stops after one page.
func sitemapFakeAPI(t *testing.T, data map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := data[r.URL.Path]
		if !ok {
			body = []any{}
		}
		b, _ := json.Marshal(body)
		n := len(body.([]any))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Total-Count", strconv.Itoa(n))
		w.Write(b)
	}))
}

type urlsetDoc struct {
	URLs []struct {
		Loc      string `xml:"loc"`
		LastMod  string `xml:"lastmod"`
		Priority string `xml:"priority"`
	} `xml:"url"`
}

type indexDoc struct {
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

func parseURLs(t *testing.T, data []byte) []string {
	t.Helper()
	var doc urlsetDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse urlset: %v\n%s", err, data)
	}
	var out []string
	for _, u := range doc.URLs {
		out = append(out, u.Loc)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.HasSuffix(x, s) {
			return true
		}
	}
	return false
}

// TestSitemapSplitsEventsAndFiltersEntities covers #1900 (index with current
// and archive children, no changefreq) and #1895 (entities with no events and
// no own text left out).
func TestSitemapSplitsEventsAndFiltersEntities(t *testing.T) {
	now := time.Now()
	upcoming := now.Add(48 * time.Hour).UTC().Format(time.RFC3339)
	old := now.AddDate(-2, 0, 0).UTC().Format(time.RFC3339)
	recent := now.AddDate(0, -3, 0).UTC().Format(time.RFC3339)

	events := []any{
		map[string]any{"id": 1, "title": "Upcoming", "start_time": upcoming, "is_published": true,
			"location":  map[string]any{"id": 1, "location": "Hall", "town": "Marburg"},
			"musicians": []any{map[string]any{"id": 10, "bandname": "Band"}}},
		map[string]any{"id": 2, "title": "Recent", "start_time": recent, "is_published": true,
			"location": map[string]any{"id": 5, "location": "Room", "town": "Marburg"}},
		map[string]any{"id": 3, "title": "Old", "start_time": old, "is_published": true},
		map[string]any{"id": 4, "title": "Cancelled", "start_time": upcoming, "is_published": true, "is_cancelled": true},
	}
	locations := []any{
		map[string]any{"id": 1, "location": "Hall", "town": "Marburg", "created_at": "2020-01-01T00:00:00Z"},
		map[string]any{"id": 2, "location": "Empty", "town": "Marburg"},
		map[string]any{"id": 3, "location": "Noted", "town": "Marburg", "notes_md": "Text"},
		map[string]any{"id": 4, "location": "Hall room", "town": "Marburg", "parent_id": 1},
		map[string]any{"id": 5, "location": "Room with events", "town": "Marburg", "parent_id": 2},
		map[string]any{"id": 6, "location": "Plain empty", "town": "Marburg"},
	}
	musicians := []any{
		map[string]any{"id": 10, "bandname": "Plays"},
		map[string]any{"id": 11, "bandname": "Silent"},
		map[string]any{"id": 12, "bandname": "Bio only", "biography": "Text"},
	}
	instructors := []any{
		map[string]any{"id": 20, "name": "Nobody"},
		map[string]any{"id": 21, "name": "Bio", "bio": "Text"},
	}
	api := sitemapFakeAPI(t, map[string]any{
		"/api/v1/events":      events,
		"/api/v1/locations":   locations,
		"/api/v1/musicians":   musicians,
		"/api/v1/instructors": instructors,
	})
	defer api.Close()
	client := &DansalClient{BaseURL: api.URL, HTTP: api.Client()}
	cfg := &Config{BaseURL: "https://example.test"}
	req := httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil)

	parts, err := buildSitemaps(req, cfg, client)
	if err != nil {
		t.Fatalf("buildSitemaps: %v", err)
	}

	// #1900: index names both children, and no part carries changefreq.
	var idx indexDoc
	if err := xml.Unmarshal(parts[sitemapIndexName].data, &idx); err != nil {
		t.Fatalf("parse index: %v", err)
	}
	if len(idx.Sitemaps) != 2 || !strings.HasSuffix(idx.Sitemaps[0].Loc, "/sitemap-current.xml") || !strings.HasSuffix(idx.Sitemaps[1].Loc, "/sitemap-archive.xml") {
		t.Errorf("index children = %+v, want current and archive", idx.Sitemaps)
	}
	for name, p := range parts {
		if strings.Contains(string(p.data), "changefreq") {
			t.Errorf("%s sitemap still contains changefreq", name)
		}
	}

	current := parseURLs(t, parts[sitemapCurrentName].data)
	archive := parseURLs(t, parts[sitemapArchiveName].data)

	// Events: upcoming and recent in current; old and cancelled in archive.
	if !contains(current, "/events/1") || !contains(current, "/events/2") {
		t.Errorf("current sitemap misses upcoming or recent event: %v", current)
	}
	if contains(current, "/events/3") || contains(current, "/events/4") {
		t.Errorf("current sitemap lists old or cancelled event: %v", current)
	}
	if !contains(archive, "/events/3") || !contains(archive, "/events/4") {
		t.Errorf("archive sitemap misses old or cancelled event: %v", archive)
	}

	// #1895: venues with events or notes, rooms only with their own events, and
	// the building of a room with events.
	// Location 2 is a building whose room 5 has events, so it is listed too.
	for _, want := range []string{"/location/1", "/location/2", "/location/3", "/location/5"} {
		if !contains(current, want) {
			t.Errorf("venue %s missing from current sitemap: %v", want, current)
		}
	}
	if contains(current, "/location/6") {
		t.Errorf("venue without events or notes listed: %v", current)
	}
	if contains(current, "/location/4") {
		t.Errorf("room without events listed: %v", current)
	}
	if !contains(current, "/musicians/10") || !contains(current, "/musicians/12") || contains(current, "/musicians/11") {
		t.Errorf("musician filter wrong: %v", current)
	}
	if !contains(current, "/instructors/21") || contains(current, "/instructors/20") {
		t.Errorf("instructor filter wrong: %v", current)
	}
}

// TestSitemapHandlerServesIndex checks the index response and its ETag.
func TestSitemapHandlerServesIndex(t *testing.T) {
	api := sitemapFakeAPI(t, map[string]any{})
	defer api.Close()
	client := &DansalClient{BaseURL: api.URL, HTTP: api.Client()}
	cfg := &Config{BaseURL: "https://example.test"}
	h := sitemapHandler(cfg, client, sitemapIndexName)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "sitemap-current.xml") {
		t.Errorf("index does not reference the current sitemap: %s", rec.Body.String())
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("index has no ETag")
	}
}
