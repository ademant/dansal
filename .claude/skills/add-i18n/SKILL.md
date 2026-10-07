---
name: add-i18n
description: Add/change a user-visible string in dansal web UI (cmd/dansal_web/i18n.yaml, $.Strings.T/TF in templates, I18nStrings in Go). Use for any new label/heading/error/admin field text or translation key. Gives section order, anchor-insert script, validation.
---

# i18n (cmd/dansal_web/i18n.yaml)

FACTS
- Embedded via `//go:embed i18n.yaml` (`i18n.go`). Optional runtime override: `web.yaml: i18n_file` (`Config.I18nFile`) — still edit the embedded file.
- 12 sections, file order (NOT alphabetical): `de br en es fr it nl uk ca pt pl cs`. Shape: `languages.<lc>.{flag,name,strings.<key>}`; top-level `default: de`.
- Missing key renders as the bare key name (no per-language fallback).
- Use: template `{{$.Strings.T "key"}}` / `{{$.Strings.TF "key" arg}}` (TF = fmt.Sprintf); Go `I18nStrings.T(key)`.

PROCEDURE
1. Reuse first: grep the ENGLISH VALUE (not key name) — generic words exist under odd keys (`col_name`="Name", `admin_delete`="Delete", `admin_magic_link_close`="Close").
2. Key prefix convention: `nav_ evt_ admin_ loc_ org_ musician_ btn_ col_`.
3. Insert into all 12 sections with anchor script (anchor must exist 12×; `evt_description` does):
```python
import re
path="cmd/dansal_web/i18n.yaml"; anchor="evt_description"
new=[("my_key","de…"),("my_key","br…"),("my_key","en…"),("my_key","es…"),("my_key","fr…"),("my_key","it…"),
     ("my_key","nl…"),("my_key","uk…"),("my_key","ca…"),("my_key","pt…"),("my_key","pl…"),("my_key","cs…")]  # file order
rx=re.compile(r"^(\s*)%s: "%anchor); out=[]; c=0
for ln in open(path,encoding="utf-8").read().splitlines(True):
    m=rx.match(ln)
    if m: k,v=new[c]; c+=1; out.append('%s%s: "%s"\n'%(m.group(1),k,v.replace('"','\\"')))
    out.append(ln)
assert c==12,c; open(path,"w",encoding="utf-8").write("".join(out))
```
4. Validate: `python3 -c "import yaml;yaml.safe_load(open('cmd/dansal_web/i18n.yaml',encoding='utf-8'))"`
5. `go build ./... && go vet ./... && go test ./...` (`hreflang_smoke_test.go` covers per-language pages).

RULES
- English-only by decision (e.g. #1422 bot-facing text): still add to all 12 with the English value + a YAML comment above the first explaining why.
- Placeholder filled in JS: render with `T` (keeps literal `%s`, e.g. into `data-msg-*`), then `.replace('%s',…)` client-side. Never concatenate translated fragments.
- New language: append a new `languages.<lc>` block (flag, name, strings) at the end; update CLAUDE.md's language list.
