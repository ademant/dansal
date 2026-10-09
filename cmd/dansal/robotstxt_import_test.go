package main

// robots.txt + Crawl-delay for the recurring feed importer (#1484,
// compliance G9 phase-66). checkRobots is tested directly against a stub
// robots.txt server; importFromSource is tested end-to-end for the
// disallowed case to confirm the dispatch point actually wires it in,
// without needing a DB (a disallowed source returns before touching db at
// all).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func resetRobotsCacheForTest(t *testing.T) {
	t.Helper()
	old := robotsCache
	robotsCache = &robotsImportCache{
		byHost:        make(map[string]robotsEntry),
		hostLastFetch: make(map[string]time.Time),
	}
	t.Cleanup(func() { robotsCache = old })
}

func TestCheckRobotsDisallowed(t *testing.T) {
	resetRobotsCacheForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte("User-agent: *\nDisallow: /\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	allowed, reason := checkRobots(context.Background(), srv.URL+"/feed.ics")
	if allowed {
		t.Fatal("expected disallowed, got allowed")
	}
	if reason == "" {
		t.Error("expected a non-empty reason")
	}
}

func TestCheckRobotsAllowedSpecificPath(t *testing.T) {
	resetRobotsCacheForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte("User-agent: *\nDisallow: /private\nAllow: /feed.ics\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if allowed, reason := checkRobots(context.Background(), srv.URL+"/feed.ics"); !allowed {
		t.Fatalf("expected allowed, got disallowed: %s", reason)
	}
	if allowed, _ := checkRobots(context.Background(), srv.URL+"/private/x"); allowed {
		t.Fatal("expected /private to be disallowed")
	}
}

func TestCheckRobotsNoRobotsTxtAllowsEverything(t *testing.T) {
	resetRobotsCacheForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // no robots.txt at all
	}))
	defer srv.Close()

	if allowed, reason := checkRobots(context.Background(), srv.URL+"/feed.ics"); !allowed {
		t.Fatalf("expected allowed (no robots.txt), got disallowed: %s", reason)
	}
}

func TestCheckRobotsUnreachableFailsOpen(t *testing.T) {
	resetRobotsCacheForTest(t)
	// Port 0 connects to nothing; the robots.txt fetch itself fails, which
	// must fail open rather than block the real fetch that follows.
	if allowed, reason := checkRobots(context.Background(), "http://127.0.0.1:1/feed.ics"); !allowed {
		t.Fatalf("expected fail-open on unreachable robots.txt, got disallowed: %s", reason)
	}
}

func TestCheckRobotsCrawlDelay(t *testing.T) {
	resetRobotsCacheForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte("User-agent: *\nCrawl-delay: 3600\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	feedURL := srv.URL + "/feed.ics"
	if allowed, reason := checkRobots(context.Background(), feedURL); !allowed {
		t.Fatalf("first fetch should be allowed, got disallowed: %s", reason)
	}
	noteRobotsFetchAttempt(feedURL)

	if allowed, reason := checkRobots(context.Background(), feedURL); allowed {
		t.Fatal("second fetch within the Crawl-delay window should be disallowed")
	} else if reason == "" {
		t.Error("expected a non-empty reason")
	}
}

// TestImportFromSourceRespectsRobotsDisallow confirms the recurring importer's
// single dispatch point (importFromSource) actually calls checkRobots —
// not just the unit above. A disallowed source returns before touching the
// DB at all, so this needs no DB setup.
func TestImportFromSourceRespectsRobotsDisallow(t *testing.T) {
	resetRobotsCacheForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte("User-agent: *\nDisallow: /\n"))
			return
		}
		t.Error("fetchFeedBody must not be reached for a disallowed source")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, _, err := importFromSource(context.Background(), FetchSource{Type: "ical", URL: srv.URL + "/feed.ics"})
	if err == nil {
		t.Fatal("expected an error for a robots.txt-disallowed source")
	}
}
