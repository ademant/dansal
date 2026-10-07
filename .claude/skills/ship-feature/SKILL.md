---
name: ship-feature
description: Run the dansal discuss → issue → implement → commit workflow for a new feature/bug/change the user brings up. Use when a topic needs discussion before code, when asked to "create an issue", "implement #N", or "implement phase-N".
---

# Feature workflow (extends CLAUDE.md "Workflow"; don't repeat it, apply it)

CONSENT GATES — each needs its own explicit yes
1. Discuss: read the real code first; give approach + tradeoffs + a recommendation; real design choices → AskUserQuestion. Skip only for typos/one-liners.
2. "ok"/"create issue" → `gh issue create` ONLY (format: ISSUE FORMAT below). Not implementation.
3. Implement only on "implement #N" / "implement phase-N" (= all issues with label `phase-N`, one commit, one `Closes #N` line each).
4. Push only when asked (or already told to push in this conversation). Push sends everything unpushed.
5. Build/deploy only when asked (deploy skill).
- A multi-part proposal: approval of one part ≠ approval of the others. Unclear → ask.
- Ambiguous reference ("the other recommendation") → pick the most recent pending proposal, state the interpretation, offer the alternative.

ISSUE FORMAT (readable by humans, written for the AI that implements it later: condensed, no prose padding)
- Before: `gh issue list --search "<keywords>" --state all` → no duplicate; note related issues.
- Title: `<area>: <concrete change>` (e.g. `Postcode search: create schema at startup, import for pre-#1459 countries`).
- Body ≤ ~40 lines, bullets/fragments, no intro/outro, no restating CLAUDE.md:
```
## Problem
- <observed fact + evidence: numbers, prod state, repro, file:symbol>
## Solution
1. <change> — `file.go` `func` (constraints, edge cases)
## Tests
- <what to assert>
## Notes
- related #N / depends on #M / out of scope / open question
```
- Name real symbols and paths (grepped), not descriptions of them. One sentence of why per design choice.
- Drop the Notes section if there's nothing to say. Long evidence → summarise to the numbers that matter.
- Create with `--body-file <scratchpad>/issue.md`; cross-link related issues (comment on the older one if needed).
- Reply to the user: link + 2–5 bullet summary, not the body.

IMPLEMENT
- Grep actual names; don't trust memory or CLAUDE.md names (migration fn is `migrateDB`).
- Logic needed in 3+ handlers → shared helper.
- Relevant skills: add-i18n (12 langs), db-migration (smoke test), admin-ui, event-import, e2e-testing.
- JSON-LD `"description"` from user markdown → `plainTextDesc` (meta.go; decodes entities before stripping md). Postal addresses in JSON-LD → also `<address>` in body (CSS reset `font-style:normal;display:block`).
- Template/form/API-contract change → verify on a scratch instance with Playwright desktop+mobile (e2e-testing skill); add/extend a spec.
- Done = `go build ./... && go vet ./... && go test ./...` + `gofmt -l <touched files>` clean (pre-existing offenders: `actor_test.go`, `event_decline_test.go`, `social_links_test.go` — ignore).

COMMIT
```bash
git add <files by name>   # never -A: tree has untracked WIP (gancio_move/, scripts/__pycache__/, .claude/skills/log-analysis/ — never commit the latter)
git commit -m "$(cat <<'EOF'
<type>: <summary>

<why>

Closes #N
Closes #M

<attribution line from the session's system instructions>
EOF
)"
```
- After push, spot-check non-first issues: `gh issue view M --json state -q .state` (flips a few seconds after push).
- Follow-up to a shipped feature: small → `Refines #N`; issue's Closes-commit not yet pushed → `Refs #N`.
- Dependencies: state "depends on #M" in the dependent issue; implement in order. New scope found → separate issue (after ok), referenced from the original.
- Pure ops/config changes (e.g. nginx template one-liners) may skip the issue like typos; still commit with a why.

AFTER USER DEPLOYS
Check journal (`journalctl -u dansal@dev -u dansal-web@dev --since …`), changed endpoints via curl, relevant e2e specs, and for UI the browser.
