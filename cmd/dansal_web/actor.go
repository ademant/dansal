package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
)

// safeFedDial resolves the target host and rejects any address that resolves
// to a loopback, private, link-local, or otherwise non-routable IP to prevent SSRF.
func safeFedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("safeFedDial: bad addr %q: %w", addr, err)
	}
	resolved, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range resolved {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			return nil, fmt.Errorf("safeFedDial: parse IP %q: %w", a, err)
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
			cgnat6598.Contains(ip) {
			return nil, fmt.Errorf("safeFedDial: %q resolves to non-routable IP %s", host, a)
		}
	}
	var d net.Dialer
	return d.DialContext(ctx, network, net.JoinHostPort(resolved[0], port))
}

// fedHTTPClient is used for all outbound ActivityPub requests.
// It enforces per-request timeouts and blocks private/loopback destinations.
var fedHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext:           safeFedDial,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          50,
	},
}

var cgnat6598 = netip.MustParsePrefix("100.64.0.0/10")

var (
	slugTranslit = strings.NewReplacer(
		"Ä", "a", "ä", "a",
		"Ö", "o", "ö", "o",
		"Ü", "u", "ü", "u",
		"ß", "ss",
		"À", "a", "à", "a", "Â", "a", "â", "a",
		"Á", "a", "á", "a", "Ã", "a", "ã", "a",
		"Å", "a", "å", "a",
		"Æ", "ae", "æ", "ae",
		"Ç", "c", "ç", "c",
		"È", "e", "è", "e", "É", "e", "é", "e",
		"Ê", "e", "ê", "e", "Ë", "e", "ë", "e",
		"Î", "i", "î", "i", "Ï", "i", "ï", "i",
		"Í", "i", "í", "i", "Ì", "i", "ì", "i",
		"Ñ", "n", "ñ", "n",
		"Ô", "o", "ô", "o", "Ó", "o", "ó", "o",
		"Ò", "o", "ò", "o", "Õ", "o", "õ", "o",
		"Ø", "o", "ø", "o",
		"Œ", "oe", "œ", "oe",
		"Ù", "u", "ù", "u", "Û", "u", "û", "u",
		"Ú", "u", "ú", "u",
		"Ý", "y", "ý", "y", "ÿ", "y",
		"'", "", "’", "", // apostrophes (e.g. Breton c'h)
	)
	slugRe     = regexp.MustCompile(`[^a-z0-9\-]`)
	slugDashRe = regexp.MustCompile(`-{2,}`)
)

// effectiveSlug returns the AP actor slug for an org: actor_name if set, else name-derived.
func effectiveSlug(org Organization) string {
	if org.ActorName != "" {
		return org.ActorName
	}
	return orgSlug(org.Name)
}

func orgSlug(name string) string {
	s := slugTranslit.Replace(name)
	s = strings.ToLower(s)
	s = slugRe.ReplaceAllString(s, "-")
	s = slugDashRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

func actorURL(cfg *Config, slug string) string {
	return "https://" + cfg.Domain + "/org/" + slug
}

// actorKeyID returns the key id used to sign requests for an actor — the
// actor's canonical URL plus the "#main-key" fragment referenced by its
// publicKey document.
func actorKeyID(cfg *Config, slug string) string {
	return actorURL(cfg, slug) + "#main-key"
}

func actorFromOrg(cfg *Config, org Organization, actor *ActorRecord) Actor {
	base := actorURL(cfg, actor.OrgSlug)
	a := Actor{
		Context:                   APContext,
		Type:                      "Service",
		ID:                        base,
		Name:                      org.Name,
		Summary:                   org.Description,
		URL:                       base,
		PreferredUsername:         actor.OrgSlug,
		Inbox:                     base + "/inbox",
		Outbox:                    base + "/outbox",
		Followers:                 base + "/followers",
		ManuallyApprovesFollowers: false,
		Discoverable:              true,
		Indexable:                 true,
		Endpoints: &APEndpoints{
			SharedInbox: "https://" + cfg.Domain + "/inbox",
			Tags:        "https://" + cfg.Domain + "/tags",
		},
		PublicKey: PublicKey{
			ID:           actorKeyID(cfg, actor.OrgSlug),
			Owner:        base,
			PublicKeyPem: actor.PublicKeyPEM,
		},
	}
	iconURL := org.AvatarURL
	if iconURL == "" {
		iconURL = org.ImageURL
	}
	if iconURL != "" {
		if iconURL[0] == '/' {
			iconURL = "https://" + cfg.Domain + iconURL
		}
		a.Icon = &APDocument{Type: "Image", MediaType: "image/jpeg", URL: iconURL}
	}
	if org.ImageURL != "" {
		bannerURL := org.ImageURL
		if bannerURL[0] == '/' {
			bannerURL = "https://" + cfg.Domain + bannerURL
		}
		bannerMime := org.ImageMediaType
		if bannerMime == "" {
			bannerMime = "image/jpeg"
		}
		a.Image = &APDocument{Type: "Image", MediaType: bannerMime, URL: bannerURL}
	}
	return a
}

// relayActorFromRecord returns the synthetic, instance-wide ActivityPub actor.
// Keeping this separate from the HTTP handler also lets profile updates deliver
// exactly the same representation that a remote server fetches from /org/relay.
func relayActorFromRecord(cfg *Config, actor *ActorRecord) Actor {
	base := actorURL(cfg, cfg.RelayActorName)
	displayName := cfg.RelayDisplayName
	if displayName == "" {
		displayName = cfg.RelayActorName + "@" + cfg.Domain
	}
	a := Actor{
		Context:                   APContext,
		Type:                      "Application",
		ID:                        base,
		Name:                      displayName,
		Summary:                   cfg.RelaySummary,
		URL:                       "https://" + cfg.Domain,
		PreferredUsername:         cfg.RelayActorName,
		Inbox:                     base + "/inbox",
		Outbox:                    base + "/outbox",
		Followers:                 base + "/followers",
		ManuallyApprovesFollowers: false,
		Discoverable:              true,
		Indexable:                 true,
		Endpoints:                 &APEndpoints{SharedInbox: "https://" + cfg.Domain + "/inbox"},
		AlsoKnownAs:               cfg.RelayAlsoKnownAs,
		PublicKey: PublicKey{
			ID:           actorKeyID(cfg, cfg.RelayActorName),
			Owner:        base,
			PublicKeyPem: actor.PublicKeyPEM,
		},
	}
	if u, m := relayAssetURL(cfg, "relay-avatar", "/relay-icon", cfg.RelayIconURL); u != "" {
		a.Icon = &APDocument{Type: "Image", MediaType: m, URL: u}
	}
	if u, m := relayAssetURL(cfg, "relay-banner", "/relay-banner", cfg.RelayImageURL); u != "" {
		a.Image = &APDocument{Type: "Image", MediaType: m, URL: u}
	}
	return a
}

// isAPRequest reports whether the request negotiates an ActivityPub JSON
// representation. application/activity+json is the canonical AP type;
// application/ld+json is also accepted because some fediverse clients send it
// when following AP actors. Note the dual use of this media type: the same
// string labels the SEO JSON-LD blocks embedded in HTML pages, which are only
// ever served inside text/html and never standalone. Keep this function
// AP-only — if standalone machine-readable JSON-LD endpoints are ever added,
// give them their own path/media-type handling rather than widening this
// branch (issue #1062).
func isAPRequest(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/activity+json") ||
		strings.Contains(accept, "application/ld+json")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/activity+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func actorsListHandler(cfg *Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slugs, err := listOrgActorSlugs(db)
		if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, "database error")
			return
		}
		type entry struct {
			Handle string `json:"handle"`
			URL    string `json:"url"`
		}
		result := make([]entry, 0, len(slugs))
		for _, slug := range slugs {
			result = append(result, entry{
				Handle: "@" + slug + "@" + cfg.Domain,
				URL:    actorURL(cfg, slug),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

// webfingerSlug extracts the actor slug from a WebFinger resource query,
// accepting both "acct:user@domain" and the actor's canonical "https://"
// URL (as returned by actorURL). RFC 7033 permits either form, and real
// Fediverse servers commonly look up actors they already know by URL, not
// just by acct: handle (issue #830). notFound distinguishes a
// well-formed-but-unresolvable resource (404) from an unsupported format
// (400).
func webfingerSlug(cfg *Config, resource string) (slug string, notFound, ok bool) {
	if strings.HasPrefix(resource, "acct:") {
		account := strings.TrimPrefix(resource, "acct:")
		parts := strings.SplitN(account, "@", 2)
		if len(parts) != 2 || parts[1] != cfg.Domain || parts[0] == "" {
			return "", true, false
		}
		return parts[0], false, true
	}
	orgPrefix := actorURL(cfg, "")
	if strings.HasPrefix(resource, orgPrefix) {
		slug := strings.TrimPrefix(resource, orgPrefix)
		if slug == "" {
			return "", true, false
		}
		return slug, false, true
	}
	return "", false, false
}

func webfingerHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resource := r.URL.Query().Get("resource")
		if resource == "" {
			writeJSONError(w, r, http.StatusBadRequest, "resource parameter required")
			return
		}
		slug, notFound, ok := webfingerSlug(cfg, resource)
		if notFound {
			writeJSONError(w, r, http.StatusNotFound, "user not found")
			return
		}
		if !ok {
			writeJSONError(w, r, http.StatusBadRequest, "unsupported resource format")
			return
		}
		// Some Fediverse clients probe common local-part conventions (e.g.
		// admin@domain, info@domain) even without being told they exist.
		// WebfingerAliases lets an admin route those to a real actor slug
		// instead of a 404 (issue #947).
		if target, aliased := cfg.WebfingerAliases[slug]; aliased && target != "" {
			slug = target
		}
		actor, err := getActorBySlug(db, slug)
		if err == sql.ErrNoRows {
			if slug == "relay" {
				writeJSONError(w, r, http.StatusNotFound, "user not found")
				return
			}
			orgs, err := client.GetOrganizations(r.Context())
			if err != nil {
				writeJSONError(w, r, http.StatusInternalServerError, "upstream error")
				return
			}
			for _, org := range orgs {
				if effectiveSlug(org) == slug {
					actor, err = ensureActor(db, org.ID, slug)
					if err != nil {
						writeJSONError(w, r, http.StatusInternalServerError, "actor init error")
						return
					}
					break
				}
			}
			if actor == nil {
				// Not an org actor — check if it's a tag (#959): acct:bal-folk@domain.
				tagMap, tagErr := client.GetTagMap(r.Context())
				if tagErr == nil {
					if tag, ok := tagMap[slug]; ok {
						tagURL := "https://" + cfg.Domain + "/tags/" + tag.Slug
						wf := WebFinger{
							Subject: "acct:" + tag.Slug + "@" + cfg.Domain,
							Aliases: []string{tagURL},
							Links: []WebFingerLink{
								{Rel: "self", Type: "application/activity+json", Href: tagURL},
								{Rel: "http://webfinger.net/rel/profile-page", Type: "text/html", Href: tagURL},
							},
						}
						w.Header().Set("Content-Type", "application/jrd+json")
						json.NewEncoder(w).Encode(wf)
						return
					}
				}
				writeJSONError(w, r, http.StatusNotFound, "user not found")
				return
			}
		} else if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		base := actorURL(cfg, actor.OrgSlug)
		var aliases []string
		var links []WebFingerLink
		// During relay migration: the primary self link must be the old actor URL
		// so Mastodon's WebFinger loopback check passes when verifying the Move.
		// After migration, remove relay_also_known_as from config to restore normal order.
		if actor.OrgID == 0 && len(cfg.RelayAlsoKnownAs) > 0 {
			for _, oldURL := range cfg.RelayAlsoKnownAs {
				links = append(links, WebFingerLink{Rel: "self", Type: "application/activity+json", Href: oldURL})
				aliases = append(aliases, oldURL)
			}
			links = append(links, WebFingerLink{Rel: "self", Type: "application/activity+json", Href: base})
			aliases = append(aliases, base)
		} else {
			aliases = []string{base}
			links = []WebFingerLink{
				{Rel: "self", Type: "application/activity+json", Href: base},
				// Advertise the HTML org page (same URL) so clients can show an
				// "Open original" button on org actor profiles (issue #1056).
				{Rel: "http://webfinger.net/rel/profile-page", Type: "text/html", Href: base},
			}
		}
		wf := WebFinger{
			Subject: "acct:" + actor.OrgSlug + "@" + cfg.Domain,
			Aliases: aliases,
			Links:   links,
		}
		w.Header().Set("Content-Type", "application/jrd+json")
		json.NewEncoder(w).Encode(wf)
	}
}

func nodeinfoIndexHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := "https://" + cfg.Domain
		resp := map[string]any{
			"links": []map[string]string{
				{
					"rel":  "http://nodeinfo.diaspora.software/ns/schema/2.0",
					"href": base + "/nodeinfo/2.0",
				},
				{
					"rel":  "http://nodeinfo.diaspora.software/ns/schema/2.1",
					"href": base + "/nodeinfo/2.1",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func buildNodeInfo(cfg *Config, schemaVersion string, info DansalInfo) NodeInfo {
	repo := cfg.NodeInfoRepository
	if repo == "" {
		repo = "https://github.com/ademant/dansal"
	}
	homepage := cfg.NodeInfoHomepage
	if homepage == "" {
		homepage = cfg.publicBaseURL()
	}
	ni := NodeInfo{
		Version: schemaVersion,
		Software: NodeInfoSoftware{
			Name:       "dansal",
			Version:    Version,
			Repository: repo,
			Homepage:   homepage,
		},
		Protocols:         []string{"activitypub"},
		OpenRegistrations: false,
		Usage: NodeInfoUsage{
			LocalPosts:    info.PublishedEvents,
			LocalComments: info.BoardEntries,
		},
	}
	ni.Usage.Users.Total = info.TotalUsers
	if schemaVersion == "2.1" {
		var meta *NodeInfoMetadata
		if cfg.NodeInfoDescription != "" || cfg.NodeInfoMaintainerName != "" || cfg.NodeInfoMaintainerEmail != "" {
			meta = &NodeInfoMetadata{NodeDescription: cfg.NodeInfoDescription}
			if cfg.NodeInfoMaintainerName != "" || cfg.NodeInfoMaintainerEmail != "" {
				meta.Maintainer = &NodeInfoMaintainer{
					Name:  cfg.NodeInfoMaintainerName,
					Email: cfg.NodeInfoMaintainerEmail,
				}
			}
		}
		ni.Metadata = meta
	}
	return ni
}

func nodeinfoHandler(cfg *Config, client *DansalClient, schemaVersion string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info, err := client.GetServiceInfo(r.Context())
		if err != nil {
			log.Printf("nodeinfo: could not load service info: %v", err)
		}
		ni := buildNodeInfo(cfg, schemaVersion, info)
		w.Header().Set("Content-Type", `application/json; profile="http://nodeinfo.diaspora.software/ns/schema/`+schemaVersion+`#"`)
		json.NewEncoder(w).Encode(ni)
	}
}

// outboxPageSize is the ActivityPub outbox page size. The underlying API is
// asked for exactly one page at a time (via offset), so the outbox never has
// to hold the full event history in memory (#1055).
const outboxPageSize = 100

func outboxHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("name")
		actor, err := getActorBySlug(db, slug)
		if err == sql.ErrNoRows {
			writeJSONError(w, r, http.StatusNotFound, "actor not found")
			return
		} else if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		base := actorURL(cfg, slug)
		outboxURL := base + "/outbox"

		// The outbox is the actor's full post history (all published events,
		// past included) — new followers read it to back-fill. Without an
		// explicit limit the API caps at 100, so without include_past=true it
		// only held upcoming events: both truncated the history (#1055).
		params := url.Values{}
		params.Set("is_published", "true")
		params.Set("include_past", "true")
		if actor.OrgID != 0 {
			params.Set("organization_id", strconv.Itoa(actor.OrgID))
		}

		if r.URL.Query().Get("page") != "true" {
			// limit=1 is enough: X-Total-Count reflects the full count even
			// when the page is truncated, so the collection root reports the
			// real totalItems without downloading the history.
			params.Set("limit", "1")
			_, total, err := client.GetEventsFilteredWithTotal(r.Context(), params)
			if err != nil {
				logHTTPError(w, r, "could not load outbox events", http.StatusBadGateway)
				return
			}
			col := OrderedCollection{
				Context:    APContext,
				Type:       "OrderedCollection",
				ID:         outboxURL,
				TotalItems: total,
				First:      outboxURL + "?page=true",
			}
			writeJSON(w, http.StatusOK, col)
			return
		}

		offset := 0
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		params.Set("limit", strconv.Itoa(outboxPageSize))
		params.Set("offset", strconv.Itoa(offset))
		events, total, err := client.GetEventsFilteredWithTotal(r.Context(), params)
		if err != nil {
			logHTTPError(w, r, "could not load outbox events", http.StatusBadGateway)
			return
		}

		items := make([]any, 0, len(events))
		for _, e := range events {
			items = append(items, buildCreateActivity(cfg, actor.OrgSlug, e))
		}

		pageURL := outboxURL + "?page=true"
		if offset > 0 {
			pageURL += "&offset=" + strconv.Itoa(offset)
		}
		page := OrderedCollectionPage{
			Context:      APContext,
			Type:         "OrderedCollectionPage",
			ID:           pageURL,
			PartOf:       outboxURL,
			TotalItems:   total,
			OrderedItems: items,
		}
		if offset+len(items) < total {
			page.Next = outboxURL + "?page=true&offset=" + strconv.Itoa(offset+len(items))
		}
		if offset > 0 {
			page.Prev = outboxURL + "?page=true&offset=" + strconv.Itoa(max(0, offset-outboxPageSize))
		}
		writeJSON(w, http.StatusOK, page)
	}
}

func followersHandler(cfg *Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("name")
		actor, err := getActorBySlug(db, slug)
		if err == sql.ErrNoRows {
			writeJSONError(w, r, http.StatusNotFound, "actor not found")
			return
		} else if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		fs, err := listFollowers(db, actor.OrgID)
		if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		uris := make([]string, len(fs))
		for i, f := range fs {
			uris[i] = f.ActorURI
		}

		base := actorURL(cfg, slug)
		col := OrderedCollection{
			Context:    APContext,
			Type:       "OrderedCollection",
			ID:         base + "/followers",
			TotalItems: len(uris),
			Items:      uris,
		}
		writeJSON(w, http.StatusOK, col)
	}
}

func inboxHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("name")
		actor, err := getActorBySlug(db, slug)
		if err == sql.ErrNoRows {
			writeJSONError(w, r, http.StatusNotFound, "actor not found")
			return
		} else if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		raw, ok := readInboxActivity(w, r)
		if !ok {
			return
		}
		processInboxActivity(w, r, cfg, db, client, actor, raw)
	}
}

func sharedInboxHandler(cfg *Config, db *sql.DB, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readInboxActivity(w, r)
		if !ok {
			return
		}
		actor := resolveSharedInboxActor(cfg, db, raw)
		if actor == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		processInboxActivity(w, r, cfg, db, client, actor, raw)
	}
}

// readInboxActivity reads and validates an inbox POST: it decodes the bounded
// body, requires a non-empty actor field (#999), and verifies the HTTP
// signature before any processing. On failure it writes the error response
// and returns ok=false.
func readInboxActivity(w http.ResponseWriter, r *http.Request) (raw map[string]any, ok bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboundJSONBody))
	if err != nil {
		writeJSONError(w, r, http.StatusBadRequest, "read error")
		return nil, false
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		writeJSONError(w, r, http.StatusBadRequest, "invalid JSON")
		return nil, false
	}
	actorField, _ := raw["actor"].(string)
	if actorField == "" {
		writeJSONError(w, r, http.StatusBadRequest, "missing actor")
		return nil, false
	}
	if err := verifyInboxRequest(r.Context(), fedHTTPClient, r, body, actorField); err != nil {
		// Special case per SWICG/Mastodon guidance: when the actor's key
		// endpoint returns 410 Gone and the activity is a self-directed
		// Delete{actor}, treat this as confirmed deletion rather than an auth
		// failure. The 410 is itself the signal that the actor is gone, so
		// full signature verification is intentionally skipped here.
		if errors.As(err, new(errActorGone)) {
			actType, _ := raw["type"].(string)
			if actType == "Delete" && apObjectID(raw["object"]) == actorField {
				log.Printf("inbox: accepting self-Delete from gone actor %s", actorField)
				return raw, true
			}
		}
		log.Printf("inbox: verification failed for %s: %v", actorField, err)
		writeJSONError(w, r, http.StatusUnauthorized, "signature verification failed")
		return nil, false
	}
	return raw, true
}

// resolveSharedInboxActor determines the target local actor for an activity
// delivered to the shared inbox. Falls back to the relay actor for activities
// (like Accept) that don't name a specific local target.
func resolveSharedInboxActor(cfg *Config, db *sql.DB, raw map[string]any) *ActorRecord {
	prefix := "https://" + cfg.Domain + "/org/"
	activityType, _ := raw["type"].(string)

	var targetURL string
	switch activityType {
	case "Follow":
		targetURL, _ = raw["object"].(string)
	case "Undo":
		if obj, ok := raw["object"].(map[string]any); ok {
			targetURL = apObjectID(obj["object"])
		}
	}

	if strings.HasPrefix(targetURL, prefix) {
		slug := strings.SplitN(strings.TrimPrefix(targetURL, prefix), "/", 2)[0]
		if actor, err := getActorBySlug(db, slug); err == nil {
			return actor
		}
	}

	// Fallback: relay actor handles Accept and unroutable activities.
	actor, err := getActorBySlug(db, cfg.RelayActorName)
	if err != nil {
		return nil
	}
	return actor
}

// apObjectID extracts the id from an AP object field that may be either a
// bare URI string or an object map.
func apObjectID(v any) string {
	switch obj := v.(type) {
	case string:
		return obj
	case map[string]any:
		id, _ := obj["id"].(string)
		return id
	}
	return ""
}

// handleFollowActivity processes an inbound Follow for an org or tag actor:
// resolve the follower's inbox, persist the follow, then mail the signed
// Accept in a goroutine so the HTTP handler returns immediately.
// addFollower persists the relationship; sendAccept performs the actual POST.
func handleFollowActivity(w http.ResponseWriter, r *http.Request, client *DansalClient, actorField string, addFollower func(inboxURL string) error, sendAccept func(inboxURL string)) {
	inboxURL, err := resolveInboxURL(r.Context(), client, actorField)
	if err != nil {
		log.Printf("inbox: resolve actor %s: %v", actorField, err)
		inboxURL = ""
	}
	if inboxURL == "" {
		writeJSONError(w, r, http.StatusBadRequest, "could not resolve actor inbox")
		return
	}
	if err := addFollower(inboxURL); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	go sendAccept(inboxURL)
	w.WriteHeader(http.StatusAccepted)
}

// handleUndoActivity processes an inbound Undo{Follow}, removing the follow
// recorded by the undo's actor (falling back to the embedded Follow object's
// actor field). Shared by org and tag inboxes.
func handleUndoActivity(w http.ResponseWriter, r *http.Request, raw map[string]any, actorField string, removeFollower func(actorURI string) error) {
	obj, _ := raw["object"].(map[string]any)
	if obj == nil {
		writeJSONError(w, r, http.StatusBadRequest, "missing object")
		return
	}
	objType, _ := obj["type"].(string)
	if objType != "Follow" {
		writeJSONError(w, r, http.StatusBadRequest, "only Undo{Follow} supported")
		return
	}
	undoActor := actorField
	if undoActor == "" {
		undoActor, _ = obj["actor"].(string)
	}
	if undoActor == "" {
		writeJSONError(w, r, http.StatusBadRequest, "missing actor")
		return
	}
	if err := removeFollower(undoActor); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func processInboxActivity(w http.ResponseWriter, r *http.Request, cfg *Config, db *sql.DB, client *DansalClient, actor *ActorRecord, raw map[string]any) {
	activityType, _ := raw["type"].(string)
	actorField, _ := raw["actor"].(string)

	// Signature verification happened in readInboxActivity, which also
	// rejected empty actor fields (#999) before any processing.

	switch activityType {
	case "Follow":
		handleFollowActivity(w, r, client, actorField,
			func(inboxURL string) error { return addFollower(db, actor.OrgID, actorField, inboxURL) },
			func(inboxURL string) { sendAccept(cfg, actor, raw, actorField, inboxURL) },
		)

	case "Undo":
		handleUndoActivity(w, r, raw, actorField,
			func(undoActor string) error { return removeFollower(db, actor.OrgID, undoActor) },
		)

	case "Accept":
		// Accept{Follow}: update our outbound follow state to accepted.
		if followActivityID := apObjectID(raw["object"]); followActivityID != "" {
			if err := updateFollowStateByActivityID(db, followActivityID, "accepted"); err != nil {
				log.Printf("inbox Accept: update follow state for %s: %v", followActivityID, err)
			}
		}
		w.WriteHeader(http.StatusAccepted)

	case "Create", "Announce":
		eventObj := extractAPEventObject(raw)
		if eventObj == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		fe := apObjectToFederatedEvent(eventObj, actorField)
		if fe.APID == "" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if err := upsertFederatedEvent(db, fe); err != nil {
			log.Printf("inbox: upsert federated event %s: %v", fe.APID, err)
		}
		w.WriteHeader(http.StatusAccepted)

	case "Update":
		obj, _ := raw["object"].(map[string]any)
		if obj == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if t, _ := obj["type"].(string); t != "Event" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		fe := apObjectToFederatedEvent(obj, actorField)
		if fe.APID == "" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		// Only allow updates from the actor that originally created the event.
		if storedActor, err := getFederatedEventActor(db, fe.APID); err == nil {
			if storedActor != actorField {
				log.Printf("inbox: Update rejected for %s: actor %q not owner (owner: %q)", fe.APID, actorField, storedActor)
				w.WriteHeader(http.StatusAccepted)
				return
			}
		} else if err != sql.ErrNoRows {
			log.Printf("inbox: check event actor for %s: %v", fe.APID, err)
		}
		if err := upsertFederatedEvent(db, fe); err != nil {
			log.Printf("inbox: update federated event %s: %v", fe.APID, err)
		}
		w.WriteHeader(http.StatusAccepted)

	case "Delete":
		apID := apObjectID(raw["object"])
		if apID != "" {
			// Only allow deletion by the actor that created the event.
			if storedActor, err := getFederatedEventActor(db, apID); err == nil {
				if storedActor != actorField {
					log.Printf("inbox: Delete rejected for %s: actor %q not owner (owner: %q)", apID, actorField, storedActor)
					w.WriteHeader(http.StatusAccepted)
					return
				}
				if err := deleteFederatedEvent(db, apID); err != nil {
					log.Printf("inbox: delete federated event %s: %v", apID, err)
				}
			} else if err != sql.ErrNoRows {
				log.Printf("inbox: check event actor for delete %s: %v", apID, err)
			}
			// ErrNoRows: event not in DB, nothing to do.
		}
		w.WriteHeader(http.StatusAccepted)

	default:
		writeJSONError(w, r, http.StatusBadRequest, "unsupported activity type")
	}
}

// extractAPEventObject unwraps a Create{Event} or Announce{Create{Event}} activity
// and returns the inner Event AP object, or nil if the structure is not recognised.
func extractAPEventObject(raw map[string]any) map[string]any {
	obj, _ := raw["object"].(map[string]any)
	if obj == nil {
		return nil
	}
	switch t, _ := obj["type"].(string); t {
	case "Event":
		return obj
	case "Create":
		inner, _ := obj["object"].(map[string]any)
		if inner != nil {
			if t2, _ := inner["type"].(string); t2 == "Event" {
				return inner
			}
		}
	}
	return nil
}

func apObjectToFederatedEvent(obj map[string]any, actorID string) FederatedEvent {
	apID, _ := obj["id"].(string)
	name, _ := obj["name"].(string)
	startTime, _ := obj["startTime"].(string)
	endTime, _ := obj["endTime"].(string)
	eventURL, _ := obj["url"].(string)

	description, _ := obj["content"].(string)
	if description == "" {
		description, _ = obj["summary"].(string)
	}

	var locationName string
	if loc, ok := obj["location"].(map[string]any); ok {
		locationName, _ = loc["name"].(string)
	}

	var imageURL string
	if attachments, ok := obj["attachment"].([]any); ok {
		for _, a := range attachments {
			att, ok := a.(map[string]any)
			if !ok {
				continue
			}
			mt, _ := att["mediaType"].(string)
			if strings.HasPrefix(mt, "image/") {
				imageURL, _ = att["url"].(string)
				break
			}
		}
	}

	var tags []string
	if tagList, ok := obj["tag"].([]any); ok {
		for _, t := range tagList {
			tag, ok := t.(map[string]any)
			if !ok {
				continue
			}
			if tp, _ := tag["type"].(string); tp == "Hashtag" {
				if n, _ := tag["name"].(string); n != "" {
					tags = append(tags, strings.TrimPrefix(n, "#"))
				}
			}
		}
	}

	// The event URL is rendered as an outbound redirect target
	// (federatedEventHandler) and must not be trusted unvalidated — a
	// crafted activity could otherwise plant an open-redirect link on our
	// own domain (#1000). Drop it rather than rejecting the whole activity.
	if eventURL != "" && validateAPURL(eventURL) != nil {
		eventURL = ""
	}

	rawBytes, _ := json.Marshal(obj)
	return FederatedEvent{
		APID:         apID,
		ActorID:      actorID,
		Name:         name,
		StartTime:    startTime,
		EndTime:      endTime,
		URL:          eventURL,
		LocationName: locationName,
		Description:  description,
		ImageURL:     imageURL,
		Tags:         tags,
		RawJSON:      string(rawBytes),
		ReceivedAt:   time.Now().Unix(),
	}
}

// validateAPURL returns an error if rawURL is not a safe, routable https URL.
// It blocks non-https schemes, empty hosts, and known local/internal hostnames.
// DNS-level blocking is handled by safeFedDial at connect time.
func validateAPURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("ActivityPub URL must be https with a non-empty host: %q", rawURL)
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "local" ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".lan") {
		return fmt.Errorf("ActivityPub URL targets a local hostname: %q", rawURL)
	}
	return nil
}

func resolveInboxURL(ctx context.Context, _ *DansalClient, actorURI string) (string, error) {
	if err := validateAPURL(actorURI); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, actorURI, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/activity+json")
	resp, err := fedHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("actor fetch returned HTTP %d", resp.StatusCode)
	}
	var actor map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRemoteJSONBody)).Decode(&actor); err != nil {
		return "", err
	}
	inbox, _ := actor["inbox"].(string)
	if err := validateAPURL(inbox); err != nil {
		return "", fmt.Errorf("actor inbox URL invalid: %w", err)
	}
	return inbox, nil
}

func sendAccept(cfg *Config, actor *ActorRecord, followActivity map[string]any, followerURI, inboxURL string) {
	base := actorURL(cfg, actor.OrgSlug)
	accept := Activity{
		Context: APContext,
		Type:    "Accept",
		ID:      base + "#accept-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Actor:   base,
		Object:  followActivity,
		To:      []string{followerURI},
	}
	body, err := json.Marshal(accept)
	if err != nil {
		log.Printf("sendAccept marshal: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := postToInbox(ctx, inboxURL, actorKeyID(cfg, actor.OrgSlug), actor.PrivateKeyPEM, body); err != nil {
		log.Printf("sendAccept post: %v", err)
	}
}

func buildAPEvent(cfg *Config, slug string, e Event) APEvent {
	base := actorURL(cfg, slug)
	eventID := fmt.Sprintf("https://%s/events/%d", cfg.Domain, e.ID)

	var published string
	if t, ok := parseTime(e.CreatedAt); ok {
		published = t.UTC().Format(time.RFC3339)
	}

	eventURL := e.URL
	if eventURL == "" {
		eventURL = eventID
	}

	var updated string
	if t, ok := parseTime(e.ChangedAt); ok {
		updated = t.UTC().Format(time.RFC3339)
	} else {
		updated = published
	}

	apEvent := APEvent{
		Type:         "Event",
		ID:           eventID,
		Name:         e.Title,
		Content:      e.Description,
		MediaType:    "text/html",
		StartTime:    e.StartTime,
		EndTime:      e.EndTime,
		Published:    published,
		Updated:      updated,
		AttributedTo: base,
		To:           []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:           []string{base + "/followers"},
		URL:          eventURL,
		Organizer:    map[string]string{"type": "Group", "id": base},
	}
	if l := e.Location; l != nil {
		locationName := l.Location
		if locationName == "" {
			locationName = l.Town
		}
		if locationName != "" {
			place := &APPlace{Type: "Place", Name: locationName}
			place.Latitude = l.Latitude
			place.Longitude = l.Longitude
			if l.Address != "" || l.Zipcode != "" || l.Town != "" || l.Country != "" {
				place.Address = &APPostalAddress{
					Type:            "PostalAddress",
					StreetAddress:   l.Address,
					PostalCode:      l.Zipcode,
					AddressLocality: l.Town,
					AddressCountry:  l.Country,
				}
			}
			apEvent.Location = place
		}
	}
	for _, tag := range e.Tags {
		apEvent.Tag = append(apEvent.Tag, APHashtag{
			Type: "Hashtag",
			Name: "#" + tag,
			Href: fmt.Sprintf("https://%s/tags/%s", cfg.Domain, tag),
		})
	}
	if e.ImageURL != "" {
		apEvent.Attachment = []APDocument{{
			Type:      "Document",
			MediaType: "image/jpeg", // honest: apImageURL points at ?format=jpeg (#1054)
			URL:       apImageURL(cfg, e.ImageURL),
			Name:      e.Title,
		}}
	}
	return apEvent
}

// apImageURL returns the event image URL as an absolute https URL pointing at
// the JPEG variant (?format=jpeg). The canonical /api/v1/images/{id} URL serves
// Content-Type: image/avif, so declaring image/jpeg while linking there is both
// a spec violation and breaks Mastodon previews; the format parameter returns a
// genuine image/jpeg response (#1054).
func apImageURL(cfg *Config, imageURL string) string {
	if imageURL == "" {
		return ""
	}
	if imageURL[0] == '/' {
		imageURL = "https://" + cfg.Domain + imageURL
	}
	if strings.Contains(imageURL, "?") {
		return imageURL + "&format=jpeg"
	}
	return imageURL + "?format=jpeg"
}

// buildNoteContent renders a human-readable HTML summary of an event for
// inclusion as the Note content, so Mastodon and other Note-only clients can
// display event posts in timelines and profile pages.
func buildNoteContent(cfg *Config, e Event) string {
	eventURL := e.URL
	if eventURL == "" {
		eventURL = fmt.Sprintf("https://%s/events/%d", cfg.Domain, e.ID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<p><a href="%s">%s</a></p>`, html.EscapeString(eventURL), html.EscapeString(e.Title))
	if e.StartTime != "" {
		if t, err := time.Parse(time.RFC3339, e.StartTime); err == nil {
			fmt.Fprintf(&b, "<p>📅 %s", t.Format("02.01.2006 15:04"))
			if e.EndTime != "" {
				if te, err := time.Parse(time.RFC3339, e.EndTime); err == nil {
					if t.Year() == te.Year() && t.Month() == te.Month() && t.Day() == te.Day() {
						fmt.Fprintf(&b, "–%s", te.Format("15:04"))
					} else {
						fmt.Fprintf(&b, " – %s", te.Format("02.01.2006 15:04"))
					}
				}
			}
			b.WriteString("</p>")
		}
	}
	if l := e.Location; l != nil {
		var parts []string
		if l.Location != "" {
			parts = append(parts, l.Location)
		}
		if l.Town != "" {
			parts = append(parts, l.Town)
		}
		if len(parts) > 0 {
			fmt.Fprintf(&b, "<p>📍 %s</p>", html.EscapeString(strings.Join(parts, ", ")))
		}
	}
	if e.Description != "" {
		var md bytes.Buffer
		if err := goldmark.Convert([]byte(e.Description), &md); err == nil {
			b.WriteString(sanitizeMarkdownHTML(md.String()))
		} else {
			b.WriteString(html.EscapeString(e.Description))
		}
	}
	if len(e.Tags) > 0 {
		b.WriteString("<p>")
		for i, tag := range e.Tags {
			if i > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, `<a href="https://%s/tags/%s" class="mention hashtag" rel="tag">#<span>%s</span></a>`,
				cfg.Domain, html.EscapeString(tag), html.EscapeString(tag))
		}
		b.WriteString("</p>")
	}
	return b.String()
}

func buildNoteFromEvent(cfg *Config, slug string, e Event) APNote {
	base := actorURL(cfg, slug)
	eventID := fmt.Sprintf("https://%s/events/%d", cfg.Domain, e.ID)
	eventURL := e.URL
	if eventURL == "" {
		eventURL = eventID
	}
	var published, updated string
	if t, ok := parseTime(e.CreatedAt); ok {
		published = t.UTC().Format(time.RFC3339)
	}
	if t, ok := parseTime(e.ChangedAt); ok {
		updated = t.UTC().Format(time.RFC3339)
	} else {
		updated = published
	}
	note := APNote{
		Type:         "Note",
		ID:           eventID,
		AttributedTo: base,
		Content:      buildNoteContent(cfg, e),
		Published:    published,
		Updated:      updated,
		To:           []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:           []string{base + "/followers"},
		URL:          eventURL,
	}
	for _, tag := range e.Tags {
		note.Tag = append(note.Tag, APHashtag{
			Type: "Hashtag",
			Name: "#" + tag,
			Href: fmt.Sprintf("https://%s/tags/%s", cfg.Domain, tag),
		})
	}
	if e.ImageURL != "" {
		note.Attachment = []APDocument{{
			Type:      "Document",
			MediaType: "image/jpeg", // honest: apImageURL points at ?format=jpeg (#1054)
			URL:       apImageURL(cfg, e.ImageURL),
			Name:      e.Title,
		}}
	}
	return note
}

func buildCreateActivity(cfg *Config, slug string, e Event) Activity {
	base := actorURL(cfg, slug)
	eventID := fmt.Sprintf("https://%s/events/%d", cfg.Domain, e.ID)
	return Activity{
		Type:   "Create",
		ID:     eventID + "/activity",
		Actor:  base,
		Object: buildNoteFromEvent(cfg, slug, e),
		To:     []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:     []string{base + "/followers"},
	}
}

func buildUpdateActivity(cfg *Config, slug string, e Event) Activity {
	base := actorURL(cfg, slug)
	eventID := fmt.Sprintf("https://%s/events/%d", cfg.Domain, e.ID)
	note := buildNoteFromEvent(cfg, slug, e)
	note.Updated = time.Now().UTC().Format(time.RFC3339)
	return Activity{
		Type:   "Update",
		ID:     fmt.Sprintf("%s/activities/update-%d", eventID, time.Now().UnixNano()),
		Actor:  base,
		Object: note,
		To:     []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:     []string{base + "/followers"},
	}
}

func buildDeleteActivity(cfg *Config, slug string, eventID int) Activity {
	base := actorURL(cfg, slug)
	apEventID := fmt.Sprintf("https://%s/events/%d", cfg.Domain, eventID)
	return Activity{
		Type:   "Delete",
		ID:     apEventID + "/activities/delete",
		Actor:  base,
		Object: APTombstone{Type: "Tombstone", ID: apEventID},
		To:     []string{"https://www.w3.org/ns/activitystreams#Public"},
		CC:     []string{base + "/followers"},
	}
}

// resolveActorFromInput resolves a webfinger address (@user@host) or AP URL to
// the canonical AP actor URL and its inbox URL.
func resolveActorFromInput(ctx context.Context, _ *http.Client, input string) (apID, inboxURL string, err error) {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "@") {
		parts := strings.SplitN(strings.TrimPrefix(input, "@"), "@", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", fmt.Errorf("invalid webfinger address")
		}
		resource := "acct:" + parts[0] + "@" + parts[1]
		wfURL := "https://" + parts[1] + "/.well-known/webfinger?resource=" + url.QueryEscape(resource)
		if err := validateAPURL(wfURL); err != nil {
			return "", "", fmt.Errorf("invalid webfinger domain: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, wfURL, nil)
		if err != nil {
			return "", "", err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := fedHTTPClient.Do(req)
		if err != nil {
			return "", "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", "", fmt.Errorf("webfinger returned HTTP %d", resp.StatusCode)
		}
		var wf struct {
			Links []struct {
				Rel  string `json:"rel"`
				Type string `json:"type"`
				Href string `json:"href"`
			} `json:"links"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxRemoteJSONBody)).Decode(&wf); err != nil {
			return "", "", err
		}
		for _, l := range wf.Links {
			if l.Rel == "self" && l.Href != "" {
				apID = l.Href
				break
			}
		}
		if apID == "" {
			return "", "", fmt.Errorf("no self link in webfinger response")
		}
	} else if strings.HasPrefix(input, "https://") {
		apID = input
	} else {
		return "", "", fmt.Errorf("expected @user@host or https:// URL")
	}

	if err := validateAPURL(apID); err != nil {
		return "", "", fmt.Errorf("invalid actor URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apID, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/activity+json")
	resp, err := fedHTTPClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("actor fetch returned HTTP %d", resp.StatusCode)
	}
	var actor map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRemoteJSONBody)).Decode(&actor); err != nil {
		return "", "", err
	}
	inboxURL, _ = actor["inbox"].(string)
	if inboxURL == "" {
		return "", "", fmt.Errorf("no inbox in actor response")
	}
	if err := validateAPURL(inboxURL); err != nil {
		return "", "", fmt.Errorf("actor inbox URL invalid: %w", err)
	}
	return apID, inboxURL, nil
}

func sendFollowActivity(cfg *Config, actor *ActorRecord, followeeAPID, followeeInbox string) (followActivityID string, err error) {
	base := actorURL(cfg, actor.OrgSlug)
	followActivityID = base + "/activities/follow-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	follow := Activity{
		Context: APContext,
		Type:    "Follow",
		ID:      followActivityID,
		Actor:   base,
		Object:  followeeAPID,
	}
	body, err := json.Marshal(follow)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := postToInbox(ctx, followeeInbox, actorKeyID(cfg, actor.OrgSlug), actor.PrivateKeyPEM, body); err != nil {
		return "", err
	}
	return followActivityID, nil
}

func sendUndoFollow(cfg *Config, actor *ActorRecord, followeeAPID, followeeInbox, followActivityID string) error {
	base := actorURL(cfg, actor.OrgSlug)
	undo := Activity{
		Context: APContext,
		Type:    "Undo",
		ID:      base + "/activities/undo-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Actor:   base,
		Object: Activity{
			Type:   "Follow",
			ID:     followActivityID,
			Actor:  base,
			Object: followeeAPID,
		},
	}
	body, err := json.Marshal(undo)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return postToInbox(ctx, followeeInbox, actorKeyID(cfg, actor.OrgSlug), actor.PrivateKeyPEM, body)
}
