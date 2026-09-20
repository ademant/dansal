---
name: admin-ui
description: Build or modify dansal admin forms and frontend UI — webmin pages, admin_*.html templates, maps, and form scripts. Use when adding a new admin edit form, wiring unsaved-changes protection, adding a Leaflet map, or making a frontend layout choice. Encodes the _formDirty/_markDirty/safeGoBack guard, attachTileLayer for maps, and the CSS-media-query-not-UA-detection rule.
---

# dansal admin & frontend UI conventions

Frontend lives in `cmd/dansal_web/templates/` (Go HTML templates, server-side rendered, plus inline JS). Admin edit forms follow a small set of standing patterns that all new forms must copy.

## Unsaved-changes guard — every admin form

Each admin edit form (`admin_*.html`) carries three globals at the bottom of the page:

```js
var _formDirty=false;
function _markDirty(){
  if(_formDirty) return;
  _formDirty=true;
  window.addEventListener('beforeunload',function(e){
    if(!_formDirty) return;
    e.preventDefault();
    e.returnValue='';
  });
}
function safeGoBack(){
  if(_formDirty&&!confirm('{{$.Strings.T "admin_unsaved_confirm"}}')){return;}
  _formDirty=false;
  if(window.history.length>1){history.back();}
  else{location.href='{{if .Data.From}}{{.Data.From | js}}{{else}}/admin/<section>{{end}}';}
}
```

And the wiring:

```js
var form=document.getElementById('<form-id>');
form.addEventListener('input',_markDirty);
form.addEventListener('change',_markDirty);
```

Rules:
- **Back button** uses `data-fn="safeGoBack" data-args="[]"` (never `history.back()` directly) — see the first button in `admin_musician_edit.html`.
- **Every input/change fires `_markDirty`** — including select boxes and any custom controls; wire them explicitly.
- **On successful save** the page reloads/re-navigates, so `_formDirty` reset happens naturally; if you add a save path that doesn't navigate, reset `_formDirty=false` after saving.
- `_markDirty` attaches `beforeunload` only on the first change (idempotent).
- Confirm strings come from i18n (`$.Strings.T "admin_unsaved_confirm"`) — add new ones via the `add-i18n` skill (all 12 languages).
- Existing forms using the pattern (the `data-fn`/dispatcher rule above applies to all of them): `admin_musician_edit.html`, `admin_location_edit.html`, `admin_event_form.html`, `admin_org_edit.html`, `admin_series_edit.html`, `admin_timetable.html`, `admin_instructor_edit.html`, `admin_fetchurl_edit.html`.

## No inline handlers — `data-fn` / `data-args` / `data-on`

`script-src` carries no `'unsafe-inline'` (nonce + `'strict-dynamic'`), so `onclick=`/`onchange=`/`onsubmit=` attributes are dead. Buttons and inputs call a function on `window` through the dispatcher documented at the top of `static/base.js`: `data-fn="name"`, `data-args='[...]'` (JSON; the literal `"@this"` passes the element itself), and `data-on="change|submit|input|…"` for non-click events (click is the default). Put new helper functions in `static/base.js`, not in an inline `<script>` that other templates would need to copy.

## Section-nav forms (org / location / musician / event edit)

The long edit forms are a sidebar of sections (`.org-nav-item`, `.loc-nav-item`, `.evt-nav-item`, each with `data-target="sec-…"`) over `<section class="form-section" id="sec-…">` blocks that stay collapsed until opened. Adding a section takes **three** edits in the same template, and forgetting the third leaves a dot that never lights up:

1. a nav button with `<span class="…-nav-dot">` and an i18n label,
2. the `<section id="sec-…">`,
3. an entry in that template's `hasData` map (`'sec-…': function(){ return …; }`). It decides whether the dot is filled **and** whether the section starts open, so a section that already holds data is open on load.

Consequences: custom controls that change data without a real input (add/remove/reorder rows) must dispatch `input` and `change` themselves, because the dots refresh on `input` and the dirty guard listens for both (see `mediaEditorChanged` in `base.js`). On narrow viewports the whole nav is a drawer behind a toggle button (`#org-nav-toggle`, `#loc-nav-toggle`, `#mus-nav-toggle`).

## Repeatable rows and other shared blocks

Put a UI piece that several pages need in `base.html` as a named `{{define}}` block and call it with `{{template "name" (dict "S" $.Strings "Links" .Foo)}}` — a block only sees the one argument you pass, so `$.Strings` has to be handed in as `S`. The external-media-link list is the reference: `media-links-list` (public), `media-links-editor` + `media-row` (admin, rows cloned from a `<template>`, posted as parallel `media_kind`/`media_title`/`media_url` arrays and parsed by `mediaLinksFromForm`). A form that posts parallel arrays must treat "no rows" as "clear", so the parser returns a non-nil empty slice, and the API client sends the field only when non-nil (see next section).

## Pages that read from `DansalClient`: list vs single endpoint

Some `DansalClient` getters answer from a cached list (`GetOrganization` scans the ~1 min `GetOrganizations` cache first). List endpoints deliberately omit heavy per-owner fields such as `media`, so a page or form that shows or edits one of those fields must call the single-resource getter — `GetOrganizationDetail` for organizations; `GetLocation`/`GetMusician` already hit the single endpoint. Symptom of using the wrong one: the data is saved (API returns it) but the page renders as if it were empty.

For writes, an optional field that must distinguish "leave untouched" from "clear" is sent through a wrapper struct that shadows it with a pointer (`orgWrite`/`locationWrite`/`musicianWrite` + `mediaPtr`): `nil` is omitted, a pointer to an empty slice sends `[]`.

## Maps — always `attachTileLayer`

Never call `L.tileLayer` directly in templates or JS. Use the shared helper from `base.html:452`:

```js
attachTileLayer(map);
```

`attachTileLayer` picks light/dark tiles from a single source (Carto dark for dark mode, OSM for light), and a `MutationObserver` + `matchMedia('(prefers-color-scheme:dark)')` listener re-attach the layer when the theme class on `<html>` changes — hand-rolled `L.tileLayer` calls bypass this and break dark mode.

## Drag-to-reorder — pointer events, not native HTML5 DnD

When an admin list/grid needs pick-up-and-drop reordering (room columns and track rows in `admin_timetable.html` are the reference implementation, #1237/#1238), use pointer events, matching this file's existing tile move/resize drag code — not native `dragstart`/`dragover`/`drop`, which has worse touch support and its own quirky styling model.

The shape, generalized as `setupDragReorder(handle, dragEl, item, itemMap, targetSel, axis, onDrop)` in `admin_timetable.html`:

- **`pointerdown` on `handle`** starts the drag; `handle.setPointerCapture(ev.pointerId)` keeps `pointermove`/`pointerup` routed to it regardless of where the pointer physically is (`document.elementFromPoint` still resolves the real element underneath — capture doesn't affect hit-testing, only event `target`).
- **A ~4px movement threshold** before a drag "counts", so a plain click doesn't misfire a reorder.
- **`dragEl.style.opacity` dimmed** while dragging (`dragEl` can differ from `handle` — e.g. a table row's whole `<tr>` dims even though its grip cell is the handle).
- **A `WeakMap<element, item>`** (rebuilt fresh on every re-render, e.g. `_rhRoomMap`/`_trRowMap`) maps whatever DOM element is currently under the pointer back to the domain object it represents — needed because `document.elementFromPoint` + `.closest(targetSel)` only gives you an element, and the same underlying array item can have multiple rendered instances (e.g. one room header per visible day).
- **Drop side decided by the target's midpoint**: which half of the target's bounding rect (`left`/`width` for a horizontal axis, `top`/`height` for vertical) the pointer is over decides insert-before vs. insert-after — so a drag reads as landing where it visibly stopped, not always snapping to one fixed side.
- **The actual reorder** is a generic `moveInArray(arr, src, dest, after)` splice helper, independent of the drag mechanics — keep these two concerns (pointer tracking vs. array mutation) separate so either can be reused without the other.

Don't copy-paste this per new drag-to-reorder feature — factor a shared helper (as `admin_timetable.html` already does for its two consumers) rather than re-deriving the pointer-tracking logic each time.

## Layout — CSS `@media`, never User-Agent detection

**Never use User-Agent detection** (`navigator.userAgent`, etc.) to decide layout. Use CSS `@media` queries. A few responsive helpers live in `base.html`; follow them instead of adding UA sniffing.

## Everything else admin forms share

- Template data flows as `$.Strings` (translations) and `.Data` (handler payload).
- Buttons that navigate back use `class="btn-secondary"` with `safeGoBack()` and an i18n `title`/`aria-label`.
- Form fields mirror the API input types (see `API.md`); keep field names consistent between the Go `EventInput`/location structs and the template `name=` attributes.
- Maps in admin forms are created the same way as public pages: initialize the Leaflet `map` var, then `attachTileLayer(map)`.

## Final checks

```bash
go build ./...
go vet ./...
go test ./...
```

For i18n keys used here, see the `add-i18n` skill. Then `make build` and `sudo make deploy INSTANCE=dev` (see the `deploy` skill).
