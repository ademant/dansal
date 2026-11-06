# dansal e2e suite (Playwright)

Journeys live in `tests/journeys/*.spec.ts`, the accessibility sweep in
`tests/accessibility/`. Every spec runs in two projects, `desktop` and
`mobile`. The `e2e-testing` Claude skill (`.claude/skills/e2e-testing/`) has
the full how-to and gotchas; this file lists what a **target instance** must
provide.

## Target instance requirements

- **Rate limits** in the API's `config.yaml` (`server:` section). Seeding alone
  exceeds the defaults:
  ```yaml
  rate_limit: 1000                    # per-IP, default 100/min
  account_mutation_rate_limit: 600    # per-account creates/updates, default 30/min
  ```
  A seed call rejected by either shows up as
  `POST /api/v1/… → 429: Rate limit exceeded…` (`helpers/seed.ts`).
- **Suggest limit** in the same `config.yaml` (`server:`). Anonymous event
  suggestions are limited per client IP (default 3 per 10 minutes), and the
  suggest specs submit more than that in one run (#1804):
  ```yaml
  suggest_rate_limit: 100
  suggest_preview_rate_limit: 100
  register_rate_limit: 100
  ```
  `register_rate_limit` (default 3 per 10 minutes) covers self-registration, so
  the register specs need it too (#1828).
  A submit rejected by it fails with `suggest submit rate-limited`. The
  preview limit (default 5 per 10 minutes) is separate: the suggest specs
  preview several times per run too (#1819).
- **Mail capture** for the mail-dependent specs (auth-invite,
  auth-magic-link, board, checkin, suggest-wizard). The instance's outgoing
  mail goes to a fake sendmail that appends each message to the mbox named by
  `DANSAL_MAIL_FILE`; the specs read that file. Dev setup (done once, as root):
  1. Copy the script to a system path. The unit sets `ProtectHome=true`, so
     the service cannot run anything under `/home`:
     `install -m 0755 e2e/bin/fake-sendmail /usr/lib/dansal/e2e-fake-sendmail`
  2. `/etc/dansal/dev/config.yaml`: `smtp.sendmail: /usr/lib/dansal/e2e-fake-sendmail`
     (`smtp_sendmail` in `web.yaml` only if the web binary sends mail).
     While this is set, dev sends no real email.
  3. Drop-in for the API unit:
     ```ini
     # /etc/systemd/system/dansal@dev.service.d/e2e-mail.conf
     [Service]
     Environment=DANSAL_MAIL_FILE=/var/lib/dansal-e2e-mail/dansal-e2e-mail.mbox
     ReadWritePaths=/var/lib/dansal-e2e-mail
     UMask=0022
     ```
     `UMask=0022` matters: with the default umask the service creates the mbox
     as `0640`, which the test user cannot read, and every mail wait then times
     out.
  4. `install -d -o dansal -g dansal -m 0755 /var/lib/dansal-e2e-mail`, then
     `systemctl daemon-reload && systemctl restart dansal@dev`.
  5. Export the same `DANSAL_MAIL_FILE` when running the suite.

  Known limit: `clearMailbox()` unlinks the mbox, which the test user cannot
  do when the service owns the directory, so the call fails silently and the
  file keeps earlier messages. `waitForMail` therefore takes the newest match
  (the last one in the file), not the first. When the mbox directory is
  missing, these specs are skipped with a message (`helpers/mailguard.ts`)
  instead of timing out.
- **Admin CLI access** for user fixtures: `ADMIN_CLI` / `ADMIN_SOCKET`
  (see the skill).
- **Webmin** (optional) for `security-regressions.spec.ts`'s #1536 coverage:
  `WEBMIN_URL` pointing at a running `dansal-webmin` instance, e.g.
  `http://localhost:8090`. When unset, that one test is skipped rather than
  failing — most target instances (including `dev` as of writing) don't run
  webmin alongside the suite.
- **A writable web.db** (optional) for specs that seed a `site_settings` row
  directly since dansal_web itself has no endpoint that writes one (only the
  separate webmin binary does): `WEB_DB_PATH` pointing at the file, e.g.
  `/var/lib/dansal-web/web.db`. Used by `activitypub.spec.ts` (IndexNow key)
  and `about-and-info-pages.spec.ts` (impressum/privacy/terms/security.txt —
  all 404 until configured). When unset, those specs fall back to asserting
  whatever the unconfigured behavior already is, rather than failing.

## Running against dev

```bash
cd e2e
export ADMIN_CLI=/usr/lib/dansal/dev/dansal_admin
export ADMIN_SOCKET=/var/lib/dansal/dev/dansal.sock
export DANSAL_MAIL_FILE=/var/lib/dansal-e2e-mail/dansal-e2e-mail.mbox
npx playwright test tests/journeys/<file>.spec.ts --project=desktop --reporter=line --workers=1
```

Retries come from `playwright.config.ts` (`retries: 1`): a test that fails once
and passes on the second attempt is reported as flaky, not failed. Do not pass
`--retries=0` for a normal run, or flaky tests look like real failures.

## Cleanup of test data (#1793, #1829)

After each run, `global-teardown.ts` deletes what the run created: registrations,
events (bookings included: they are test data), organisations, locations,
musicians, instructors and the users that registration made. Seed data shared
between runs (the seed org, the shared seed accounts) is never deleted. Each
created entity is written to `e2e/.run/cleanup/run-<id>.jsonl` as it is created.

Each run ends with a summary line per phase: removed, failed, kept and skipped
ledgers, with the reason for each failure. A registration that was already
approved cannot be removed through the API (409); it is reported as kept, and it
expires on its own after a week. Entities that fail stay in the ledger.

`global-setup.ts` clears ledgers from earlier runs at the start of the next run.
A ledger whose owning process is gone is cleaned at once; one whose owner is
still running is left alone. A ledger with no readable owner is cleaned once it
is an hour old.

Cleanup only runs on hosts in `CLEANUP_ALLOWED_HOSTS` (`helpers/cleanup.ts`).
Anything else is refused and left in the ledger. Organisations, locations,
users and the rest are removed with `dansal_admin`, which needs
`server.allow_admin_delete: true` on the target instance.

Not yet recorded: events created by feed imports and suggestion submissions
(the server creates them, so the test does not see their ids), and organisations
or users created by specs that do not go through the helpers above.

## Smoke and full suites

Journeys tagged `@smoke` form the smoke suite: desktop only, run on every PR
and push to `main` (`make e2e-smoke` locally). The full suite (desktop and
mobile, plus the WCAG sweep) runs on release tags, nightly and on manual
dispatch, and gates the release packaging job.

Tag a journey as smoke with `test("title", { tag: "@smoke" }, async ...)`.
Keep the smoke set small and green; the full suite is where new coverage
goes first.
