package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// #1661: legacy Gancio URLs stay 410 but browsers get matching events or
// venues derived from the old slug.

func TestLegacySlugWords(t *testing.T) {
	for slug, want := range map[string]string{
		"tanzfest-altmeteln":     "tanzfest altmeteln",
		"tanzlernabend-10":       "tanzlernabend",
		"2000-2200-tanzabend-15": "tanzabend",
		"herbstball-2024-1":      "herbstball",
		"duo-lotto2":             "duo lotto2",
		"123-456":                "",
		"a-b-c-d-e-f-g-h":        "a b c d e f",
		"jams-tanzhaus-in-der-christuskirche-firl": "jams tanzhaus in der christuskirche firl",
	} {
		if got := legacySlugWords(slug); got != want {
			t.Errorf("legacySlugWords(%q) = %q, want %q", slug, got, want)
		}
	}
}

func TestLongestWord(t *testing.T) {
	for in, want := range map[string]string{
		"tanzfest altmeteln": "altmeteln",
		"tanzlernabend":      "", // the phrase itself — no different fallback
		"duo lotto":          "lotto",
		"ab cd":              "",
	} {
		if got := longestWord(in); got != want {
			t.Errorf("longestWord(%q) = %q, want %q", in, got, want)
		}
	}
}

func legacyGoneServer(t *testing.T) (*DansalClient, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/events":
			title := r.URL.Query().Get("title")
			past := r.URL.Query().Get("order") == "desc"
			var out []map[string]any
			switch {
			case title == "tanzfest altmeteln" && past:
				out = append(out, map[string]any{"id": 15, "title": "Tanzfest Altmeteln", "start_time": "2026-08-14T18:00:00Z"})
			case title == "tanzlernabend" && !past:
				out = append(out, map[string]any{"id": 42, "title": "Tanzlernabend", "start_time": "2026-11-02T19:00:00Z"})
			}
			json.NewEncoder(w).Encode(out)
		case "/api/v1/locations":
			json.NewEncoder(w).Encode([]map[string]any{{"id": 7, "location": "Heilort", "town": "Witten"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &DansalClient{BaseURL: srv.URL, HTTP: srv.Client()}, &calls
}

func withErrorPageDeps(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT)`)
	oldSite := siteCfg
	siteCfg = newSiteSettingsCache(db)
	t.Cleanup(func() { siteCfg = oldSite })
	old := errorPageDeps
	errorPageDeps.cfg, errorPageDeps.tmpls, errorPageDeps.i18n = &Config{Domain: "example.test"}, loadTemplates(), loadI18n("")
	t.Cleanup(func() { errorPageDeps = old })
}

func TestLegacyGoneHandler(t *testing.T) {
	withErrorPageDeps(t)
	client, calls := legacyGoneServer(t)
	mux := http.NewServeMux()
	h := legacyGoneHandler(client)
	mux.HandleFunc("GET /event/{slug}", h)
	mux.HandleFunc("GET /place/{id}/{slug...}", h)
	mux.HandleFunc("GET /tag/{slug}", h)

	for _, tc := range []struct {
		path, accept string
		want         []string
		notWant      []string
	}{
		{"/event/tanzfest-altmeteln", "text/html", []string{`href="/events/15"`, "Tanzfest Altmeteln"}, nil},
		{"/event/tanzlernabend-10", "text/html", []string{`href="/events/42"`}, nil},
		{"/event/unknown-thing", "text/html", []string{`href="/cities"`}, []string{`class="error-page-suggest"`}},
		{"/place/36/Heilort%20Witten", "text/html", []string{`href="/location/7"`, "Witten"}, nil},
		{"/tag/scottish", "text/html", []string{`href="/cities"`}, []string{`class="error-page-suggest"`}},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Accept", tc.accept)
		mux.ServeHTTP(rec, req)
		body := rec.Body.String()
		if rec.Code != http.StatusGone {
			t.Errorf("%s: status %d, want 410", tc.path, rec.Code)
		}
		for _, s := range tc.want {
			if !strings.Contains(body, s) {
				t.Errorf("%s: body lacks %q", tc.path, s)
			}
		}
		for _, s := range tc.notWant {
			if strings.Contains(body, s) {
				t.Errorf("%s: body unexpectedly has %q", tc.path, s)
			}
		}
	}

	// Crawlers / API clients: plain 410 without any API lookup.
	before := calls.Load()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/event/tanzfest-altmeteln", nil)
	req.Header.Set("Accept", "*/*")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone || strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("non-HTML client: status %d body %q", rec.Code, rec.Body.String())
	}
	if calls.Load() != before {
		t.Errorf("non-HTML client triggered %d API calls", calls.Load()-before)
	}
}
