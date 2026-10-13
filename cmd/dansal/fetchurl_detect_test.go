package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDetectFetchType(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"rss feed with rss in path", "http://example.org/rss.xml", "rss"},
		{"atom feed", "http://example.org/feed/atom", "rss"},
		{"rss under feed dir", "http://example.org/feed/feed.xml", "rss"},
		{"ical with ics suffix under feed dir", "http://example.org/feed/events.ics", "ical"},
		{"ical with ics suffix and query string", "http://example.org/feed/events.ics?e2e=abc", "ical"},
		{"ical with ics suffix without feed dir", "http://example.org/cal.ics", "ical"},
		{"json suffix", "http://example.org/api/events.json", "json"},
		{"json suffix with query string", "http://example.org/api/events.json?e2e=abc", "json"},
		{"xml suffix", "http://example.org/feed/events.xml", "rss"},
		{"extensionless under feed dir", "http://example.org/feed/", "rss"},
		{"extensionless without feed dir", "http://example.org/calendar", "ical"},
		{"gancio path probe wins", "http://example.org/foo/api/events", "json"},
		{"folkdance probe", "http://folkdance.page/api/x.json", "json"},
		{"tec probe", "http://example.org/wp-json/tribe/events/v1", "json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectFetchType(tc.url); got != tc.want {
				t.Fatalf("detectFetchType(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestDetectFetchTypeContentTypePrecedence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/calendar":
			w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		case "/feed/feed":
			w.Header().Set("Content-Type", "application/rss+xml")
		case "/api/list":
			w.Header().Set("Content-Type", "application/json")
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	old := safeClient
	safeClient = &http.Client{Timeout: 5e9}
	t.Cleanup(func() { safeClient = old })

	cases := []struct {
		name string
		url  string
		want string
	}{
		{"extensionless iCal served as text/calendar", srv.URL + "/calendar", "ical"},
		{"extensionless feed served as rss+xml", srv.URL + "/feed/feed", "rss"},
		{"extensionless served as json", srv.URL + "/api/list", "json"},
		{"404 falls back to path heuristics", srv.URL + "/feed/missing", "rss"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectFetchType(tc.url); got != tc.want {
				t.Fatalf("detectFetchType(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}
