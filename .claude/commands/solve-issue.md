---
description: Implement a dansal GitHub issue end-to-end (read, implement, test, commit with Closes). Build/deploy only if asked.
argument-hint: <issue-number>
---

Issue `$ARGUMENTS` (missing → ask). Follow the `ship-feature` skill from "IMPLEMENT"; the user's request to solve it is the go-ahead.

1. `gh issue view $ARGUMENTS --json title,body,comments,labels`. Check dependencies ("depends on #M") and whether it's already partly done (`git log --grep '#$ARGUMENTS'`).
2. Read the code the issue names; grep real symbol names. Entry points:
   | topic | where |
   |---|---|
   | API/DB | `cmd/dansal/*.go`; schema `cmd/dansal/main.go` → db-migration skill |
   | import/dedup | event-import skill |
   | admin/public UI, maps, forms | `cmd/dansal_web/templates/`, `admin_*.go`, `frontend.go`, `static/base.js` → admin-ui skill |
   | feeds out (iCal/RSS/JSON) | `cmd/dansal_web/feed.go` |
   | ActivityPub | `cmd/dansal_web/{ap,actor,httpsig*}.go` (CLAUDE.md rules) |
   | runtime settings | `site_settings` + `siteSettingsCache` (`sitecache.go`), webmin `siteconfig.go` |
   | strings | add-i18n skill |
3. Project rules not in skills: new admin POST route → entry in `routeEndpoint` (`cmd/dansal_web/user_rate_limit.go`); save-and-stay = redirect `…/edit?saved=1`; email/Telegram/Matrix in goroutines; `has_*` → tags.
4. Tests + `go build ./... && go vet ./... && go test ./...` + `gofmt -l`; UI changes → e2e-testing skill (desktop+mobile).
5. Commit (files by name) with `Closes #$ARGUMENTS` and the session's attribution line. Push/build/deploy only when asked.
