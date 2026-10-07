---
name: admin-ui
description: Build/modify dansal web UI — admin_*.html forms, webmin pages, public templates, inline/base.js scripts, Leaflet maps, dialogs, drag-reorder, section-nav edit forms, save-error display, A/B comparison layouts. Use for any template/JS/CSS change in cmd/dansal_web or cmd/dansal_webmin.
---

# Web UI conventions (cmd/dansal_web/templates, static/base.js)

CSP — HARD RULES
- script-src has no 'unsafe-inline' → NO `onclick=`/`onchange=`/`onsubmit=` attributes (dead). Use the base.js dispatcher: `data-fn="name"` + `data-args='[json]'` (`"@this"` = element) + `data-on="change|submit|input"` (default click). New helpers go in `static/base.js` as `window` functions, not per-template inline scripts.
- `<script>`/`<style>` elements need `nonce="{{$.Nonce}}"`. Touching a `style="…"` attribute → move it to a class (see CLAUDE.md style-src section).
- Never 304/conditional GET for HTML rendered via base.html (nonce mismatch breaks all scripts, #1367; see `conditional.go`).

UNSAVED-CHANGES GUARD (every admin edit form)
```js
var _formDirty=false;
function _markDirty(){ if(_formDirty)return; _formDirty=true;
  window.addEventListener('beforeunload',function(e){ if(!_formDirty)return; e.preventDefault(); e.returnValue=''; }); }
function safeGoBack(){ if(_formDirty&&!confirm('{{$.Strings.T "admin_unsaved_confirm"}}'))return; _formDirty=false;
  if(window.history.length>1)history.back(); else location.href='{{if .Data.From}}{{.Data.From | js}}{{else}}/admin/<section>{{end}}'; }
var form=document.getElementById('<form-id>'); form.addEventListener('input',_markDirty); form.addEventListener('change',_markDirty);
```
- Back button: `class="btn-secondary" data-fn="safeGoBack" data-args="[]"` + i18n title/aria-label. Submit → `_formDirty=false` first. Save path without navigation → reset `_formDirty`.
- Custom controls (add/remove/reorder rows) must dispatch `input` AND `change` themselves (`mediaEditorChanged`).
- Reference: `admin_musician_edit.html` (also location/event_form/org/series/timetable/instructor/fetchurl edit).

SECTION-NAV EDIT FORMS (org/location/musician/event)
Adding a section = 3 edits in the same template: (1) nav button `.…-nav-item[data-target="sec-x"]` with `.…-nav-dot` + i18n label, (2) `<section class="form-section" id="sec-x">`, (3) `hasData['sec-x']=function(){…}` (fills dot AND opens section on load). Mobile: nav is a drawer (`#org-nav-toggle`/`#loc-nav-toggle`/`#mus-nav-toggle`).

SHARED BLOCKS
- Reusable piece → `{{define}}` in base.html, call `{{template "name" (dict "S" $.Strings "Links" .Foo)}}` (block sees only its arg). Ref: `media-links-list`, `media-links-editor`+`media-row` (`<template>` clone, parallel arrays `media_kind/media_title/media_url` → `mediaLinksFromForm`).
- Parallel-array forms: zero rows = clear → parser returns non-nil empty slice.

DansalClient READS/WRITES
- Cached list getters omit heavy fields (`media`…): pages showing/editing those must use the single getter (`GetOrganizationDetail`; `GetLocation`/`GetMusician` already single). Symptom of wrong one: saved but renders empty.
- Optional field needing "untouched vs clear": write wrapper with pointer (`orgWrite`/`locationWrite`/`musicianWrite` + `mediaPtr`): nil omitted, ptr-to-empty sends `[]`.
- Event JSON-array fields: PUT (`EventWriteRequest`) overwrites only if `len>0`; PATCH (`EventMergePatchRequest`, `*[]T`) overwrites when non-nil (incl. `[]`).

MAPS: always `attachTileLayer(map)` (static/base.js; dark/light switching). Never `L.tileLayer`.

LAYOUT: CSS `@media` only. Never UA sniffing (server or JS).

DRAG-TO-REORDER: pointer events, not HTML5 DnD. Reuse/generalize `setupDragReorder(handle, dragEl, item, itemMap, targetSel, axis, onDrop)` + `moveInArray` (`admin_timetable.html`): setPointerCapture, ~4px threshold, dim dragEl, WeakMap el→item rebuilt per render, before/after by target midpoint. Durable order → save from `onDrop` immediately (#1278).

SAVE ERRORS (#1421): `adminSaveError(err)` (`save_error.go`) → `ErrorKey/ErrorDetail/ErrorRef` in page data; template `{{$.Strings.T .ErrorKey}}{{template "save-error-extra" (dict "D" .ErrorDetail "R" .ErrorRef "S" $.Strings)}}`. Never a bare "Save failed".

DIALOGS
- Destructive: `<form data-on="submit" data-confirm="…">` (native confirm); buttons elsewhere via `form="id"`.
- `<dialog>`+`showModal()`: resolve in button handlers + `oncancel`, NEVER on `close` (not fired in background tabs); add `margin:auto` (global `*{margin:0}` breaks centering). Refs: `confirmUnusualDate()` (#1413), duplicate-check dialog.
- Unusual date check: `dateUnusual(iso)` JS / `unusualDate` template func; opt in with `data-date-check="YYYY-MM-DD"` / `data-date-check-bulk="<selector>"`.
- Flash across redirect-to-referer: own token param read in `tmplData` (`?pubmsg=`).

A/B COMPARISON: `admin_duplicate.html` (#1427): rows=fields, cols A/B tinted `--dup-a`/`--dup-b` (+dark), <640px rows become blocks via CSS, sticky actions top, destructive bottom, `{{range (list .A .B)}}`.

MISC
- Button classes `.btn-primary/.btn-secondary/.btn-sm-secondary/.btn-danger` live in each template's `<style>` — copy them.
- Status tokens `--badge-{warn,danger,ok}-{bg,fg}` light+dark; `--badge-info-*` dark only → `var(--badge-info-bg,#e0f0ff)`. Contrast ≥4.5:1; never fade with `opacity` (#1426).
- Venue picker outside admin form: `GET /search/locations?q=`; quick room: `POST /admin/api/location/{id}/room/quick-create`.
- Field names: template `name=` must match API/Go struct field names (see API.md).
- Verify: go build/vet/test + e2e desktop+mobile for behaviour changes.
