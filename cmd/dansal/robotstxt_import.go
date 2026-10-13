package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
)

// robotsUserAgent is the product token we match against User-agent: lines —
// just the product name, not the full UA string fetchClient actually sends
// ("dansal/1.0 (calendar feed importer; ...)"), matching how the spec says
// crawlers are meant to be addressed (e.g. "User-agent: Googlebot", not the
// full UA header). A site wanting to single dansal out writes
// "User-agent: dansal".
const robotsUserAgent = "dansal"

// robotsCacheTTL caps how long a fetched robots.txt is trusted before being
// re-fetched — a publisher changing it should take effect within this
// window without restarting the service.
const robotsCacheTTL = 24 * time.Hour

type robotsEntry struct {
	data       *robotstxt.RobotsData // nil only on a parse failure; TestAgent on a nil receiver would panic, so callers must check
	fetchedAt  time.Time
	crawlDelay time.Duration
}

// robotsImportCache is process-lifetime, not persisted: adminFetchAll runs
// inside the long-running dansal server process (both the dansal-fetch.timer
// path and `dansal_admin fetch-all` go through the same admin-socket command,
// handled by this same process — see admin.go's "fetch-all" case), so an
// in-memory cache here is shared across every fetch-all run, not just one.
// Losing it on a service restart just means robots.txt gets re-fetched and
// the Crawl-delay clock resets — not a correctness problem, same tradeoff
// the other in-memory caches in this package (credCache etc.) already make.
type robotsImportCache struct {
	mu            sync.Mutex
	byHost        map[string]robotsEntry
	hostLastFetch map[string]time.Time
}

var robotsCache = &robotsImportCache{
	byHost:        make(map[string]robotsEntry),
	hostLastFetch: make(map[string]time.Time),
}

// checkRobots reports whether the recurring importer may fetch rawURL right
// now, covering both robots.txt Allow/Disallow for our user-agent and any
// Crawl-delay the host declared (#1484, compliance G9 phase-66). Only call
// this from the recurring fetch-all path — cmd/dansal/recheck_source.go's
// single admin-initiated re-fetch of one event's original URL, and
// fetchurl_suggest.go's one-off preview/confirm, are deliberately exempt:
// robots.txt (RFC 9309) governs automated/recurring crawling, not a bounded
// fetch a human asked for directly. Once/if a suggested feed is approved
// into a standing fetch_sources row, it's on this recurring path from then
// on like any other source.
//
// Fails open (allowed=true) when robots.txt itself can't be fetched or
// parsed — same convention as the HIBP breach check elsewhere in this repo
// (cmd/dansal/users.go): a transient failure to reach a third party must not
// turn into a different, harder-to-notice failure here.
func checkRobots(ctx context.Context, rawURL string) (allowed bool, reason string) {
	if rule := robotsDisallowRule(ctx, rawURL); rule != "" {
		return false, "robots.txt disallows fetching this path for our user-agent"
	}
	return checkCrawlDelay(ctx, rawURL)
}

// errRobotsNotAcknowledged marks a feed that robots.txt disallows for dansal
// and whose organization/user has not yet accepted that restriction (#1545).
// Callers answer it with 409 so the UI can show the notice and ask again.
var errRobotsNotAcknowledged = errors.New("robots.txt restricts this feed; confirm to continue")

// robotsDisallowRule returns a human-readable description of the robots.txt
// rule that blocks rawURL for our user-agent, or "" when it is allowed or
// robots.txt could not be read (fail open, see fetchRobotsForHost).
func robotsDisallowRule(ctx context.Context, rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	entry := robotsEntryFor(ctx, u)
	if entry.data != nil && !entry.data.TestAgent(u.Path, robotsUserAgent) {
		return fmt.Sprintf("disallowed for %q on %s (path %s)", robotsUserAgent, u.Host, u.Path)
	}
	return ""
}

// robotsEntryFor returns the cached robots.txt entry for u's host, fetching it
// when missing or older than robotsCacheTTL.
func robotsEntryFor(ctx context.Context, u *url.URL) robotsEntry {
	host := u.Host
	robotsCache.mu.Lock()
	entry, ok := robotsCache.byHost[host]
	stale := !ok || time.Since(entry.fetchedAt) > robotsCacheTTL
	robotsCache.mu.Unlock()

	if stale {
		entry = fetchRobotsForHost(ctx, u)
		robotsCache.mu.Lock()
		robotsCache.byHost[host] = entry
		robotsCache.mu.Unlock()
	}
	return entry
}

// checkCrawlDelay reports whether the host's Crawl-delay has elapsed since our
// last fetch attempt to it. Allow/Disallow is checked separately by
// robotsDisallowRule, so an acknowledged source can still be spaced out.
func checkCrawlDelay(ctx context.Context, rawURL string) (allowed bool, reason string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return true, ""
	}
	entry := robotsEntryFor(ctx, u)
	if entry.crawlDelay > 0 {
		robotsCache.mu.Lock()
		last, seen := robotsCache.hostLastFetch[u.Host]
		robotsCache.mu.Unlock()
		if seen && time.Since(last) < entry.crawlDelay {
			return false, "Crawl-delay for this host has not elapsed yet"
		}
	}
	return true, ""
}

// noteRobotsFetchAttempt records that we just attempted to fetch something
// from rawURL's host, for Crawl-delay spacing — called once per actual fetch
// attempt (success or failure alike), not once per checkRobots call.
func noteRobotsFetchAttempt(rawURL string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return
	}
	robotsCache.mu.Lock()
	robotsCache.hostLastFetch[u.Host] = time.Now()
	robotsCache.mu.Unlock()
}

// fetchRobotsForHost fetches and parses http(s)://host/robots.txt. Scheme is
// taken from u since robots.txt is scoped per-origin (a host served over
// both http and https could in principle answer differently), though in
// practice fetch_sources URLs are expected to be https.
//
// Uses safeClient, not fetchClient (#1535): the host comes from the same
// admin/publisher-supplied fetch_source URL the main feed fetch already
// guards against SSRF for, so the robots.txt companion request needs the
// identical private/internal-address block — any publisher can otherwise
// point a feed at a loopback/link-local/metadata address and have this
// fetch hit it directly.
func fetchRobotsForHost(ctx context.Context, u *url.URL) robotsEntry {
	now := time.Now()
	robotsURL := u.Scheme + "://" + u.Host + "/robots.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return robotsEntry{fetchedAt: now}
	}
	resp, err := safeClient.Do(req)
	if err != nil {
		return robotsEntry{fetchedAt: now} // unreachable: fail open
	}
	defer resp.Body.Close()

	// FromResponse already implements the Google-spec status-code handling:
	// 2xx parses the body, 4xx is treated as "no robots.txt" (full allow),
	// 5xx as a temporary "full disallow". We still fail open on a genuine
	// parse error (malformed body on an announced 2xx), consistent with the
	// rest of this function.
	data, err := robotstxt.FromResponse(resp)
	if err != nil || data == nil {
		return robotsEntry{fetchedAt: now}
	}

	var delay time.Duration
	if g := data.FindGroup(robotsUserAgent); g != nil {
		delay = g.CrawlDelay
	}
	return robotsEntry{data: data, fetchedAt: now, crawlDelay: delay}
}

// robotsAckRequest is the optional acknowledgement a caller sends with a
// feed-creating request when robots.txt disallows the URL (#1545).
type robotsAckRequest struct {
	RobotsAck       bool   `json:"robots_ack"`
	RobotsAckReason string `json:"robots_ack_reason"`
}

// recordRobotsAck stores that someone accepted robots.txt restricting a feed,
// and mirrors it onto src so the same request can import it (#1545).
func recordRobotsAck(src *FetchSource, rule, reason, byName string) {
	src.RobotsAckAt = time.Now().UTC().Format(time.RFC3339)
	src.RobotsAckBy = byName
	src.RobotsAckReason = reason
	src.RobotsAckRule = rule
	db.Exec("UPDATE fetch_sources SET robots_ack_at = ?, robots_ack_by = ?, robots_ack_reason = ?, robots_ack_rule = ? WHERE id = ?",
		src.RobotsAckAt, src.RobotsAckBy, src.RobotsAckReason, src.RobotsAckRule, src.ID)
}
