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
npx playwright test tests/journeys/<file>.spec.ts --project=desktop --retries=0 --reporter=line --workers=1
```

## Smoke and full suites

Journeys tagged `@smoke` form the smoke suite: desktop only, run on every PR
and push to `main` (`make e2e-smoke` locally). The full suite (desktop and
mobile, plus the WCAG sweep) runs on release tags, nightly and on manual
dispatch, and gates the release packaging job.

Tag a journey as smoke with `test("title", { tag: "@smoke" }, async ...)`.
Keep the smoke set small and green; the full suite is where new coverage
goes first.
