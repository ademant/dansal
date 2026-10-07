---
name: e2e-testing
description: Write/run/debug dansal Playwright e2e specs (e2e/tests/journeys/*.spec.ts) against dev or a scratch instance; seeding fixtures, second-user login, mailbox/token/throttle issues on public forms, mobile drawers, flaky/hanging tests. Also use for one-off curl verification on a scratch instance.
---

# Playwright e2e (e2e/)

RUN AGAINST dev (code under test must already be deployed to dev)
```bash
cd e2e; export NVM_DIR="$HOME/.config/nvm"; . "$NVM_DIR/nvm.sh"; nvm use default
export ADMIN_CLI=/usr/lib/dansal/dev/dansal_admin ADMIN_SOCKET=/var/lib/dansal/dev/dansal.sock
export DANSAL_MAIL_FILE=/var/lib/dansal-e2e-mail/dansal-e2e-mail.mbox   # NOT the helper default (/tmp; unit has PrivateTmp)
npx playwright test tests/journeys/<f>.spec.ts --project=desktop --retries=0 --reporter=line --workers=1
```
- Always run BOTH `--project=desktop` and `--project=mobile` (Pixel 7). One-project pass = not done.
- Runs take 15–60 s → `run_in_background`. Stderr `error: email already exists` = noise.

SCRATCH INSTANCE (code not deployed to dev)
```bash
S=<scratch>; mkdir -p $S/images
go build -o $S/dansal ./cmd/dansal && go build -o $S/dansal_web ./cmd/dansal_web && go build -o $S/dansal_admin ./cmd/dansal_admin
cp packaging/{config,web,webmin}.yaml $S/
sed -i -e 's|^  port: 8000|  port: 18000|' -e 's|^  listen: "127.0.0.1:8000"|  listen: "127.0.0.1:18000"|' \
  -e "s|/var/lib/dansal/calendar.db|$S/calendar.db|" -e "s|/var/lib/dansal/images|$S/images|" \
  -e "s|/var/lib/dansal/dansal.sock|$S/dansal.sock|" -e "s|/var/lib/dansal/backups|$S/backups|" \
  -e 's|^  base_url: ""|  base_url: "http://localhost:18000"|' $S/config.yaml
sed -i -e 's|^listen: "127.0.0.1:8080"|listen: "127.0.0.1:18080"|' -e 's|^domain: "events.example.com"|domain: "localhost"|' \
  -e 's|^dansal_url: "http://127.0.0.1:8000"|dansal_url: "http://127.0.0.1:18000"|' -e "s|/var/lib/dansal-web/web.db|$S/web.db|" $S/web.yaml
export BASE_URL=http://localhost:18080 API_URL=http://localhost:18000 ADMIN_CLI=$S/dansal_admin ADMIN_SOCKET=$S/dansal.sock ADMIN_NO_SUDO=1
$S/dansal_admin --socket $S/dansal.sock create-user --email admin@example.com --password TestPass123! --role admin
TOKEN=$(curl -s -X POST localhost:18000/api/v1/login -H 'Content-Type: application/json' -d '{"email":"admin@example.com","password":"TestPass123!"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
curl -s -b "dsw_token=$TOKEN" http://localhost:18080/admin/...   # dsw_user re-derived by authRefreshMiddleware
```
- Use `localhost` not 127.0.0.1 (origin must equal web.yaml `domain`; else bare "Forbidden" on POSTs).
- Target needs `rate_limit: 1000` and `account_mutation_rate_limit: 600` in config.yaml (e2e/README.md), fake-sendmail mbox for mail specs.
- Stop by PID (`pgrep -af`, `kill`); NEVER `pkill -f <path>` (matches the tool's own shell → exit 144).
- `sudo -n` allowed only for `/usr/lib/dansal/dev/dansal_admin` → other binaries need `ADMIN_NO_SUDO=1`.
- Single request/response question → curl on scratch instance. Multi-step journey or permanent regression → spec.

AUTH
- Admin: suite logs in once (`global-setup.ts` → `.auth/admin.json` = `AUTH_FILE`); never `loginAs()` for admin.
- Second user: `const {context,page,token}=await loginViaApi(browser,email,pw)` (helpers/seed.ts). `loginAs()` form login hangs on a 2nd login (unresolved).
- NEVER build that context yourself with `browser.newContext({baseURL})`: it inherits admin storageState → signed admin `dsw_user` wins → requests silently run as admin (symptom: admin-only endpoint 200 instead of 403).
- API-only reads as a user: `Authorization: Bearer <token>`; `getTokenFromCookie(page)`.

FIXTURES
- Org/location: create via admin UI (`/admin/organizations/new` → `#name`, `#save-btn`; location `#location`, `#town` in `sec-address`), then look up id via API. Raw-API creates are invisible to cached pickers and `/org/{slug}` for ~1 min (#1276; only web handlers invalidate). Helpers: `createOrg` (org-location-media.spec.ts), `openSection` (locations.spec.ts).
- Events index cache is 2 s (`eventsTTL`, ETag) → API-seeded events appear on `/` quickly; org/location/musician caches 30–60 s.
- Org pages with upcoming/recurring lists: use a fresh org, not `seed.orgId` (`GetAllEventsByOrg` returns the 100 oldest incl. past; shared org overflows).
- Names: `unique(name)` (`${name} ${Date.now()}`).
- Dedup applies to every create: same title ±3h no location → merged; same venue ±3h manual → both flagged for review (#1424). Space fixtures >3h or vary venue; to force/avoid a match control the signals. Flagged pairs land in admin review → delete them. Check: `GET /api/v1/events/{id}/duplicate-check`.
- Admin API create = published; unpublished → PATCH `{"is_published":false}` (merge-patch).
- Cleanup: events/orgs/locations may stay; suggestions/feed suggestions/flagged pairs MUST be deleted (`finally`, `deleteSuggestionsByTitle`). Helpers: `addOrgMember`, `deleteUser`, `gotoAdminEventsFor`.

API GOTCHAS
- PATCH needs `Content-Type: application/merge-patch+json` (else 415).
- `POST /events`, `/musicians` → array `[{…}]`; `POST /locations` → `[{location,similar_locations}]`. `const x=Array.isArray(b)?b[0]:b`.
- `apiPost`/`apiGet` throw on non-2xx with status+body.
- Times are RFC3339 strings: sort via `new Date(a).getTime()`; iCal has second precision → compare `Math.floor(ms/1000)`.
- Save that redirects to the same URL: `expect.poll` the API, not `waitForURL`.

FEED IMPORT TESTS: fetch path blocks localhost (SSRF) → upload instead:
```ts
await page.goto("/admin/events/import");
await page.locator("#file").setInputFiles({name:"feed.ics",mimeType:"text/calendar",buffer:Buffer.from(ics,"utf-8")});
await page.selectOption("#feed-type","ical");
await page.locator('form.import-form button[type="submit"]').click();
```
1 VEVENT → prefilled new-event form in-process (wait for field value, no URL change); ≥2 → preview table. Confirmed merge from preview updates start/end/description, NOT title.

PUBLIC FORMS (board, suggest wizard, booking)
- Form token min age 1 s (`consumeFormToken`): `await page.waitForTimeout(1100)` before final submit. Symptom: "Form data invalid".
- Throttles keyed on ip+UA (`publicThrottle` shared, `hasPendingSubmission`): anonymous context with unique UA per run:
  `browser.newContext({storageState:{cookies:[],origins:[]},userAgent:\`Mozilla/5.0 (E2E x ${Date.now()})\`})`
- `/api/v1/login` limiter: 5/min per IP, sliding, UA doesn't help. "Too many login attempts" → wait 70–90 s with NO login requests (probes refill it).
- Account mutation limiter self-clears in 60–90 s; burst of seed failures across specs is often this.
- Mail: `skipWithoutMailbox()` first; `waitForMailboxURL`/`waitForMail` return the FIRST match → `clearMailbox()` right before the triggering request.
- Client-only checks (chips/pickers/warnings): don't submit.

MOBILE
- Long-press drawers: don't simulate; set end state: `page.evaluate(()=>{document.body.classList.add("ms-active","actions-open"); …})` (copy classes from the template's `enterMultiSelect()`).
- Hidden checkboxes: `el.checked=true; el.dispatchEvent(new Event("change",{bubbles:true}))`.
- Desktop-only inline controls: branch on `.isVisible()` to the mobile quick-edit popup.
- Obstructed button in long table: `scrollIntoViewIfNeeded()` + `click({force:true})`.
- Section nav items TOGGLE; sections with data start open → open only if not visible (`openSection`).
- Date picker reopens on the previously selected month → step either direction (`showMonthOf`).
- `data-confirm` → `page.once("dialog",d=>d.accept())` before click.
