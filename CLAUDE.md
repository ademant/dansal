# dansal — Claude Code project instructions

## Workflow

Before implementing any non-trivial change:
1. **Discuss** the approach — explain the plan and tradeoffs, wait for confirmation
2. **Create a GitHub issue** with `gh issue create` describing the problem and solution, once the user gives the ok
3. **Implement** the fix
4. **Commit** with `Closes #<issue>` in the message so the issue is closed automatically on push

```bash
gh issue create --title "short description" --body "problem, solution, impact"
# then implement, then:
git commit -m "fix: description\n\nCloses #NNN\n\nCo-Authored-By: ..."
```

Discussion and issue creation (steps 1–2) do not have to be immediately followed by implementation (steps 3–4). The user may want to discuss and open issues for several features in a row, then come back and implement each one later, in any order. Only create an issue after the user has explicitly said ok to that specific proposal — don't create it as part of a general discussion. Each issue is still closed via its own `Closes #NNN` commit whenever it's eventually implemented.

Skip the discussion step only for obvious typos or single-line fixes, which may be implemented directly without an issue. Always create the issue before writing code for anything else.

**Batches of related issues**: when the user asks to analyse a range of issues and group them into an implementation order, encode that as GitHub labels (`phase-31`, `phase-32`, …) in dependency order rather than a scratch file — labels stay attached to the issues and survive across sessions. "Implement phase-N" then means: implement every issue with that label, one commit closing all of them together (multiple `Closes #NNN` lines). Build/deploy only when separately asked (see Build & deploy). Don't jump ahead to a later phase's issues even if related — phases are ordered because later ones assume earlier refactors landed.

**gofmt and doc comments**: `gofmt -w` converts straight quotes to curly quotes inside doc comments (comments immediately preceding a top-level declaration) — this is canonical Go 1.19+ formatting, not a mistake to undo. Run `gofmt -l <touched files>` after any refactor and `gofmt -w` to fix, same as `go vet`/`go test`.

## Go version

This project requires **Go 1.26+** (`go.mod` currently declares `go 1.26.3`). Before running `make build`, verify:

```bash
go version  # must print go1.26 or higher
```

Do not downgrade `go.mod`. If a new language or stdlib feature from 1.24+ is available and fits the problem, prefer it over a manual workaround.

## Build & deploy

**`make build` and `sudo make deploy` are not run automatically after every change.** Implement, commit (`go build ./...`/`go vet ./...`/`gofmt -l`/`go test ./...` are still run every time to verify the change compiles and passes), and stop there — only run `make build` / `sudo make deploy INSTANCE=<name>` when the user explicitly asks for a build or deploy in that message. Several changes/commits commonly stack up before the user asks for a deploy; don't build+deploy after each one on your own initiative.

Always rebuild and redeploy **all** binaries together, regardless of which files changed. A selective deploy risks shipping stale binaries (see issue #147).

```bash
# Build all four binaries in parallel (as regular user)
make build

# Install binaries and restart a specific instance (requires sudo, no build step)
sudo make deploy INSTANCE=dev
sudo make deploy INSTANCE=prod
```

`make deploy INSTANCE=<name>` installs binaries to `/usr/lib/dansal/<name>/`, updates systemd template units, and restarts the named instance (`dansal@<name>`, `dansal-web@<name>`, `dansal-webmin@<name>`). Each instance has its own binary directory so dev/test/prod can run different versions independently. It does **not** build — run `make build` first as the regular user (sudo doesn't have `go` in PATH).

`dev` is the default deploy target after implementing a change. Only deploy to `test` or `prod` when the user explicitly asks for that instance by name — never assume a change destined for dev should also go to test/prod.

**Setting up a new instance** (first time only):
```bash
sudo scripts/install-instance
```
The script asks for ports, domain, SMTP, and optionally generates a webmin mTLS client certificate. It calls `make setup-instance` internally, patches the config files, and installs binaries.

Manual alternative (if you prefer not to use the script):
```bash
# 1. Create dirs, install template configs, enable units
sudo make setup-instance INSTANCE=prod

# 2. Edit the three config files
sudo nano /etc/dansal/prod/config.yaml   # port, base_url, smtp
sudo nano /etc/dansal/prod/web.yaml      # listen, domain, dansal_url
sudo nano /etc/dansal/prod/webmin.yaml   # listen, session_secret

# 3. Install binaries and start services
make build
sudo make deploy INSTANCE=prod
```

**Updating an existing instance:**
```bash
make build
sudo make deploy INSTANCE=prod
```

Do **not** run `go build ./cmd/...` and install manually — always use `make build && sudo make deploy INSTANCE=<name>` to ensure all binaries are updated together.

## Project layout

| Path | Purpose |
|---|---|
| `cmd/dansal/` | REST API server (`/usr/lib/dansal/<instance>/dansal`) |
| `cmd/dansal_web/` | Web frontend + ActivityPub (`/usr/lib/dansal/<instance>/dansal-web`) |
| `cmd/dansal_admin/` | Admin CLI (`/usr/lib/dansal/<instance>/dansal_admin`) |
| `cmd/dansal_webmin/` | Web admin UI (`/usr/lib/dansal/<instance>/dansal-webmin`) |
| `cmd/dansal_web/templates/` | Go HTML templates |
| `cmd/dansal_web/i18n.yaml` | Translations (12 languages: `br`, `ca`, `cs`, `de`, `en`, `es`, `fr`, `it`, `nl`, `pl`, `pt`, `uk`) |

## Key facts

- **DB**: SQLite at `/var/lib/dansal/<instance>/calendar.db`; config at `/etc/dansal/<instance>/config.yaml` (API), `/etc/dansal/<instance>/web.yaml` (web), `/etc/dansal/<instance>/webmin.yaml` (webmin)
- **Services**: `dansal` (API, port 8000), `dansal-web` (frontend, port 8080 behind nginx), `dansal-webmin` (admin UI)
- **DB migrations**: append idempotent `db.Exec(...)` calls at the end of `runMigrations()` in `cmd/dansal/main.go`; also update `createTables()` for fresh installs
- **Maps**: always use `attachTileLayer(map)` from `base.html` — never call `L.tileLayer` directly
- **Email, Telegram, Matrix**: always send in a goroutine — never block the HTTP handler
- **New i18n strings**: add to all 12 language sections in `i18n.yaml` (`br`, `ca`, `cs`, `de`, `en`, `es`, `fr`, `it`, `nl`, `pl`, `pt`, `uk`)

## DB migration safety-net pattern

`createTables()` is designed for fresh installs — it creates all tables including `schema_migrations` and marks **all** versions applied via `INSERT OR IGNORE`. On existing DBs that lacked `schema_migrations`, this incorrectly skips migrations. After each migration block in `runMigrations()`, add an unconditional structural check that self-heals at zero cost once correct:

```go
// Safety net: ensure column exists even if migration was pre-marked
{
    var n int
    db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('table') WHERE name='column'").Scan(&n)
    if n == 0 {
        db.Exec("ALTER TABLE table ADD COLUMN column TYPE DEFAULT value")
    }
}
```

For table population (e.g. seed data), use a COUNT-based check:
```go
{
    var n int
    db.QueryRow("SELECT COUNT(*) FROM tags").Scan(&n)
    if n == 0 {
        db.Exec("INSERT OR IGNORE INTO tags ...")
    }
}
```

## Event deduplication (5 tiers)

`previewDuplicateStatus()` in `cmd/dansal/preview.go` and `insertEvent()` in `cmd/dansal/events.go` both delegate to a single shared finder, `findExistingEvent(q querier, title, url string, startTime *int64, locationID int64, uid string, fetchSourceID int) (ExistingEvent, DuplicateTier, error)` in `cmd/dansal/dedup.go`. Before this existed, the two callers hand-copied the SQL and had already drifted — don't reintroduce a second copy of the tier logic in either caller; add new tiers to `findExistingEvent` only. `startTime` is a pointer because `previewDuplicateStatus` may have an unparseable date string (nil skips all tiers but UID); `insertEvent` always passes a non-nil pointer. `dedup_test.go` asserts both callers agree tier-by-tier — extend it when adding a tier.

The hierarchy (`DuplicateTier` enum: `TierNone`, `TierUID`, `TierURL`, `TierLocation`, `TierTitle`, `TierFuzzyReview`):

1. **UID** — exact match on feed UID
2. **URL** — exact match on event URL
3. **location_id + start_time ±3h** — when `locationID > 0`, no title check. Titles get rewritten over an event's lifetime (placeholder → confirmed lineup → cancellation notice → backup act), so once UID/URL have both missed, same venue + same slot is already a strong enough signal to auto-merge on its own.
4. **title + start_time ±3h** (no location) — when `locationID == 0` (feed location name didn't resolve to a DB location). Title is still required here since, without a location signal, time-only matching would be far too promiscuous.
5. **(insertEvent only) fetch_source_id + start_time ±3h + fuzzy title overlap** — when tiers 1–4 all miss (e.g. the venue *also* changed and the feed regenerated the UID). Too low-confidence to auto-merge, so instead of guessing it inserts as new and flags both rows via `needs_duplicate_review`/`duplicate_of_id`, notifying admins to resolve via the existing `/admin/events` bulk-merge UI. `previewDuplicateStatus` treats this tier as a hint only, not a match — it still reports "new".

Tier 4 fires as a fallback when tiers 1–3 all miss. This catches: (a) feeds using city-level location names vs. DB venue names, (b) location name mismatches caused by HTML-entity decoding or venue renames that make `ensureLocation` create a new location row (giving a new `locationID` that tier 3 can't match against the event's old `location_id`).

`insertEvent(q querier, in EventInput) (id int, shortCode, outcome string, err error)` takes an `EventInput` struct (not positional params — it grew past 28 of those). Both call sites (`createEventFromRequest`, the clone-event handler) build a named `EventInput{...}` literal; do the same for any new call site rather than adding another positional wrapper.

## Event org access-control helpers

`cmd/dansal/events.go` has two shared helpers used everywhere an event/timetable/image/registration handler needs to check the caller belongs to the relevant organization — don't reintroduce a hand-rolled `!orgID.Valid || !isOrgMember(...)` check:

- `requireExistingOrgMember(w, callerID int, existingOrgID sql.NullInt64) bool` — single check, writes 403 and returns false on failure. Used in `deleteEvent`, `cancelEvent`, approve-for-publish, `images.go`, `register.go`, `suggest_manage.go`.
- `requireEventOrg(w, role string, callerID int, existingOrgID sql.NullInt64, targetOrgID *int, requireTarget bool) bool` — covers the PUT vs PATCH asymmetry: admin is unrestricted; publisher/user must belong to the event's *existing* org, and (only when `requireTarget` is true) must also belong to the *target* org being moved to. `updateEvent` (PUT) passes `requireTarget = userRole != RolePublisher`; `patchEvent` always passes `true` since it resolves the target org from the existing one when the request omits `organization_id`. `timetableAuthCheck` calls it with `targetOrgID = nil, requireTarget = false` (no target org concept for timetable edits).

`isOrgMember(userID, orgID int) bool` is the single-org-column membership check these build on. It's distinct from `locationHasOrgMember` (`locations.go`), which checks the many-to-many `location_organizations` join table — don't try to merge the two, they answer different questions.

## Location aliases

`locations.aliases` (JSON array column) stores alternate names a location is known by in external feeds. Used in:
- **Auto-matching** during import (`adminImportEventsHandler` in `admin_import.go`): feed location name is looked up against DB location names and all their aliases
- **Persisting manual overrides** (`adminImportConfirmHandler`): when admin manually maps a feed location to a DB location, the feed name is automatically appended as a new alias so future imports auto-match
- **Merge** (`admin_locations.go`): dropped location names are preserved as aliases on the surviving location

## Index page data flow

`indexHandler` (`cmd/dansal_web/frontend.go`) fetches events **once** via `client.GetEvents` (up to 100 future published events) and passes the same slice to the template. The template renders three views from that single dataset:

- **Map**: `eventsGeoJSON` projects events to a compact JSON blob (only fields needed for map popups, short key names). Events without coordinates are excluded.
- **Weekly table** and **future list**: server-rendered `<li>` elements with `data-*` attributes; JS filters/arranges client-side.

The full `Event` struct (with all API fields) is never sent to the browser — `eventsGeoJSON` deliberately strips fields like `geohash`, `osm_id`, `notes_md`, `aliases`, `contact_*`, `series_id`, etc.

## Images

- Stored as AVIF (default) or JPEG in `config.Server.ImagesDir`, one file per event: `{event_id}.avif`
- Resized at upload time to fit within `ImageXMax` × `ImageYMax` (default 1024×1024)
- Served directly via `http.ServeFile` in `getEventImage` — no per-request resizing or device adaptation
- `imgCache` is an in-memory set of event IDs with images, populated at startup from disk scan; avoids `os.Stat` per event in list responses
- For mobile-optimised serving, the right approach is `srcset` in templates + a `?w=` query param generating cached smaller variants — **not** User-Agent detection (breaks HTTP caching)

## Theme and language persistence

- **Theme**: stored in browser `localStorage` key `colorScheme` (`auto`/`dark`/`light`). Pure client-side — server never reads it. Instance-wide default can be set in `web.yaml` (`dark_mode: auto|light|dark`), which sets the initial `<html>` class server-side to avoid flash on load.
- **Language**: stored in cookie `dsw_lang` (1-year expiry, set server-side). Server reads it in `detectLang()` before rendering. Cannot use `localStorage` alone because Go templates are rendered server-side and need the language before the page is built. `Accept-Language` header could be used as a fallback default for new visitors.

## Responsive design

All mobile adaptation is CSS (`@media` queries) and JS — the server sends identical HTML to all clients. Never use User-Agent detection for layout decisions: it's unreliable and fragments the HTTP cache (`Vary: User-Agent` is very cache-unfriendly).

## Navigation deep-links

Use `geo:lat,lon?q=lat,lon` links alongside OpenStreetMap links to let mobile users open OsmAnd or other native navigation apps. Both `event.html` and `location.html` have these. No `target="_blank"` on `geo:` links — they open native apps, not browser tabs.

## Unsaved-changes guard pattern

Admin forms use `_formDirty` / `_markDirty()` / `safeGoBack()` to warn before navigating away with unsaved changes:

- `_formDirty = false` at page load; `_markDirty()` sets it to `true` and attaches a `beforeunload` listener.
- `safeGoBack()` checks `_formDirty` and calls `history.back()` — used by the `←` back button.
- On form submit, `_formDirty = false` is set immediately so the `beforeunload` listener does not fire.
- Room edit links inside a form check `_formDirty` before navigating away.

## Parent-child locations (rooms / buildings)

Locations can have a parent via `parent_id`. A child (room) inherits empty address, coordinates, and parking from its parent at read time via `inheritLocationFields()` in `cmd/dansal/locations.go`. Rules:

- `locationsJSON` template func flattens top-level locations and their children using `"RoomName — BuildingName"` labels for disambiguation in dropdowns.
- The room picker on the event edit form is click-to-open (`onfocus="searchRooms('')"`).
- `adminLocationDashboardHandler` inherits parent org IDs for the "create event" button when a room has no direct org associations.
- On the location edit form, address/coordinates/parking fields are disabled for child locations (they inherit from parent).

## IndexNow / site_settings

Runtime config that must not require a service restart lives in the `site_settings` table in the web DB, read via `siteSettingsCache` (10 s TTL) in `cmd/dansal_web/sitecache.go`. The webmin UI (`cmd/dansal_webmin/`) writes to this table. Example: the IndexNow API key is stored here and served dynamically at `GET /{key}.txt` without a restart.

## Tags vocabulary

`tags` table: `slug TEXT PRIMARY KEY, name TEXT, category TEXT CHECK(category IN ('format','level','type'))`. `validateTags()` rejects unknown slugs. Categories:
- `format`: `bal-folk`, `fest-noz`, `session`, `concert`, `festival`, `open-air`, `workshop`, `music-course`
- `type`: `dance-workshop`, `musician-workshop`
- `level`: `beginners`, `intermediate`, `advanced`

## has_* boolean fields (legacy — prefer tags)

`has_ball`, `has_workshop`, `has_festival` are legacy boolean columns on the `events` table that predate the tag system. The tag system is the authoritative source of truth. **Whenever a change touches any `has_*` field, switch that code path to use tags instead:**

- `has_ball` → `sliceContains(e.Tags, "bal-folk") || sliceContains(e.Tags, "fest-noz")`
- `has_workshop` → `sliceContains(e.Tags, "workshop") || sliceContains(e.Tags, "dance-workshop") || sliceContains(e.Tags, "musician-workshop") || sliceContains(e.Tags, "music-course")`
- `has_festival` → `sliceContains(e.Tags, "festival")`

Full removal of `has_*` from the API and DB is tracked in issue #871 (Layer 1: `dansal_web` display) and a future Layer 2 issue (API + DB + wp-dansal).
