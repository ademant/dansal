---
name: deploy
description: Build/deploy/rollback/inspect dansal instances (dev/test/prod/nl): make build, sudo make deploy INSTANCE=, rollback, list, deploy-nginx, verifying a deploy, checking which version runs. Use only when the user asks to build/deploy/rollback/restart or asks what version is deployed.
---

# Build & deploy

POLICY (CLAUDE.md, restated because it's the #1 mistake)
- Build/deploy ONLY when the current message asks. Default instance `dev`; other instances only when named.
- Always all binaries: `make build` then `sudo make deploy INSTANCE=<i>`. Never hand-install `go build` output (#147).

COMMANDS
```bash
go version                         # >= go.mod's `go` line (currently 1.27.x); newer is fine (toolchain switching)
make build                         # as user; builds cmd/{dansal,dansal_web,dansal_admin,dansal_webmin,dansal_doc}
sudo -n make deploy INSTANCE=dev   # root; installs units, wiki, binaries, restarts
sudo -n make list INSTANCE=dev
sudo -n make rollback INSTANCE=dev [VERSION=<v>]   # VERSION → ROLLBACK_VERSION; omitted = previous
sudo -n make deploy-nginx INSTANCE=<i>             # vhost + 00-dansal-log-formats.conf; deploy-full = deploy + deploy-nginx
make check-config                  # packaging configs vs live /etc
```
- `sudo -n` works non-interactively on this host. If sudo prompts ("a terminal is required"), hand the command to the user (`! sudo make deploy INSTANCE=dev`) and continue other work.
- Auto-mode may block prod changes (deploy, nginx reload, /etc edits) as "Production Deploy": don't retry or work around; give the user the exact command.
- `go` missing: user-local tarball from go.dev/dl (sha256 from `?mode=json&include=all`) into `~/goX/`, add to PATH. No root needed.

LAYOUT
- Binaries: `/usr/lib/dansal/<i>/bin/<name>.<git-describe>` symlinked from `/usr/lib/dansal/<i>/<name>`; last 5 versions kept (`scripts/deploy-instance`); DB+config snapshotted per version (`calendar.db.<version>`).
- Running version: `readlink /usr/lib/dansal/<i>/dansal-web` → `…-g<sha>`; is commit X live: `git merge-base --is-ancestor X <sha> && echo yes`.
- API DB `/var/lib/dansal/<i>/calendar.db`; web DB `/var/lib/dansal-web/<i>/web.db`; configs `/etc/dansal/<i>/{config,web,webmin}.yaml` (not touched by deploy).
- Units: `dansal@ dansal-web@ dansal-webmin@ dansal-doc@`; timers `dansal-fetch@ dansal-backup@ dansal-mailcheck@ dansal-prune-images@ dansal-vacuum@`.
- `VERSION` = `git describe --tags --always --dirty` (uncommitted changes → `-dirty`).

VERIFY AFTER DEPLOY
- `sudo -n journalctl -u dansal@<i> -u dansal-web@<i> --since "10 min ago" --no-pager` — look for `migrate…:`, `_chk already exists`, `no such column`, `no such table`.
- `systemctl status dansal@<i> dansal-web@<i> dansal-webmin@<i> --no-pager`.
- DB reads: `sudo -n sqlite3 -readonly <db> "…"`; experiments only on a copy in the scratchpad.
- `localhost:8080` and the public dev domain may be different instances/DBs — confirm which before concluding.
- Schema change going to prod: run the db-migration skill's smoke test first.
- First-time instance: `sudo scripts/install-instance` (interactive).
