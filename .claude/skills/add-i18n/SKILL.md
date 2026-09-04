---
name: add-i18n
description: Add or change a user-visible string (button label, heading, error message, admin form field) in the dansal web UI. Use when touching i18n.yaml, adding new translation keys, or referencing strings from templates or Go handlers. Encodes the 12-language document order, the anchor-key insertion script, and YAML validation.
---

# Add a translation to dansal-web

All UI strings for `cmd/dansal_web` live in `cmd/dansal_web/i18n.yaml`, embedded into the binary via `//go:embed` (`cmd/dansal_web/i18n.go:15`). The file is the single source of truth for **12 languages**; there is no per-language fallback chain — a missing key renders as the bare key name.

## The 12 languages, in document order

Sections appear in this exact order in the YAML (do not reorder, do not append a section elsewhere):

```
de, br, en, es, fr, it, nl, uk, ca, pt, pl, cs
```

`CLAUDE.md`/`AGENTS.md` say "7 languages" — that is stale. The real count is **12**, and the order is the file's line order, not alphabetical (`br` comes right after `de`, `cs` last). When in doubt, trust the file.

## File shape

```yaml
languages:
  de:
    flag: "🇩🇪"
    name: "Deutsch"
    strings:
      nav_events: "Veranstaltungen"
      ...
  br:
    ...
```

- Top-level `default: de` is the fallback language.
- Each language has `flag`, `name`, and a `strings:` map.
- Values are plain strings; `%s`-style placeholders are filled via `TF` (see below).

## How keys are used

- **Templates**: `{{$.Strings.T "key"}}` or `{{$.Strings.TF "key" "arg"}}` — every page template gets `$.Strings`.
- **Go handlers**: lookup via the `I18n` type in `cmd/dansal_web/i18n.go`; `I18nStrings.T(key)` returns the key itself when missing (so a missing key degrades to the key name, not a crash).
- Sites can override strings at runtime via an external file at `/etc/dansal/i18n.yaml` (`config.yaml: i18n_file`, `cmd/dansal_web/i18n.go:57`) — always update the embedded YAML too.

## Procedure

1. **Pick a unique key** following existing naming (`nav_`, `evt_`, `admin_`, `loc_`, `org_`, `musician_`, `btn_go_back`, …). **Grep for a reusable existing key first** — a generic word/phrase you need (e.g. "Name", "Delete", "Close") may already exist under a differently-scoped key name (`col_name` for a generic table-column "Name" header, `admin_delete` for a generic "Delete" action, `admin_magic_link_close` happens to hold the generic "Close" translation despite its feature-specific name). Search by the *English value*, not just the key name, since the existing key's prefix won't necessarily hint at your new use case. Only add a new key when nothing already carries the exact phrase you need.
2. **Insert the key into all 12 sections, in document order.** Hand-editing 12 places is error-prone; use an anchor-key approach. Pick an existing key known to exist in all 12 sections (e.g. `evt_description`), and run a small Python script that inserts your new key after that anchor in each section — anchored on indentation so it lands inside the right `strings:` map:

```python
import re, io

path = "cmd/dansal_web/i18n.yaml"
new = [("my_new_key", "translation")] * 12  # one per language, in document order
anchor = "evt_description"

src = open(path, encoding="utf-8").read()
lines = src.splitlines(keepends=True)
anchor_re = re.compile(r"^(\s*)%s: " % anchor)

count = 0
out = []
for i, ln in enumerate(lines):
    m = anchor_re.match(ln)
    if m:
        indent = m.group(1)
        key, val = new[count]
        count += 1
        out.append("%s%s: \"%s\"\n" % (indent, key, val))
    out.append(ln)
assert count == 12, "anchor not found in all 12 sections (%d/12)" % count
open(path, "w", encoding="utf-8").write("".join(out))
```

3. **Validate** the YAML is still parseable:

```bash
python3 -c "import yaml; yaml.safe_load(open('cmd/dansal_web/i18n.yaml', encoding='utf-8'))"
```

4. **Reference the key** in the template/Go code as shown above. Add/extend a test only when the change touches template logic (`hreflang_smoke_test.go` covers language-parameterized pages — keep it green).

## Non-translation changes to i18n.yaml

- **Language metadata** (`flag`, `name`): same file, edit once per language.
- **Adding a new language**: new top-level key under `languages` with `flag`, `name`, `strings`, inserted at the end. Note `default: de` is the fallback, not a language section.

## Final checks

```bash
go build ./...
go vet ./...
go test ./...
```

Then `make build` and `sudo make deploy INSTANCE=dev` (see the `deploy` skill).
