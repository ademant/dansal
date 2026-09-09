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

## Everything else

- `unique(name)` (`` `${name} ${Date.now()}` ``) on every fixture title/org/location name — keeps repeated runs against the shared, persistent dev DB from colliding, and doubles as a readable marker for manual cleanup.
- `authedGet`/`authedJSON` helpers (`page.request.fetch` with `Authorization: Bearer <token>`) for API-level assertions after a UI flow — most specs duplicate a small local copy rather than importing a shared one; match that convention.
- No cleanup step is expected for events/orgs/locations created by a spec (unlike `locations.spec.ts`'s location deletes, which exist only because of the geohash `UNIQUE` index forcing it) — leaving fixtures in the shared dev DB is accepted precedent, not an oversight.
- The account-level API rate limiter (`cmd/dansal/account_rate_limit.go`, 30 req/min) self-clears within ~60–90s; a burst of failures across many concurrent spec files that look like generic `seedLocation`/API errors is often this, not a real regression — confirm via a direct curl probe before concluding otherwise.
