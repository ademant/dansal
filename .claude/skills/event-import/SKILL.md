---
name: event-import
description: Change the dansal event import pipeline: feed fetch/detection/parsing (fetchurl*.go, preview.go parseBodyToRequests, importFromSource), new feed type, dedup (findExistingEvent tiers, insertEvent, previewDuplicateStatus), location resolution (ensureLocation, location_aliases), suggest-preview/JSON-LD. Use for any import/dedup/location-matching bug or feature.
---

# Import & dedup (cmd/dansal)

FILES
- `dedup.go` `findExistingEvent` — ONLY implementation of tier logic (#1005). `dedup_test.go` asserts preview+insert agree per tier; extend it for any tier change.
- `preview.go` `previewDuplicateStatus` (read-only), `parseBodyToRequests` (preview/suggest dispatcher).
- `events.go` `insertEvent(q, EventInput{…})` (write path; build named EventInput literals).
- `fetchurl.go` `importFromSource` (scheduled fetch dispatcher), `ensureLocation`, `safeClient` (SSRF-blocks private IPs), `detectFetchType`, `fetchTypeHeaders`, `extractVCalendarBody`, `importICalBody`.
- `fetchurl_folkdance.go` `validFetchType`; `fetchurl_rss.go` `rssMismatchHint`; `duplicates.go` `pairCollision` (resolve flagged pairs).

TIERS (first match wins; constant `threeHours`; details also in CLAUDE.md)
1 UID exact (works with nil startTime). 2 URL exact AND |Δstart|<3h (window is deliberate, #702: feeds reuse one homepage URL — CLAUDE.md omits the window). 3 location_id + 3h: auto-merge only same feed source or fuzzy/identical title from another feed; manual create / unrelated title → `TierLocationReview` (#1424). 4 title + 3h (when locationID==0). 5 same fetch_source_id + 3h + `titlesFuzzyOverlap` → `TierFuzzyReview`.
- Review tiers (`IsReview()`): insert as new, flag both (`needs_duplicate_review`, `duplicate_of_id`); preview reports "new" + `duplicate_hint_id`.
- Dedup runs on EVERY create incl. admin form/API — affects tests/fixtures.

PREVIEW vs INSERT INVARIANTS
- Preview never writes: location lookup is plain SELECT; insert path uses `ensureLocation` (may create).
- Preview "updated" also when feed coords differ >0.0001° from stored (`previewLocationUpdated`).

FEED TYPES: `ical json folkdance-json gancio-json rss kufer jcal jsonld`.
- New type MUST be added to `validFetchType` + `importFromSource` switch + `parseBodyToRequests` switch (#1377). parseBodyToRequests has no rss/kufer case: default = iCal parse (ok for kufer pages, wrong for real RSS).
- jcal: converted with `jcalToICalText` then the iCal path (`importICalBody`); needs `Accept: application/calendar+json` from `fetchTypeHeaders` (static per-type map — never store credentials there).
- Content type lies (#1387): generic xml → check URL for ics/ical, then `bodyLooksLikeICal`, else rss; rss+xml/atom+xml → rss. `extractVCalendarBody` cuts the VCALENDAR out of HTML wrappers before `ics.ParseCalendar`. Misrouted body → `rssMismatchHint` names what it really is.
- Suggest: `POST /api/v1/events/suggest-preview` (anonymous, own rate limit) parses an untyped HTML body as JSON-LD (`looksLikeHTML`, #1417). `GET /events/suggest/url-dates?url=` (dansal-web) returns distinct start dates for the wizard's advisory date warning.

LOCATION RESOLUTION (`ensureLocation`, in order)
1 OSM type+id (backfilled onto name matches) 2 exact name 3 `"name - address"` (Gancio LOCATION) 4 alias in `location_aliases` table (the `locations.aliases` JSON column was migrated away, #740 — CLAUDE.md is stale here).
- Manual mapping in import confirm appends the feed name as alias (`syncLocationAliases`). Location merge keeps dropped names as aliases.
- On match: coords overwrite when the feed supplies them; text fields (short_name,address,town,zipcode,country,region) backfill-only `COALESCE(NULLIF(col,''),?)`; OSM id backfill only when NULL.

TESTING
- Offline import: admin import file upload (`#file`) or suggest import tab with an HTML file containing `application/ld+json` — same parse path, no network (fetch path can't reach localhost by design).
- `go build ./... && go vet ./... && go test ./...`; prefer a regression test over manual checks (silent data corruption risk).
