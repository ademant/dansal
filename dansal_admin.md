# dansal_admin

Command-line administration tool for the dansal calendar server.

`dansal_admin` connects to the running `dansal` process via a Unix domain socket and performs privileged operations that are not exposed through the web API: user and session management, SMTP/Telegram/Matrix configuration, backups, and data migration.

---

## Synopsis

```
dansal_admin [--socket PATH] <command> [flags]
dansal_admin help [<command>]
```

---

## Requirements

- The `dansal` service must be running.
- The caller must have read/write access to the admin socket (default `mode 600`, owned by the user running `dansal`).
- Commands that write to `config.yaml` require the dansal process to have write permission on that file.

---

## Global flag

| Flag | Default | Description |
|---|---|---|
| `--socket PATH` | read from `/etc/dansal/config.yaml`, fall back to `./dansal.sock` | Path to the dansal Unix admin socket |

---

## User management

### list-users

```
dansal_admin list-users
```

List all user accounts with ID, username, email, role, disabled status, and creation date.

---

### create-user

```
dansal_admin create-user --username STR --email STR --password STR \
  [--role STR] [--telegram STR] [--matrix STR]
```

Create a new user account.

| Flag | Required | Description |
|---|---|---|
| `--username` | yes | Login name |
| `--email` | yes | Email address |
| `--password` | yes | Initial password |
| `--role` | no | `admin`, `user`, `publisher`, or `viewer` (default: `user`) |
| `--telegram` | no | Telegram handle |
| `--matrix` | no | Matrix ID |

**Roles:**

| Role | Can do |
|---|---|
| `admin` | Manage all business data (events, locations, organisations), manage users |
| `user` | Manage events for their own organisation |
| `publisher` | Publish and approve events |
| `viewer` | Read-only access |

---

### delete-user

```
dansal_admin delete-user --username STR
```

Permanently delete a user account. Admin accounts cannot be deleted.

---

### set-password

```
dansal_admin set-password --username STR --password STR
```

Change the password for any account.

---

### set-role

```
dansal_admin set-role --username STR --role STR
```

Change a user's role. Valid values: `admin`, `user`, `publisher`, `viewer`.

---

### enable-user / disable-user

```
dansal_admin enable-user  --username STR
dansal_admin disable-user --username STR
```

Enable or disable an account. Disabling revokes all active sessions immediately. Admin accounts cannot be disabled. Enabling also resets the failed-login counter.

---

## Invite links

### list-invites

```
dansal_admin list-invites [--username STR]
```

List all invite links. Without `--username` all links are shown. Columns: ID, role, org ID, expiry, used timestamp, token.

---

### revoke-invite

```
dansal_admin revoke-invite --token STR
```

Invalidate an unused invite link.

---

## Session management

### list-sessions

```
dansal_admin list-sessions --username STR
```

List all sessions for a user (active and expired). Columns: ID, IP, fingerprint, last seen, expiry, user-agent.

---

### revoke-session

```
dansal_admin revoke-session --id INT
```

Immediately invalidate a session by its numeric ID. Use `list-sessions` to find the ID.

---

## Organisation management

### list-orgs

```
dansal_admin list-orgs
```

List all organisations with ID, name, description, and creation date.

---

### list-members

```
dansal_admin list-members --org-id INT
```

List all members of an organisation.

---

### add-member / remove-member

```
dansal_admin add-member   --org-id INT --username STR
dansal_admin remove-member --org-id INT --username STR
```

Add or remove a user from an organisation. `add-member` is idempotent.

---

## SMTP

Changes are written to `config.yaml` and take effect immediately without a service restart.

### smtp-show

```
dansal_admin smtp-show
```

Display the current SMTP configuration. The password is never shown.

---

### smtp-set

```
dansal_admin smtp-set [--host H] [--port P] [--username U] \
  [--from F] [--from-name N] [--tls M] [--timeout S]
```

Update SMTP settings. Only provided flags are changed.

| Flag | Description |
|---|---|
| `--host` | SMTP server hostname |
| `--port` | Port (default at send time: 587) |
| `--username` | SMTP account username |
| `--from` | Envelope From address (defaults to username if empty) |
| `--from-name` | Display name in the From header |
| `--tls` | TLS mode: `starttls` (default), `tls` (port 465), `none` |
| `--timeout` | Dial and send timeout in seconds (default: 30) |

After saving, the command also updates `smtp_host` in `/etc/dansal/web.yaml` so the web service stays in sync.

---

### smtp-set-password

```
dansal_admin smtp-set-password [--password P]
```

Set the SMTP account password. It is encrypted with AES-256-GCM and stored obscured in `config.yaml` (key also in `config.yaml` — protect the file with mode 600). If `--password` is omitted the password is prompted with no terminal echo.

---

### smtp-test

```
dansal_admin smtp-test --to EMAIL
```

Send a test email using the current configuration and print the result.

---

## Telegram

### telegram-show

```
dansal_admin telegram-show
```

Show the configured Telegram bot token and bot name.

---

### telegram-set

```
dansal_admin telegram-set [--token T] [--name N]
```

Set the Telegram bot token and/or bot name. Only provided flags are changed. Changes are written to `config.yaml` and take effect immediately.

---

## Matrix

### matrix-show

```
dansal_admin matrix-show
```

Show the configured Matrix homeserver URL and whether an access token is set (the token itself is not displayed).

---

### matrix-set

```
dansal_admin matrix-set [--homeserver URL] [--token T]
```

Set the Matrix homeserver URL and/or access token directly. Use `matrix-login` instead when you have a username and password and want the token fetched automatically.

---

### matrix-login

```
dansal_admin matrix-login --homeserver URL --username U [--password P]
```

Obtain a Matrix access token via the Matrix Client-Server API (`m.login.password`) and store it in `config.yaml`. If `--password` is omitted it is prompted with no terminal echo.

| Flag | Required | Description |
|---|---|---|
| `--homeserver` | yes | Matrix homeserver URL, e.g. `https://matrix.example.org` |
| `--username` | yes | Matrix username (local part only) |
| `--password` | no | Password (prompted if omitted) |

---

## Heartbeat

The heartbeat runs continuously in the dansal process and probes each notification channel (email, Telegram, Matrix) on a configurable interval to verify they are reachable.

### heartbeat-show

```
dansal_admin heartbeat-show
```

Show the current status of all notification channels: configured/not configured, ok/fail, and any error message from the last probe.

---

### heartbeat-set

```
dansal_admin heartbeat-set --interval N
```

Set the probe interval in minutes. The new value is written to `config.yaml` and picked up by the running heartbeat loop on its next iteration.

---

## Backup and restore

Backups are `.tar.gz` archives containing three components:

| Component | Notes |
|---|---|
| `config.yaml` | Full server configuration including SMTP credentials |
| `calendar.db` | Consistent SQLite snapshot taken via `VACUUM INTO` |
| `images/` | All uploaded images |

Restoring config and the database does not require a service restart — config is reloaded live and the database is replaced via the SQLite online backup API.

### backup

```
dansal_admin backup [--mode full|incremental|cumulative] [--output DIR]
                    [--passphrase-file F | --passphrase-prompt]
```

Create a backup archive (#1765). Every archive holds `calendar.db` and `web.db` (consistent snapshots), the instance config, nginx and systemd files, and the images.

| mode | images stored | base |
|---|---|---|
| `full` | all | none; starts a new chain |
| `incremental` | changed since the previous archive of any kind | previous archive |
| `cumulative` | changed since the last full | last full |

Without `--mode`, the mode comes from `server.backup_mode` (default `full`). The nightly timer runs `backup` with no mode, so it follows the config. Incremental and cumulative need an earlier full in the same directory, and fail with a message naming the missing full otherwise.

Encryption: `server.backup_encryption_public_key` encrypts every archive to the configured recipients (`age1…`, `ssh-ed25519`, `ssh-rsa` ≥ 2048 bits). `--passphrase-file` or `--passphrase-prompt` encrypts one archive with a passphrase (age passphrase mode). With a public key configured, a passphrase needs `--passphrase-file`.

```
dansal_admin backup --mode cumulative
dansal_admin backup --mode full --passphrase-prompt
```

Output: `dansal-<mode>-<timestamp>.tar.zst`, with `.age` (age encrypted) or `.enc` (symmetric key file) appended. A manifest copy (`<archive>.manifest.json`) sits next to each archive; it is unencrypted and needed to resolve chains.

---

### restore

```
dansal_admin restore --input PATH|DIR [--identity F] [--passphrase-file F] [--to DIR] [--wipe-credentials]
```

A directory restores its newest archive together with the chain it depends on. A file restores that archive and the archives it depends on. Chains are applied oldest first, so the images end up exactly as they were at the last backup; images the final backup no longer has are removed.

- `calendar.db` is restored live via the SQLite online backup API.
- `web.db`, config, nginx and systemd files go to `--to DIR` only. Without `--to` they are listed as not restored, and nothing is written to `/etc`.
- `.age` archives need `--identity` (an age identity file or an SSH private key) or `--passphrase-file`.
- Legacy `.tar.gz` archives restore as before, with overlaid images.

Prints a summary: `restored: archives=N db=true images=N removed=N`.

---

### password-backup

```
dansal_admin password-backup [--output PATH] [--password P]
```

Create a full backup and encrypt it with AES-256-GCM. Key derivation uses scrypt (N=65536, r=8, p=1). If `--password` is omitted it is prompted twice for confirmation.

`--output` defaults to `./dansal-encrypted-<timestamp>.tar.gz.enc`.

> Passing `--password` on the command line exposes it in the process list. Use the interactive prompt in production.

---

### password-restore

```
dansal_admin password-restore --input PATH [--password P]
```

Decrypt a `.tar.gz.enc` file created by `password-backup` and restore it. If `--password` is omitted it is prompted with no echo.

---

## Data export and import

These commands work directly on the SQLite database file and do **not** require the dansal service to be running (pass `--db` with the path if needed).

### export

```
dansal_admin export --table TABLE [--output FILE] [--db PATH]
```

Export a table to JSON. Output goes to stdout by default.

| Flag | Default | Description |
|---|---|---|
| `--table` | — | `fetchurl`, `locations`, `organisations`, or `events` (required) |
| `--output` | stdout | Destination file |
| `--db` | `/var/lib/dansal/calendar.db` | Path to `calendar.db` |

---

### import

```
dansal_admin import --table TABLE [--input FILE] [--db PATH] [--apply]
```

Import records from JSON. Runs as a dry-run by default; add `--apply` to write.

Deduplication keys per table:

| Table | Dedup key |
|---|---|
| `fetchurl` | URL |
| `locations` | Location name |
| `organisations` | Organisation name |
| `events` | UID, then URL |

Existing records that match the dedup key are updated; new records are inserted.

| Flag | Default | Description |
|---|---|---|
| `--table` | — | Same values as export (required) |
| `--input` | stdin | Source JSON file |
| `--db` | `/var/lib/dansal/calendar.db` | Path to `calendar.db` |
| `--apply` | false | Write changes (omit for dry-run) |

---

## Maintenance

### vacuum

```
dansal_admin vacuum
```

Run `VACUUM` on the SQLite database to reclaim space freed by deleted rows. May take a moment on large databases.

---

### fill-location-fields

```
dansal_admin fill-location-fields [--db PATH] [--apply]
```

Parse `address`, `zipcode`, and `town` from location names for rows where those columns are empty. Recognises German address patterns embedded in the name, e.g.:

```
KFZ, Biegenstr. 13, 35037 Marburg  →  address=Biegenstr. 13  zipcode=35037  town=Marburg
Tanzhaus, 69115 Heidelberg          →  zipcode=69115  town=Heidelberg
```

Without `--apply` the command prints what would change (dry-run).

---

## Examples

**Initial setup after install:**

```bash
# Create the first admin account
dansal_admin create-user --username alice --email alice@example.org \
  --password secret --role admin

# Configure SMTP
dansal_admin smtp-set --host mail.example.org --port 587 \
  --username noreply@example.org --from noreply@example.org \
  --from-name "Calendar" --tls starttls
dansal_admin smtp-set-password
dansal_admin smtp-test --to alice@example.org

# Configure Matrix
dansal_admin matrix-login --homeserver https://matrix.example.org \
  --username calendarbot

# Set heartbeat interval
dansal_admin heartbeat-set --interval 10
dansal_admin heartbeat-show
```

**Respond to a locked-out user:**

```bash
dansal_admin enable-user --username bob
dansal_admin set-password --username bob --password newpass
```

**Revoke a compromised session:**

```bash
dansal_admin list-sessions --username alice
dansal_admin revoke-session --id 42
```

**Daily backup via cron:**

```bash
0 3 * * * /usr/bin/dansal_admin backup --output /var/backups/dansal/dansal-$(date +\%F).tar.gz
```

**Migrate data from another instance:**

```bash
dansal_admin export --table locations --output locations.json
# ... copy file to new server ...
dansal_admin import --table locations --input locations.json --apply
```

---

## Files

| Path | Purpose |
|---|---|
| `/etc/dansal/config.yaml` | Server configuration; SMTP/Telegram/Matrix credentials stored here |
| `/etc/dansal/web.yaml` | Web frontend configuration; `smtp_host` kept in sync by `smtp-set` |
| `/var/lib/dansal/calendar.db` | SQLite database |
| `/var/lib/dansal/images/` | Uploaded images |
| socket (from config) | Unix domain socket used by `dansal_admin` to talk to the running service |

`config.yaml` contains credentials in plaintext (SMTP password is AES-256-GCM encrypted but the key is also in the file). Protect it:

```bash
chmod 600 /etc/dansal/config.yaml
```
