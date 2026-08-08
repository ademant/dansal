package main

import (
	"html/template"
	"strings"
)

// Chat/contact template functions — one slice of the merged tmplFuncMap,
// split out of frontend.go (#1031).

func validMatrixID(s string) bool {
	if !strings.HasPrefix(s, "@") {
		return false
	}
	colon := strings.IndexByte(s, ':')
	return colon > 1 && colon < len(s)-1
}

var tmplFuncsChat = template.FuncMap{
	// chatLinkPlatforms returns the known chat_links platforms in canonical
	// display order, for the admin org edit form to render one input per
	// platform. Adding a platform is one entry here, no DB migration (#925).
	"chatLinkPlatforms": func() []ChatPlatformInfo {
		return chatLinkPlatformOrder
	},
	// chatLinkURL looks up the URL for a platform within an org's ChatLinks,
	// for prefilling the admin edit form. Returns "" when absent.
	"chatLinkURL": func(links []ChatLink, platform string) string {
		for _, l := range links {
			if l.Platform == platform {
				return l.URL
			}
		}
		return ""
	},
	// chatPlatformLabel returns the display label for a chat_links platform
	// slug (e.g. "mailing_list" -> "Mailing list"), for the public org page.
	"chatPlatformLabel": func(platform string) string {
		for _, p := range chatLinkPlatformOrder {
			if p.Slug == platform {
				return p.Label
			}
		}
		return platform
	},
	// mastodonURL converts "@user@instance.tld" → "https://instance.tld/@user".
	// If the value already starts with "http", it is returned unchanged.
	"mastodonURL": func(handle string) string {
		if strings.HasPrefix(handle, "http") {
			return handle
		}
		// strip leading @
		h := strings.TrimPrefix(handle, "@")
		parts := strings.SplitN(h, "@", 2)
		if len(parts) == 2 {
			return "https://" + parts[1] + "/@" + parts[0]
		}
		return handle
	},
	"validMatrixID": validMatrixID,
}
