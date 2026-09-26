---
name: e2e-testing
description: Write or debug dansal's Playwright e2e tests (e2e/tests/journeys/*.spec.ts). Use when adding a new journey spec, driving the admin UI with Playwright, seeding org/location/event fixtures, or diagnosing a flaky/hanging e2e test against the dev instance. Encodes the admin-picker cache gotcha (#1276), the mobile long-press-drawer pattern, dedup-safe fixture spacing, the feed-import SSRF constraint, and the loginAs() hang workaround.
---

# dansal e2e testing (Playwright)

Specs live in `e2e/tests/journeys/*.spec.ts`. Two projects — `desktop` and `mobile` (Pixel 7 viewport) — run every spec; **always verify both**, not just desktop. A spec that only works on one project isn't done.

## Running a spec against the dev instance

```bash
cd e2e
export NVM_DIR="$HOME/.config/nvm"; source "$NVM_DIR/nvm.sh"; nvm use default
export ADMIN_CLI=/usr/lib/dansal/dev/dansal_admin
export ADMIN_SOCKET=/var/lib/dansal/dev/dansal.sock
export DANSAL_MAIL_FILE=/var/lib/dansal-e2e-mail/dansal-e2e-mail.mbox
npx playwright test tests/journeys/<file>.spec.ts --project=desktop --retries=0 --reporter=line --workers=1
```

- `ADMIN_CLI`/`ADMIN_SOCKET` are needed because `dansal_admin` isn't on `PATH` and the dev instance's admin socket requires them explicitly.
- `DANSAL_MAIL_FILE` is only actually needed by specs using the fake-sendmail mailbox helpers (`board.spec.ts`, `suggest-wizard.spec.ts`) but is harmless to export always — see "Public forms" below for why the helper's own default doesn't work against dev.
- `--workers=1 --retries=0` while iterating: a failing test's worker restart re-runs a shared `beforeAll` and muddies traces otherwise.
- Runs print recurring `error: email already exists` lines to stderr — background noise from `createUsers()`/other concurrent activity, not a test failure signal. Ignore it; look at the actual pass/fail summary line.
- **Any product code change under test must already be deployed to `dev`** (`make build && sudo make deploy INSTANCE=dev` — see the `deploy` skill) before running against it. `go build`/`go test` passing locally does not mean the running dev instance has the fix.
- A single test run commonly takes 15–60s; background it (`run_in_background`/Monitor) rather than blocking on a foreground `Bash` call, especially when iterating repeatedly.

## Running against a scratch instance instead of dev

When the code under test isn't deployed to dev (and deploying needs sudo you don't have), run the API and web binaries from a scratch directory with their own config and DB and point the suite at them:

```bash
go build -o /tmp/scratch/dansal ./cmd/dansal && go build -o /tmp/scratch/dansal_web ./cmd/dansal_web && go build -o /tmp/scratch/dansal_admin ./cmd/dansal_admin
export BASE_URL=http://localhost:18080 API_URL=http://localhost:18000
export ADMIN_CLI=/tmp/scratch/dansal_admin ADMIN_SOCKET=/tmp/scratch/dansal.sock ADMIN_NO_SUDO=1   # helpers/seed.ts prefixes `sudo -n` otherwise
```

- Use `localhost`, not `127.0.0.1`: the web layer's CSRF/origin check requires the request host to match `domain` in `web.yaml`, and a mismatch shows up as a bare "Forbidden" on every form POST.
- Restart by PID (`pgrep -af`, `kill <pid>`), never `pkill -f "<path>"` from a Bash tool call — the pattern also matches the shell running the command, which kills it (exit 144).
- `sudo -n` is only allowed for `/usr/lib/dansal/dev/dansal_admin`; that's why `ADMIN_NO_SUDO=1` is needed for any other binary.

### Bootstrapping the scratch instance's config from scratch

`packaging/{config,web,webmin}.yaml` are real, working templates (not just documentation) — copy and `sed` them rather than writing config files by hand:

```bash
mkdir -p /tmp/scratch/images && cp packaging/config.yaml packaging/web.yaml packaging/webmin.yaml /tmp/scratch/
sed -i \
  -e 's|^  port: 8000|  port: 18000|' -e 's|^  listen: "127.0.0.1:8000"|  listen: "127.0.0.1:18000"|' \
  -e "s|^  db_path: /var/lib/dansal/calendar.db|  db_path: /tmp/scratch/calendar.db|" \
  -e "s|^  images_dir: /var/lib/dansal/images|  images_dir: /tmp/scratch/images|" \
  -e "s|^  admin_socket: /var/lib/dansal/dansal.sock|  admin_socket: /tmp/scratch/dansal.sock|" \
  -e "s|^  backup_dir: /var/lib/dansal/backups|  backup_dir: /tmp/scratch/backups|" \
  -e 's|^  base_url: ""|  base_url: "http://localhost:18000"|' \
  /tmp/scratch/config.yaml
sed -i \
  -e 's|^listen: "127.0.0.1:8080"|listen: "127.0.0.1:18080"|' -e 's|^domain: "events.example.com"|domain: "localhost"|' \
  -e 's|^dansal_url: "http://127.0.0.1:8000"|dansal_url: "http://127.0.0.1:18000"|' \
  -e "s|^db_path: /var/lib/dansal-web/web.db|db_path: /tmp/scratch/web.db|" \
  /tmp/scratch/web.yaml
```

Then create an admin user and log in via the raw API for a bearer token — the same "don't fight the login form" reasoning as `loginAs()` below, just for `curl` instead of Playwright:

```bash
/tmp/scratch/dansal_admin --socket /tmp/scratch/dansal.sock create-user --email admin@example.com --password TestPass123! --role admin
TOKEN=$(curl -s -X POST http://localhost:18000/api/v1/login -H "Content-Type: application/json" \
  -d '{"email":"admin@example.com","password":"TestPass123!"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")
curl -s -b "dsw_token=$TOKEN" http://localhost:18080/admin/fetchurls/new   # authRefreshMiddleware re-establishes dsw_user from just this cookie
```

**Prefer this curl-plus-scratch-instance path over a Playwright spec for a narrow check** — "does this template render the new `<option>`", "does this POST accept the new field and round-trip it into the edit page", "what does the API actually return for X" — where a single request-response pair answers the question. Reach for a real spec (or extend an existing one) instead when the check is a multi-step user journey (fill a form, navigate, verify a redirect) or needs to be a *permanent* regression test; a one-off curl session verifies the change today but leaves nothing behind for tomorrow.

## Auth: storageState, not per-test logins

`playwright.config.ts` + `global-setup.ts` log in **once** for the whole suite (admin) and save `.auth/admin.json` (`AUTH_FILE` from `helpers/auth.ts`). Every spec's `page` fixture and `beforeAll`'s `browser.newContext({ storageState: AUTH_FILE })` load pre-authenticated — never call `loginAs()` for the admin role.

**A second, non-admin login inside a test is a real trap.** `loginAs()` (the actual `/login` form) has reproducibly hung indefinitely for a second login in a test — reproduced in complete isolation, cause never root-caused (the login page itself is fine when checked manually; global-setup's own one real login always works). Workaround, not a fix — **sidestep the form entirely** with the shared `loginViaApi` helper (`helpers/seed.ts`):

```ts
const { context: viewerContext, page: viewerPage, token: viewerToken } =
  await loginViaApi(browser, VIEWER.email, VIEWER.password);
// ... use viewerPage / viewerToken ...
await viewerContext.close();
```

It logs in through the raw API and injects the resulting token as the `dsw_token` cookie — no need to also forge the signed `dsw_user` cookie, since `authRefreshMiddleware` (`cmd/dansal_web/session.go`) sees a valid `dsw_token` with no `dsw_user` and transparently re-establishes the full session (via `GET /api/v1/me`) on the very next request.

**Always get the context from `loginViaApi` itself — never call `browser.newContext(...)` yourself for this.** `playwright.config.ts`'s `use.storageState` (`AUTH_FILE`, the admin's saved session) is a per-project default that `browser.newContext()` silently inherits unless *every* key is overridden — a context created as `browser.newContext({ baseURL: BASE_URL })` still starts with the admin's already-*signed* `dsw_user` cookie present and valid, and `authRefreshMiddleware` only ever re-derives `dsw_user` when none is already present — so it never looks at the freshly-injected `dsw_token` at all. This doesn't look like a login failure: every request just silently succeeds as admin regardless of whose token is in `dsw_token` (an admin-only endpoint returning 200 instead of the expected 403 is the symptom). `loginViaApi` creates its context with an explicit empty `storageState` for exactly this reason — don't reintroduce the bug by passing your own context in.

For read-only API calls against a specific user's data, a lighter option than a full second browser context: just call the raw API directly with `Authorization: Bearer <token>` — `getTokenFromCookie(page)` reads the current session's token straight off cookies.

## Admin-picker caches: create org/location fixtures through the UI, not raw API (#1276)

`dansal_web`'s `DansalClient` caches `GetOrganizations`/`GetLocations` in memory (~1 minute TTL) for admin pickers (event edit form's org/location assignment, the events-list bulk quick-assign tool, the import preview's location-mapping table, series-new/series-edit selects). **Only the web layer's own create/edit/delete handlers invalidate that cache.** An org/location created by POSTing straight to the raw API server is invisible to every one of those pickers for up to the TTL — the fixture exists, but no dropdown/table shows it, and a `selectOption`/row-filter that expects it will time out.

Always create org/location fixtures through the real admin form:

```ts
async function createOrg(page: Page, name: string): Promise<number> {
  await page.goto("/admin/organizations/new");
  await page.fill("#name", name);
  await page.locator("#save-btn").click();
  await page.waitForURL("**/admin/organizations");
  // then look the id up by name via the raw (uncached) API — reads aren't cached
}
```

The same cache serves the public `/org/{slug}` page (it resolves the slug through the cached org list), so an API-created org 404s there for up to a minute. Create the org through `/admin/organizations/new`, then look up its id/`actor_name` by name via the API; the slug is `actor_name` or the name lowercased with runs of non-alphanumerics turned into `-`. `org-location-media.spec.ts` has a ready `createOrg` helper.

Same reasoning for locations (`#location`, `#town` under the `sec-address` nav section, `#loc-nav-toggle` opens it on mobile — see `locations.spec.ts`'s `openSection` helper). A location created via raw API is fine to use *once you have its ID*, as long as nothing later needs to find it by name in a cached picker.

If a spec needs an org page's "recurring events" or similar upcoming-events listing (`GetAllEventsByOrg`), prefer a **freshly created, dedicated org** over the shared seeded one (`seed.orgId`) — that endpoint fetches only the 100 oldest events for the org (no `limit=` passed, ascending `start_time`, `include_past=true`), and the shared seed org accumulates real event rows across every spec file's run. Past a few hundred events it can push brand-new future instances out of that window entirely — this already happened once (`series-lifecycle.spec.ts`).

## Fixture dates must dodge dedup, not just each other

`insertEvent`'s tier hierarchy (see the `event-import` skill) runs on *every* create, including a plain admin-form create — not just feed imports. Two fixture events can silently merge into one row if they land within tier 3/4's ±3h window at the same location (tier 3) or with the same title (tier 4, no location). Symptoms look like a missing/wrong event, not an error.

- Space same-location fixture events by **more than 3 hours**, or give them genuinely distinct locations.
- When two events *should* look like duplicates on purpose (e.g. testing manual merge), make sure at least one dedup signal differs by construction — e.g. one has a real location and the other doesn't, so tier 3 can never fire between them regardless of title (`event-lifecycle.spec.ts`'s merge-tool test relies on exactly this).
- `SeriesEvent.StartTime`/`Event`'s time fields come back as RFC3339 **strings** from the JSON API — sort with `new Date(a.start_time).getTime() - new Date(b.start_time).getTime()`, not bare subtraction (silently produces `NaN`/stable-no-op ordering on strings).
- A synthetic iCal `DTSTART`/`DTEND` only carries second precision — compare a server-echoed timestamp at second granularity (`Math.floor(ms / 1000)`), not exact milliseconds.

## Feed import testing: file upload, not a hosted test feed

The real fetch-URL import path (`cmd/dansal/fetchurl.go`'s `safeClient`) deliberately blocks loopback/private-IP addresses (SSRF protection) — there is no way to point it at a locally-hosted synthetic feed server from a test. The admin import form has a second ingestion path that doesn't touch the network at all: a file upload (`#file`), which runs through the *identical* parse → preview → confirm pipeline. Use that:

```ts
await page.goto("/admin/events/import");
await page.locator("#file").setInputFiles({
  name: "feed.ics",
  mimeType: "text/calendar",
  buffer: Buffer.from(icsText, "utf-8"),
});
await page.selectOption("#feed-type", "ical");
await page.locator('form.import-form button[type="submit"]').click();
```

A single-VEVENT feed renders the pre-filled "new event" form **in-process** (no URL change — `adminImportEventsHandler`'s `len(events) == 1` branch), not the preview table; wait for the form field value, not a `waitForURL`. Only 2+ events trigger the real preview/duplicate-status table.

The import-confirm step's `PreviewEvent` JSON (the hidden `event_N` fields the preview table round-trips) carries no `source`/`uid`/`fetch_source_id` — a confirmed "duplicate" merge therefore takes `insertEvent`'s plain (non-source) update branch, which refreshes `start_time`/`end_time`/`description` but leaves `title` untouched by design. Assert on those fields, not title, when checking a merge took effect.

## Mobile-only interaction patterns: reproduce the end state, don't simulate the gesture

Several admin list/table pages (`admin_events.html`, `admin_series_edit.html`) put their bulk-actions bar behind a mobile-only drawer that a **real long-press** (`touchstart` + `setTimeout`, not a tap) opens — Playwright has no built-in long-press simulation. Don't fight it: reproduce the JS end state directly via `page.evaluate`, matching what the real gesture handler does (check the template's own `enterMultiSelect()`/equivalent for the exact classes/attributes):

```ts
await page.evaluate(() => {
  document.body.classList.add("ms-active", "actions-open"); // names vary per page
  const btn = document.getElementById("mt-actions-btn");
  if (btn) (btn as HTMLButtonElement).hidden = false;
});
```

Checkbox selection itself (`.event-cb`, `.series-event-cb`, etc.) is also mobile-hidden by CSS but not gated behind the drawer — set `.checked = true` and dispatch a real `change` event directly rather than trying to click a hidden native input:

```ts
await page.locator(`tr[data-evt-id="${id}"] .event-cb`).evaluate((el) => {
  (el as HTMLInputElement).checked = true;
  el.dispatchEvent(new Event("change", { bubbles: true }));
});
```

Separately, per-row inline edit controls (e.g. `series-lifecycle.spec.ts`'s description textareas) are sometimes CSS-hidden below 640px in favour of a tap-to-open quick-edit popup — check `.isVisible()` on the desktop control first and branch to the mobile popup flow when it's not, rather than assuming one UI exists on both viewports.

A long scrollable table can also leave a submit button genuinely obstructed mid-retry on mobile (Playwright's scroll-and-click retry loop bouncing between intercepting elements) even though it's plainly visible in a screenshot. `scrollIntoViewIfNeeded()` then `.click({ force: true })` rather than chasing the animation.

## Public forms (board, suggest wizard, booking): mailbox path, token timing, throttles

Specs that drive an anonymous/public form (`board.spec.ts`, `suggest-wizard.spec.ts`) hit a cluster of anti-abuse mechanisms shared across `contactBoardPostHandler`, `suggestSubmitHandler`, and `bookingSubmitHandler` (`cmd/dansal_web`). All three surfaced as real failures while building #1260 — check this section first before treating one of these as a product bug.

- **`DANSAL_MAIL_FILE` must point at the dev instance's actual mbox, not the helper's default.** `helpers/mailbox.ts` defaults to `/tmp/dansal-e2e-mail.mbox`, but the `dansal@dev` systemd unit runs with `PrivateTmp=true` and its own `Environment=DANSAL_MAIL_FILE=/var/lib/dansal-e2e-mail/dansal-e2e-mail.mbox` — completely different, non-overlapping files. Any spec using `waitForManageToken`/`waitForBoardManageToken`/`waitForMailboxURL` needs:
  ```bash
  export DANSAL_MAIL_FILE=/var/lib/dansal-e2e-mail/dansal-e2e-mail.mbox
  ```
  added to the run command in this skill's "Running a spec" section above. Without it the wait just times out ("no match... after 15000 ms") with no hint that the mail was ever sent. `clearMailbox()`/`waitForMailboxURL` both honor this env var (via the same `MAIL_FILE` constant), so setting it once per shell covers the whole run.

- **`consumeFormToken`'s 1-second *minimum* age** (`formguard.go`) rejects a token used less than 1s after it was issued — an anti-bot check a real visitor clears naturally while filling a form, but Playwright's fill-and-submit can beat. Symptom: a generic "Form data invalid"/"Submission failed" error on first submit. Add a short wait (`page.waitForTimeout(1100)`) right before the final submit click, after confirming the submit button is enabled — see `board.spec.ts` and `submitSuggestWizard` in `suggest-wizard.spec.ts`.

- **Per-IP+User-Agent throttles block repeated local runs, not just real abuse.** `contactBoardPostHandler`'s `hasPendingSubmission`/`setPendingSubmission` and `suggestSubmitHandler`'s shared `publicThrottle` (also used by booking) both key on `sha256(ip+"|"+user-agent)` or `ip+"|"+user-agent` directly, with a multi-minute window (`FormTokenMaxAgeMins`, default 30; `PublicRateWindowMins`, default 10, limit 10 requests). Every Playwright run from the same machine shares one IP and, by default, one User-Agent — so a few reruns in a row of `board.spec.ts` or `suggest-wizard.spec.ts` (or a mix of both, since `publicThrottle` is shared across handlers) can trip "Too many submissions"/"Too many requests" even though each run uses a fresh browser context. Give the anonymous context a unique per-run `userAgent` so it fingerprints as a different visitor each time:
  ```ts
  const anonCtx = await browser.newContext({
    storageState: { cookies: [], origins: [] },
    userAgent: `Mozilla/5.0 (E2E <name> test ${Date.now()})`,
  });
  ```

- **`loginRateLimiter` (`/api/v1/login`, 5 req/min per IP by default) is IP-only — a per-run User-Agent doesn't help here.** Any spec doing more than one real login in quick succession (`loginViaApi`, the web layer's `/login` POST, invite/OIDC auto-login) draws from this same budget, and it's a *sliding* window: a rejected call doesn't consume a slot, but every accepted one sits in the window for a full minute regardless of which spec or run made it. A single isolated run of a login-heavy spec (`auth-totp.spec.ts`, `auth-invite.spec.ts`) stays well under 5/min on its own — this only bites when iterating on one of these specs rapidly, back-to-back, against the shared dev instance. Symptom: `loginViaApi` throws `"Too many login attempts"` even though the failing call's own test made only one or two login attempts. A `curl` probe confirming the limiter *looks* clear isn't enough to prove the next run is safe — that probe's own accepted call re-occupies a slot, and several rapid retries can keep the window full of your own prior attempts. Let it drain quietly (~70–90s with no login-triggering requests at all, including probes) rather than tightening the retry loop.

## API request gotchas in specs

- `PATCH` needs `Content-Type: application/merge-patch+json`; plain `application/json` gets `415`.
- Create endpoints that accept bulk bodies answer with an **array** — `POST /events` and `/musicians` return `[{…}]`, `POST /locations` returns `[{location, similar_locations}]` (`seedLocation` already unwraps it). Reading `.id` off the top level gives `undefined` and the next call goes to `/…/undefined`. Tolerate both: `const x = Array.isArray(b) ? b[0] : b`.
- After a form save that redirects back to the *same* URL, `waitForURL` can't tell "saved" from "not submitted yet"; poll the API (`expect.poll(...)`) for the outcome instead.

## Collapsed edit-form sections: check before you click

Clicking a section's nav item **toggles** it. A section that already holds data starts open (its `hasData` check passes), so a second click closes it and every input inside becomes "not visible". Use a helper that returns early when the section is already visible, and only then opens the mobile drawer toggle and clicks the item (`openSection` in `org-location-media.spec.ts`; `locations.spec.ts` has the drawer half).

## Cleaning up what a spec leaves behind

The "no cleanup expected" default below holds for events, orgs and locations. It does **not** hold for things that land in an admin review queue: pending event suggestions and feed suggestions pile up on the dashboard and eventually swamp real ones. `suggest-wizard.spec.ts` deletes the suggestion it created in a `finally` (`deleteSuggestionsByTitle`, best-effort so cleanup never masks the test's own failure). New helpers for setup/teardown live in `helpers/seed.ts`: `addOrgMember`, `deleteUser`, `gotoAdminEventsFor`.

CI sets `rate_limit: 1000` in the e2e instance's `config.yaml` (`.github/workflows/e2e.yml`): every spec's seed calls share one per-IP bucket, and the packaging default of 100/min runs out mid-suite.

## Everything else

- `unique(name)` (`` `${name} ${Date.now()}` ``) on every fixture title/org/location name — keeps repeated runs against the shared, persistent dev DB from colliding, and doubles as a readable marker for manual cleanup.
- `authedGet`/`authedJSON` helpers (`page.request.fetch` with `Authorization: Bearer <token>`) for API-level assertions after a UI flow — most specs duplicate a small local copy rather than importing a shared one; match that convention.
- No cleanup step is expected for events/orgs/locations created by a spec (see the review-queue exception above) (unlike `locations.spec.ts`'s location deletes, which exist only because of the geohash `UNIQUE` index forcing it) — leaving fixtures in the shared dev DB is accepted precedent, not an oversight.
- `waitForMailboxURL`/`waitForMail` (`helpers/mailbox.ts`) return the *first* match in the mbox file, not the latest. A spec that sends more than one email of the same link-shape across its run — or shares the file with another spec's leftover, never-cleared mail from a moment earlier — must call `clearMailbox()` immediately before the request that triggers the specific email it's about to wait for, not just once at the top of the test.
- The account-level API rate limiter (`cmd/dansal/account_rate_limit.go`, 30 req/min) self-clears within ~60–90s; a burst of failures across many concurrent spec files that look like generic `seedLocation`/API errors is often this, not a real regression — confirm via a direct curl probe before concluding otherwise.
