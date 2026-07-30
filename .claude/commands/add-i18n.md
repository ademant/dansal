---
description: Add a new i18n key to all 12 language sections of cmd/dansal_web/i18n.yaml.
argument-hint: <key> "<English text>"
---

# Add an i18n key to dansal

Arguments: `$ARGUMENTS` — e.g. `my_new_key "My new label"`

Parse: **key** (snake_case) and **English translation**.

---

## Rules before you start

- Every key must appear in **all 12** language sections. A missing key causes a template render panic.
- Each Edit call must match **exactly one** occurrence. Always use a nearby existing key as a surrounding-context anchor. Never match on the key alone if the pattern could repeat.
- The file is ~14 000 lines. Use `grep -n` to find anchors rather than reading large blocks.

---

## Step 1 — Choose an anchor key

Pick a key that is semantically nearby and will stay unique within each section. Run:

```bash
grep -n "anchor_key:" cmd/dansal_web/i18n.yaml
```

You'll get 12 line numbers (one per language). These are your insertion points.

## Step 2 — Find the exact language section boundaries

```bash
grep -n "^  [a-z][a-z]:" cmd/dansal_web/i18n.yaml
```

Current sections (line numbers shift as the file grows — always re-run):

| Lang | Meaning |
|---|---|
| `de` | German |
| `br` | Breton |
| `en` | English |
| `es` | Spanish |
| `fr` | French |
| `it` | Italian |
| `nl` | Dutch |
| `uk` | Ukrainian |
| `ca` | Catalan |
| `pt` | Portuguese |
| `pl` | Polish |
| `cs` | Czech |

## Step 3 — Translate the key

Provide translations for all 12 languages. For languages you are not certain of, use a clear, natural equivalent of the English. Mark uncertain translations with a comment so the user can review:

| Lang | Translation |
|---|---|
| `de` | … |
| `br` | … |
| `en` | (from argument) |
| `es` | … |
| `fr` | … |
| `it` | … |
| `nl` | … |
| `uk` | … |
| `ca` | … |
| `pt` | … |
| `pl` | … |
| `cs` | … |

## Step 4 — Insert into each section

For each of the 12 languages, make one Edit call using the anchor key as context. Pattern:

```
old_string:
      anchor_key: "existing translation"
new_string:
      anchor_key: "existing translation"
      my_new_key: "Translation for this language"
```

Work through the languages in file order: `de`, `br`, `en`, `es`, `fr`, `it`, `nl`, `uk`, `ca`, `pt`, `pl`, `cs`.

If the anchor string appears more than once in the file (e.g. two languages happen to share the exact same translation), use `replace_all: false` and include more surrounding lines to make the match unique.

## Step 5 — Verify all 12 are present

```bash
grep -c "my_new_key:" cmd/dansal_web/i18n.yaml
```

Must print `12`. If fewer, find which section is missing:

```bash
grep -n "my_new_key:" cmd/dansal_web/i18n.yaml
```

Compare against the section boundaries from Step 2.

## Step 6 — Build to catch template errors

```bash
make build
```

A missing or misnamed key causes a compile-time or startup panic in `dansal_web`. Fix before deploying.

## Step 7 — Commit

```bash
git add cmd/dansal_web/i18n.yaml
git commit -m "$(cat <<'EOF'
i18n: add <key> to all 12 language sections

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Common mistakes

| Mistake | Fix |
|---|---|
| Inserting in only 7 languages | Always do all 12; check with `grep -c` |
| Anchor matches multiple lines | Add more surrounding context to `old_string` |
| Key name has a typo vs. template | `grep -r "my_new_key" cmd/dansal_web/templates/` to confirm usage |
| Forgetting to build after edit | Template panics at startup, not compile time |
