package main

import "strings"

// ChatLink is one entry in an organization's chat_links JSON column: a
// community chat/list invite (Telegram, Signal, WhatsApp, Threema, Matrix
// room, or Mailman/Postorius mailing list). Distinct from the
// mastodon/instagram/facebook identity fields — those represent "this is
// the same entity elsewhere" and are surfaced in JSON-LD sameAs; chat_links
// represent "join our community" and are never added to sameAs (see #925).
type ChatLink struct {
	Platform string `json:"platform"`
	URL      string `json:"url"`
}

// validChatLinkPlatforms is the known registry of chat_links platforms.
// Adding a new platform is one entry here plus one entry in the web-side
// display registry (chatLinkPlatformOrder in cmd/dansal_web/frontend.go) —
// no schema migration needed, unlike a dedicated column per platform.
var validChatLinkPlatforms = map[string]bool{
	"telegram":     true,
	"signal":       true,
	"whatsapp":     true,
	"threema":      true,
	"matrix":       true,
	"mailing_list": true,
}

// filterChatLinks drops entries with an unknown platform or blank URL,
// mirroring how fetch-import tags are filtered against a known vocabulary
// (#923) instead of rejecting the whole request over one bad entry.
func filterChatLinks(links []ChatLink) []ChatLink {
	if len(links) == 0 {
		return nil
	}
	out := make([]ChatLink, 0, len(links))
	for _, l := range links {
		url := strings.TrimSpace(l.URL)
		if validChatLinkPlatforms[l.Platform] && url != "" {
			out = append(out, ChatLink{Platform: l.Platform, URL: url})
		}
	}
	return out
}
