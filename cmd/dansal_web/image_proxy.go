package main

import "net/http"

// apiImagePrefixes lists the API's public image-serving endpoints, mirroring
// corpPublicImagePrefixes in cmd/dansal/main.go (#1148). The API returns these
// paths as root-relative URLs in its JSON (eventImageURL, orgImageURL, …) and
// the web templates render them verbatim into <img src>. Behind a reverse
// proxy that fronts both tiers they resolve against the API, but when the web
// tier is reached directly — local development, and the e2e suite's
// BASE_URL=http://localhost:8080 — nothing serves them, so every image 404s
// into a 0×0 broken <img> that renders as invisible (#1374).
//
// Each entry maps an API path prefix to the path-value name the API's own
// route uses, so the web route table stays a line-for-line mirror.
var apiImagePrefixes = []struct {
	apiPrefix string
	idParam   string
}{
	{"/api/v1/images/", "event_id"},
	{"/api/v1/event-banner/", "event_id"},
	{"/api/v1/org-images/", "id"},
	{"/api/v1/org-avatars/", "id"},
	{"/api/v1/musician-images/", "id"},
	{"/api/v1/musician-avatars/", "id"},
	{"/api/v1/instructor-avatars/", "id"},
	{"/api/v1/location-images/", "id"},
	{"/api/v1/series-images/", "id"},
	{"/api/v1/contact-post-images/", "img_id"},
}

// registerImageProxy mounts imageProxyHandler for every image route the API
// exposes, so the root-relative URLs the web templates render resolve against
// the web origin in every deployment (#1374).
func registerImageProxy(mux *http.ServeMux, client *DansalClient) {
	for _, p := range apiImagePrefixes {
		mux.Handle("GET "+p.apiPrefix+"{"+p.idParam+"}", imageProxyHandler(client, p.apiPrefix, p.idParam))
	}
}
