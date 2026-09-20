---
name: ship-feature
description: Discuss a topic/idea for the dansal project, turn it into a GitHub issue once agreed, implement it, close the issue on commit, build, and deploy to dev. Use when the user brings up a new feature, bug, or change to discuss before any code gets written.
---

# Ship a dansal feature/fix

Argument: the topic or idea to discuss (e.g. "widen the description field and add a markdown cheatsheet").

This skill encodes the standing dansal workflow from `CLAUDE.md`. Follow it in order — do not skip the discuss step for anything non-trivial.

## 1. Discuss first

- Explore the relevant code before proposing anything (read the actual files involved — don't guess at current behavior).
- Lay out the approach and tradeoffs in plain terms. If there's a real design choice (e.g. layout numbers, i18n scope, which files to touch), ask via `AskUserQuestion` rather than picking silently.
- **Wait for explicit confirmation** ("seems valid", "go ahead", etc.) before creating the issue or touching code. Skip this step only for obvious typos or single-line fixes.
- A "yes" to a proposal authorises the **issue only**. Implementation starts when the user says so ("implement #N"); several issues are often opened first and implemented later, in any order. When a batch of issues is grouped into phases, they carry `phase-N` labels and "implement phase-N" means one commit closing every issue with that label.
- **When a proposal bundles multiple distinct actions** (e.g. "I'll open issues for these bugs, *and* apply this other batch of safe fixes directly"), a reply confirming one part is not consent for the rest — confirm each part gets its own explicit go-ahead, even within the same message. Concretely: after a review surfaces a mix of findings, the user saying "create issues for all of them" authorizes exactly that (the issue-creation half of a two-part plan), not a follow-on implementation step you also described — don't infer the second half from silence or from the conversation's general direction. If it's unclear which parts were actually approved, ask.

## 2. Create the issue

Once the approach is agreed:

```bash
gh issue create --title "short description" --body "problem, solution, impact"
```

The body should capture the *why* and the concrete plan from step 1, not just a one-line restatement — it's the durable record of what was agreed.

## 3. Implement

- Follow established project patterns (see `CLAUDE.md`: migration safety-net pattern, dedup tiers, `attachTileLayer` for maps, goroutines for email, etc.). Confirm actual function/field names by grepping rather than relying on memory — e.g. the migration runner is `migrateDB()`, not `runMigrations()`.
- If the same non-trivial logic (e.g. resolving a display name from a caller ID) would otherwise be duplicated across 3+ handlers, extract a shared helper instead of copy-pasting it again.
- **i18n**: `cmd/dansal_web/i18n.yaml` actually carries **12** language sections in document order — `de, br, en, es, fr, it, nl, uk, ca, pt, pl, cs` — not just the 7 named in `CLAUDE.md`. When adding new keys, insert into all 12. A small Python script anchored on a key known to exist in all 12 sections (e.g. one you just added, or `evt_description`) is more reliable than hand-editing 12 places — insert in document order, then validate with `python3 -c "import yaml; yaml.safe_load(open('cmd/dansal_web/i18n.yaml', encoding='utf-8'))"`.
- **Schema changes**: after adding an `ALTER TABLE`/safety-net block, smoke-test it before committing (see the `db-migration` skill; for a new table keep the smoke test) — write a throwaway `_test.go` in `cmd/dansal` that opens a fresh `:memory:` DB, swaps the package-level `db` var, calls `createTables()` then `migrateDB()` **twice** (to confirm idempotency), and queries the new columns. Delete the test file afterward; it's a one-off verification, not a permanent regression test, unless the change is risky enough to warrant keeping it.
- Add/update permanent tests where it's cheap to do so, especially for template or handler changes.
- Run before considering it done:
  ```bash
  go build ./...
  go vet ./...
  go test ./...
  ```

## 4. Verify UI changes for real

`go test` doesn't render templates against a live API. For anything touching a template, form or web/API contract, run the built binaries on a scratch instance and drive it with the Playwright specs on **both** projects (`desktop` and `mobile`) — see the `e2e-testing` skill's "scratch instance" section. Add or extend a spec for the new behaviour. Things only this catches: list-vs-single-endpoint fields missing from a page, a section that toggles shut when clicked, `application/json` sent where `merge-patch+json` is required.

## 5. Commit with `Closes #N`

```bash
git commit -m "$(cat <<'EOF'
<type>: <description>

Closes #NNN
Closes #MMM

<optional body explaining why>

Co-Authored-By: <current model, per the session's attribution instructions> <noreply@anthropic.com>
EOF
)"
```

(Don't hardcode a model name/version here — it goes stale. Use whatever attribution block the active session's own instructions specify.)

**One `Closes #N` line per issue.** `Closes #1, #2` on one line closes only #1 on GitHub, silently. Spot-check with `gh issue view <N> --json state -q .state` after pushing (the state flips a few seconds after the push, not instantly). Run `gofmt -l` on the files you touched too; a few pre-existing files are always listed (`actor_test.go`, `config.go`), ignore those. Stage only your own files by name — the working tree often carries someone else's untracked WIP.

Note: the issue only actually auto-closes once this commit is **pushed**. Don't push proactively — confirm with the user first, per the standing rule on actions visible to others. If the user has already told you to push in this conversation, you don't need to ask again for the same change. "Push it" pushes whatever is currently unpushed on the branch, not just the most recent commit — multiple shipped features commonly stack up before a push.

Small follow-up tweaks to a feature you just shipped (e.g. a one-line display fix) don't need a fresh discuss → issue cycle — implement, test, and commit referencing `Refines #NNN` (not a new `Closes`) instead of restarting the workflow.

## 6. Build and deploy — only when asked

`CLAUDE.md`: `make build` and `sudo make deploy` are **not** run automatically after a change. Implement, run `go build`/`go vet`/`gofmt -l`/`go test`, commit, and stop; several commits commonly stack up before the user asks for a build. Build/deploy only when that message asks for it:

```bash
make build   # builds all five binaries, as the regular user
sudo make deploy INSTANCE=dev
```

- Always rebuild **all** binaries together (`make build`), never a selective `go build` of just the changed package — see issue #147.
- When asked to deploy without an instance name, use **dev**. Only deploy to other instances (`test`, `prod`, etc.) when the user explicitly asks for it.
- `sudo make deploy` requires an interactive terminal for `sudo` auth. If running non-interactively and `sudo` fails ("a terminal is required to authenticate"), tell the user to run `sudo make deploy INSTANCE=dev` themselves — don't treat this as a blocker on the rest of the workflow.

## Summary checklist

1. Discuss → wait for confirmation
2. `gh issue create` (once the user says ok to that proposal)
3. Implement + test (when the user asks for it)
4. Verify UI on a scratch instance, desktop + mobile
5. Commit with one `Closes #N` line per issue
6. Only if asked: `make build`, then `sudo make deploy INSTANCE=dev` (ask before deploying elsewhere; ask before pushing unless already told to)
