package main

import "testing"

// TestCorpForAPIPath locks in the route-aware Cross-Origin-Resource-Policy
// values from #1148: image-serving GET endpoints get 'cross-origin' since
// they're expected to be hotlinked via <img> tags cross-origin; the rest of
// the JSON API is left unset so its intentionally-open CORS consumption
// (see corsOrigin) isn't broken by CORP.
func TestCorpForAPIPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/api/v1/images/42", "cross-origin"},
		{"/api/v1/event-banner/42", "cross-origin"},
		{"/api/v1/org-images/1", "cross-origin"},
		{"/api/v1/org-avatars/1", "cross-origin"},
		{"/api/v1/musician-images/1", "cross-origin"},
		{"/api/v1/musician-avatars/1", "cross-origin"},
		{"/api/v1/instructor-avatars/1", "cross-origin"},
		{"/api/v1/location-images/1", "cross-origin"},
		{"/api/v1/series-images/1", "cross-origin"},
		{"/api/v1/contact-post-images/1", "cross-origin"},
		{"/api/v1/events", ""},
		{"/api/v1/events/42", ""},
		{"/api/v1/login", ""},
	}
	for _, c := range cases {
		if got := corpForAPIPath(c.path); got != c.want {
			t.Errorf("corpForAPIPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
