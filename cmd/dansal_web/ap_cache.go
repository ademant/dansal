package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// #1471: when an event/org post federates, ~1000 fediverse instances fetch
// the same AP object within 1-2 minutes. Every one of those requests ran
// the same API round trip(s) + JSON build from scratch, so identical work
// ran concurrently ~1000x and contended for backend capacity human
// visitors shared during the burst. This file coalesces concurrent
// requests for the same object via singleflight and caches the marshalled
// result for a short TTL, so a whole burst costs one build per object per
// TTL window instead of one per request.
//
// Only AP responses are affected (isAPRequest at each call site) — the
// HTML event page carries a per-request CSP nonce and session-dependent
// content and is deliberately out of scope.

// apCacheTTL is deliberately short: long enough to flatten a fan-out burst
// (which plays out over 1-2 minutes), short enough that an edit is visible
// within seconds even without the Update/Delete invalidation hooks below.
const apCacheTTL = 10 * time.Second

// apBuildTimeout bounds the work singleflight's "leader" does on behalf of
// every caller sharing that call — independent of any single caller's own
// request context, since one client disconnecting (or being slow) must
// never cancel the fetch for the hundreds of others waiting on the same
// result.
const apBuildTimeout = 10 * time.Second

// apResponseEntry is a cached, pre-marshalled AP JSON response. etag/
// lastModified are the zero value when the underlying response has no
// conditional-GET metadata (the outbox, and tombstones, currently don't —
// matching the behavior before this cache existed).
type apResponseEntry struct {
	status       int
	body         []byte
	etag         string
	lastModified time.Time
	expiresAt    time.Time
}

// apCacheResult carries a build's outcome through singleflight.Do, which
// only has room for one return value besides the error.
type apCacheResult struct {
	entry     apResponseEntry
	cacheable bool
}

var (
	// apEventCache / apEventGroup: key is the event ID (int).
	apEventCache sync.Map
	apEventGroup singleflight.Group

	// apOutboxCache / apOutboxGroup: key is "slug|page|offset" (string) —
	// matching outboxHandler's own cache key, since the response shape
	// differs by those params.
	apOutboxCache sync.Map
	apOutboxGroup singleflight.Group
)

func init() {
	go sweepAPCaches()
}

// sweepAPCaches periodically drops expired entries so the cache only ever
// holds objects actually touched in the last TTL window (#1471's bounded-
// memory requirement) — without this, a long-tail of IDs fetched once
// during a burst would sit in the map forever.
func sweepAPCaches() {
	ticker := time.NewTicker(apCacheTTL)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		apEventCache.Range(func(k, v any) bool {
			if now.After(v.(apResponseEntry).expiresAt) {
				apEventCache.Delete(k)
			}
			return true
		})
		apOutboxCache.Range(func(k, v any) bool {
			if now.After(v.(apResponseEntry).expiresAt) {
				apOutboxCache.Delete(k)
			}
			return true
		})
	}
}

// invalidateAPEventCache drops the cached Note/Tombstone for id. Called
// from the Update/Delete delivery hooks (admin_events.go) so the object a
// remote server fetches right after receiving our Update/Delete activity
// is never stale — the TTL alone would otherwise still serve the old
// version for up to apCacheTTL after a same-moment edit.
func invalidateAPEventCache(id int) {
	apEventCache.Delete(id)
}

// serveAPEventEntry writes entry as the HTTP response, honoring
// If-None-Match/If-Modified-Since against whatever conditional-GET
// metadata entry carries (none, for the outbox and tombstones).
func serveAPEventEntry(w http.ResponseWriter, r *http.Request, entry apResponseEntry) {
	if entry.etag != "" {
		w.Header().Set("ETag", entry.etag)
	}
	if !entry.lastModified.IsZero() {
		w.Header().Set("Last-Modified", entry.lastModified.Format(http.TimeFormat))
		if ims := r.Header.Get("If-Modified-Since"); ims != "" {
			if since, perr := http.ParseTime(ims); perr == nil && !entry.lastModified.After(since) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	if entry.etag != "" && r.Header.Get("If-None-Match") == entry.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/activity+json")
	w.WriteHeader(entry.status)
	w.Write(entry.body)
}

// buildAPNoteEntry builds the cacheable AP representation of an
// already-fetched event: a Note, with the same conditional-GET metadata
// (ETag `"id-changed_at"`, Last-Modified from ChangedAt) eventHandler
// computed inline before this cache existed. Shared by the #1471 fast
// path (getAPEventResponse) and eventHandler's own fallback for the rare
// cases that bypass it, so there's exactly one place that turns an Event
// into its AP Note representation.
func buildAPNoteEntry(ctx context.Context, cfg *Config, client *DansalClient, event Event) apResponseEntry {
	apETag := fmt.Sprintf(`"%d-%s"`, event.ID, event.ChangedAt)
	var lastMod time.Time
	if ct, cerr := parseUnixOrRFC3339(event.ChangedAt); cerr == nil && !ct.IsZero() {
		lastMod = ct.UTC().Truncate(time.Second)
	}
	slug := cfg.RelayActorName
	if event.OrganizationID != nil {
		if org, oerr := client.GetOrganization(ctx, *event.OrganizationID); oerr == nil {
			slug = effectiveSlug(org)
		}
	}
	note := buildNoteFromEvent(cfg, slug, event)
	note.Context = APContext
	body, _ := json.Marshal(note)
	return apResponseEntry{
		status: http.StatusOK, body: body, etag: apETag, lastModified: lastMod,
		expiresAt: time.Now().Add(apCacheTTL),
	}
}

// buildAPTombstoneEntry builds the cacheable 410 Tombstone for a
// confirmed-gone event id. Tombstones carry no ETag/Last-Modified, matching
// the behavior before this cache existed.
func buildAPTombstoneEntry(cfg *Config, id int) apResponseEntry {
	body, _ := json.Marshal(APTombstone{
		Context: APContext,
		Type:    "Tombstone",
		ID:      fmt.Sprintf("https://%s/events/%d", cfg.Domain, id),
	})
	return apResponseEntry{status: http.StatusGone, body: body, expiresAt: time.Now().Add(apCacheTTL)}
}

// getAPEventResponse returns the cached (or freshly built, with concurrent
// callers for the same id coalesced via singleflight) AP response for
// event id. cacheable is false for the edge cases eventHandler's own
// fallback logic must still handle itself (a never-allocated id possibly
// redirected elsewhere, or a genuine upstream error) — those are rare
// enough, and need enough extra branching, that they're not worth folding
// into this cache.
//
// The singleflight call deliberately uses its own bounded context
// (apBuildTimeout), not any individual caller's request context: one
// client disconnecting or being slow must never cancel the fetch for the
// hundreds of others sharing it.
func getAPEventResponse(cfg *Config, client *DansalClient, id int) (apResponseEntry, bool) {
	if v, ok := apEventCache.Load(id); ok {
		entry := v.(apResponseEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry, true
		}
	}
	v, _, _ := apEventGroup.Do(strconv.Itoa(id), func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), apBuildTimeout)
		defer cancel()
		event, err := client.GetEvent(ctx, id)
		if err == nil {
			entry := buildAPNoteEntry(ctx, cfg, client, event)
			apEventCache.Store(id, entry)
			return apCacheResult{entry, true}, nil
		}
		if errors.Is(err, errExpired) {
			entry := buildAPTombstoneEntry(cfg, id)
			apEventCache.Store(id, entry)
			return apCacheResult{entry, true}, nil
		}
		return apCacheResult{}, nil
	})
	res := v.(apCacheResult)
	return res.entry, res.cacheable
}

// buildAPOutboxEntry mirrors outboxHandler's own response-building
// (actor.go) exactly, just returning the marshalled bytes instead of
// writing them directly, plus a bool for whether the upstream fetch
// succeeded (ok=false means the caller should fall back to its own
// error response, same as outboxHandler did before this cache existed).
func buildAPOutboxEntry(ctx context.Context, cfg *Config, client *DansalClient, actor *ActorRecord, slug, pageParam, offsetParam string) (apResponseEntry, bool) {
	base := actorURL(cfg, slug)
	outboxURL := base + "/outbox"

	params := url.Values{}
	params.Set("is_published", "true")
	params.Set("include_past", "true")
	if actor.OrgID != 0 {
		params.Set("organization_id", strconv.Itoa(actor.OrgID))
	}

	if pageParam != "true" {
		// limit=1 is enough: X-Total-Count reflects the full count even
		// when the page is truncated, so the collection root reports the
		// real totalItems without downloading the history.
		params.Set("limit", "1")
		_, total, err := client.GetEventsFilteredWithTotal(ctx, params)
		if err != nil {
			return apResponseEntry{}, false
		}
		col := OrderedCollection{
			Context:    APContext,
			Type:       "OrderedCollection",
			ID:         outboxURL,
			TotalItems: total,
			First:      outboxURL + "?page=true",
		}
		body, _ := json.Marshal(col)
		return apResponseEntry{status: http.StatusOK, body: body, expiresAt: time.Now().Add(apCacheTTL)}, true
	}

	offset := 0
	if n, err := strconv.Atoi(offsetParam); err == nil && n >= 0 {
		offset = n
	}
	params.Set("limit", strconv.Itoa(outboxPageSize))
	params.Set("offset", strconv.Itoa(offset))
	events, total, err := client.GetEventsFilteredWithTotal(ctx, params)
	if err != nil {
		return apResponseEntry{}, false
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
	body, _ := json.Marshal(page)
	return apResponseEntry{status: http.StatusOK, body: body, expiresAt: time.Now().Add(apCacheTTL)}, true
}

// getAPOutboxResponse is getAPEventResponse's twin for GET
// /org/{name}/outbox (#1471). The cache key includes page/offset since
// those change the response shape; requireAPSignature (the route's own
// middleware) already runs before outboxHandler's body on every request,
// cache hit or not, so authorized-fetch verification is unaffected.
func getAPOutboxResponse(cfg *Config, client *DansalClient, actor *ActorRecord, slug, pageParam, offsetParam string) (apResponseEntry, bool) {
	key := slug + "|" + pageParam + "|" + offsetParam
	if v, ok := apOutboxCache.Load(key); ok {
		entry := v.(apResponseEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry, true
		}
	}
	v, _, _ := apOutboxGroup.Do(key, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), apBuildTimeout)
		defer cancel()
		entry, ok := buildAPOutboxEntry(ctx, cfg, client, actor, slug, pageParam, offsetParam)
		if ok {
			apOutboxCache.Store(key, entry)
		}
		return apCacheResult{entry, ok}, nil
	})
	res := v.(apCacheResult)
	return res.entry, res.cacheable
}
