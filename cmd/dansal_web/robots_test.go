package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRobotsTxtDisallowsWastedCrawlerPaths(t *testing.T) {
	rec := httptest.NewRecorder()
	robotsTxtHandler(&Config{Domain: "example.org"})(rec, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"User-agent: *", "Disallow: /admin/", "Disallow: /api/",
		"Disallow: /events-more", "Disallow: /events-past", "Disallow: /search/",
		"Disallow: /tiles/", "Disallow: /login", "Disallow: /events/*/board",
		"Disallow: /events/*.ics$", "Sitemap: ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("robots.txt missing %q:\n%s", want, body)
		}
	}
	// Pages and feeds stay crawlable.
	for _, bad := range []string{"Disallow: /events/\n", "Disallow: /feed", "Disallow: /\n", "Disallow: /search\n"} {
		if strings.Contains(body, bad) {
			t.Errorf("robots.txt must not contain %q", bad)
		}
	}
}
