---
description: Add a new i18n key to all 12 language sections of cmd/dansal_web/i18n.yaml.
argument-hint: <key> "<English text>"
---

Args `$ARGUMENTS` → key (snake_case), English text. Follow the `add-i18n` skill:
1. grep the English value first — reuse an existing key if one carries the phrase.
2. Translate into `de br en es fr it nl uk ca pt pl cs` (file order); flag uncertain translations to the user.
3. Insert with the skill's anchor script (asserts 12 hits); validate YAML; `grep -c '^ *<key>:' cmd/dansal_web/i18n.yaml` == 12.
4. `go build ./... && go test ./...`. No commit/build/deploy unless part of an agreed issue / asked.
