---
name: event-import
description: Work on the event import pipeline — feed fetching, parsing, location resolution, and duplicate detection. Use when touching findExistingEvent, previewDuplicateStatus, insertEvent, ensureLocation, fetchurl*.go, location aliases, or anything that imports events from external feeds (ical, rss, json/folkdance, gancio, kufer). Encodes the 5-tier dedup hierarchy, the shared preview/insert logic, and the location resolution + alias rules that must not be broken.
---

# Event import & deduplication

Two code paths ingest external events: **preview** (dry-run, shows new/exists/updated before an admin confirms) and **insert** (the real write). They must agree on what counts as a duplicate — do not change one without the other.

Key files (all in `cmd/dansal/`):
- `dedup.go` — `findExistingEvent()`: the **single shared implementation** of the dedup hierarchy. Added to end the drift where preview and insert each ran hand-copied versions (#1005).
- `preview.go` — `previewDuplicateStatus()`: read-only duplicate classification for the UI.
- `events.go` — `insertEvent()`: the write path.
- `fetchurl.go` + `fetchurl_*.go` — fetching and parsing feed formats.
- `dedup_test.go` — regression tests for the tier hierarchy and insert outcomes.

## The 5-tier dedup hierarchy (do not change the order)

`findExistingEvent` (`dedup.go:63`) returns the first tier that matches, as `DuplicateTier`:

1. **UID** — exact `events.uid` match. The only tier that works when start time is unavailable (`startTime` is a `*int64`, nil when the date failed to parse).
2. **URL** — exact `events.url` match **within ±3h of start_time**. The ±3h window is deliberate: a feed that reuses one generic URL (e.g. its homepage) must not lock this tier onto the first event ever imported with that URL and silently absorb unrelated later events (#702). Do not drop the window.
3. **`location_id` + start_time ±3h** — same venue + slot. No title check: titles get rewritten over an event's lifetime.
4. **`title` + start_time ±3h** — fallback when location is unresolved (feed gave no resolvable name) or tier 3 missed (e.g. after entity-decoding or a rename).
5. **`fetch_source_id` + start_time ±3h + fuzzy title overlap** (`titlesFuzzyOverlap`) — **low-confidence review hint, NOT an auto-merge match**. Returns `TierFuzzyReview` with the *candidate* row; the caller must insert as new and flag both rows for an admin to resolve via the merge UI. `previewDuplicateStatus` treats `TierFuzzyReview` the same as no match ("new"), since preview has no review state.

Constants: `threeHours = 3 * 60 * 60` (seconds), `titlesFuzzyOverlap` in the same package.

## Preview vs insert — what must stay true

- `previewDuplicateStatus` is **read-only**: it must never create rows. Its location lookup (preview.go:129-135) is a plain `SELECT`, whereas `insertEvent`'s caller resolves locations via `ensureLocation` (which *may create* a location row). Keep that split.
- Preview also checks `previewLocationUpdated()` — feed coordinates differing by more than `0.0001°` (~11 m) from the stored location mean the preview reports "updated", even when the event itself didn't change.
- Both decide update-vs-insert and which fields to preserve via the `ExistingEvent` struct (`dedup.go:24`).

## Location resolution & aliases

`ensureLocation` resolves a feed location name to a `location_id` in this order (`fetchurl.go:220-242`):

1. **OSM identity** — `osm_type`+`osm_id` exact match (preferred; backfilled onto name-matched rows so future lookups hit this tier).
2. **Exact name** — `location = ?`.
3. **Composite name-address** — `"name - address"` (Gancio's iCal export stores `LOCATION` that way).
4. **Alias** — `location_aliases.alias` lookup (`SELECT location_id FROM location_aliases WHERE alias=?`).

**Alias rule (preserve it):** when an admin manually maps a feed location to a DB location, the feed's name is appended as an alias (`syncLocationAliases`, `locations.go:208`) so future imports auto-match. The alias store is the `location_aliases` junction table (columns live there, not in `locations.aliases` — a legacy JSON column that was migrated to the junction table in v6, `main.go:1382`).

**Update policy when a location matches (`fetchurl.go:244-268`):**
- **Coordinates**: overwrite whenever the feed supplies them (corrected geodata must flow in from the source).
- **Text fields** (short_name, address, town, zipcode, country, region): **backfill-only** via `COALESCE(NULLIF(col,''), ?)` — preserve manual admin edits.
- **OSM identity**: backfill only when NULL.

## Feed formats

`parseBodyToRequests` dispatches on `src.Type` (`preview.go:198`): `json` (probed — may be gancio or TEC JSON), `folkdance-json`, `gancio-json`, otherwise iCal (`ics.ParseCalendar`). Accepted types: `ical, json, folkdance-json, gancio-json, rss, kufer` (fetchurl.go enum). Each has a `fetchurl_<format>.go` parser.

## Testing & final checks

- `dedup_test.go` exercises `findExistingEvent` against seeded rows — extend it whenever you touch tier semantics.
- A change here can silently corrupt imported data, so prefer adding a regression test over relying on manual testing.

```bash
go build ./...
go vet ./...
go test ./...
```

Then `make build` and `sudo make deploy INSTANCE=dev` (see the `deploy` skill).
