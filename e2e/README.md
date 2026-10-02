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
  auth-magic-link, board, suggest-wizard): `smtp.sendmail` / `smtp_sendmail`
  pointing at `bin/fake-sendmail`, which appends to the mbox named by
  `DANSAL_MAIL_FILE`. For the dev instance:
  ```ini
  # /etc/systemd/system/dansal@dev.service.d/e2e-mail.conf
  [Service]
  Environment=DANSAL_MAIL_FILE=/var/lib/dansal-e2e-mail/dansal-e2e-mail.mbox
  ReadWritePaths=/var/lib/dansal-e2e-mail
  ```
  and export the same `DANSAL_MAIL_FILE` when running the suite. When the mbox
  directory is missing these specs are skipped with a message
  (`helpers/mailguard.ts`) instead of timing out.
- **Admin CLI access** for user fixtures: `ADMIN_CLI` / `ADMIN_SOCKET`
  (see the skill).

## Running against dev

```bash
cd e2e
export ADMIN_CLI=/usr/lib/dansal/dev/dansal_admin
export ADMIN_SOCKET=/var/lib/dansal/dev/dansal.sock
export DANSAL_MAIL_FILE=/var/lib/dansal-e2e-mail/dansal-e2e-mail.mbox
npx playwright test tests/journeys/<file>.spec.ts --project=desktop --retries=0 --reporter=line --workers=1
```
