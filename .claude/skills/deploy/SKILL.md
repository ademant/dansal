---
name: deploy
description: Build and deploy the dansal stack — all binaries, systemd units, nginx, and rollback. Use when the user asks to build, deploy, rollback, restart, or check the deployed version of the dansal calendar (dev/prod/test/nl instances), or after any code change to get it live. Encodes the make build + sudo make deploy flow, INSTANCE selection, the sudo non-interactive gotcha, and versioned rollback.
---

# Build & deploy dansal

The repo builds **five binaries** from one monorepo: `dansal` (REST API), `dansal_web` (frontend + ActivityPub), `dansal_admin` (admin CLI), `dansal_webmin` (admin web UI), `dansal_doc` (docs site).

**Always build and deploy all binaries together** — a selective deploy risks stale binaries (issue #147). Never copy `go build ./cmd/...` output manually.

## Prerequisites

- Go **1.26+** (go.mod pins `go 1.26.4`) — verify with `go version` first.
- Deploy needs **root**; builds run as the regular user.

## The flow

```bash
go version                       # confirm 1.26+
make build                       # build all five binaries, as the regular user
sudo make deploy INSTANCE=dev    # install + restart, default instance is dev
```

- **Default deploy target is `dev`.** Only deploy to other instances (`test`, `prod`, `nl`, …) when the user explicitly asks.
- `VERSION` is derived from `git describe --tags --always --dirty` (Makefile:1).
- `make deploy` requires `INSTANCE` (`$(error INSTANCE is required: sudo make deploy INSTANCE=dev)`).

## What `make deploy` does

1. Installs systemd unit files (`install-units` target).
2. Installs binaries as `/usr/lib/dansal/<instance>/bin/<name>.<version>`, symlinked from the plain name, snapshotting the DB and config YAMLs under the same version tag before restarting.
3. Keeps the **last 5 versions** per instance (see `scripts/deploy-instance`).
4. Restarts the instance's systemd services.

## The sudo / non-interactive gotcha

`sudo make deploy` needs an **interactive terminal** for sudo auth. If it fails with "a terminal is required to authenticate" (e.g. running through a non-interactive tool), do **not** treat it as a blocker — tell the user to run `sudo make deploy INSTANCE=dev` themselves, and continue with anything else that can proceed in the meantime.

## Instance topology

- **DB**: SQLite at `/var/lib/dansal/<instance>/calendar.db`
- **Config**: `/etc/dansal/<instance>/{config.yaml, web.yaml, webmin.yaml}`
- **Services** (systemd template units, one per instance): `dansal@<name>`, `dansal-web@<name>`, `dansal-webmin@<name>`, plus `dansal-doc@<name>` and timers: `dansal-fetch@`, `dansal-backup@`, `dansal-mailcheck@`, `dansal-prune-images@`, `dansal-vacuum@`.

## Rollback & inspection

```bash
sudo make rollback INSTANCE=dev                # repoint binaries/DB/config at the previous version
sudo make rollback INSTANCE=dev VERSION=<v>    # ...or a specific version
sudo make list INSTANCE=dev                    # show versions available to roll back to
```

`rollback`/`list` also require root and `INSTANCE`.

## First-time setup

```bash
sudo scripts/install-instance   # interactive: creates instance, configs, systemd units
```

## Config validation

```bash
make check-config               # validates packaging configs against live /etc configs
```

## Deploying to prod

Only when the user explicitly asks. Prod-specific considerations:
- Config lives at `/etc/dansal/prod/*.yaml` — the `deploy` target only installs binaries and units; config is managed separately.
- Consider the timing (downtime during service restart) and check the DB migration smoke test in the `db-migration` skill before deploying a schema change.

## Final checks

```bash
make build
sudo make deploy INSTANCE=dev
```

Use `sudo systemctl status dansal@dev dansal-web@dev dansal-webmin@dev` to confirm the services came up cleanly after a deploy.
