package main

import (
	"net/http"
	"net/url"
	"strings"
)

// MediaLink is one external link (video, audio, image or other) on a
// musician, organization or location (#1360/#1361). It is only ever rendered
// as a plain link — never embedded or hotlinked.
type MediaLink struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Host is the link target's hostname without a leading "www.", shown next to
// the link so visitors can see where it leads before clicking.
func (l MediaLink) Host() string {
	u, err := url.Parse(l.URL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// DisplayTitle is the title, falling back to the host when none was given.
func (l MediaLink) DisplayTitle() string {
	if t := strings.TrimSpace(l.Title); t != "" {
		return t
	}
	return l.Host()
}

// mediaLinksFromForm reads the edit form's repeated media_kind/media_title/
// media_url rows (parallel arrays, in display order). Rows without a URL are
// skipped. The result is never nil, so a form with no rows still means "clear
// the list".
func mediaLinksFromForm(r *http.Request) []MediaLink {
	kinds := r.Form["media_kind"]
	titles := r.Form["media_title"]
	urls := r.Form["media_url"]
	links := []MediaLink{}
	for i, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		l := MediaLink{URL: u}
		if i < len(kinds) {
			l.Kind = strings.TrimSpace(kinds[i])
		}
		if i < len(titles) {
			l.Title = strings.TrimSpace(titles[i])
		}
		links = append(links, l)
	}
	return links
}

// maxMediaLinks mirrors the API's per-owner cap (maxMediaLinksPerOwner).
const maxMediaLinks = 20

// mergeMediaLists appends extra's links to base's, skipping URLs base already
// has and stopping at the per-owner cap. Used when locations are merged so the
// survivor keeps the links of the ones deleted. Returns nil when both are
// empty, so callers that then send it don't accidentally clear anything.
func mergeMediaLists(base, extra []MediaLink) []MediaLink {
	out := append([]MediaLink(nil), base...)
	seen := make(map[string]bool, len(out))
	for _, l := range out {
		seen[l.URL] = true
	}
	for _, l := range extra {
		if !seen[l.URL] && len(out) < maxMediaLinks {
			out = append(out, l)
			seen[l.URL] = true
		}
	}
	return out
}
