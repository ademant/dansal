# dansal-web Routes Reference

Routes served by the `dansal_web` binary (public frontend + ActivityPub), served
at the base URL configured as `web.dansal_url`/`web.domain` in `web.yaml`. This
covers dansal-web's **integration surface** — pages worth linking to, feeds
worth subscribing to, and widgets worth embedding. It intentionally excludes
internal web-app flows (login, settings, registration, booking, checkin,
passkeys, contact-board management) that aren't integration points.

For the `dansal` REST API (`/api/v1/...`), see [API.md](API.md).

## Table of Contents

- [Content Pages](#content-pages)
- [Feeds](#feeds)
- [Discovery Files](#discovery-files)
- [Embeds](#embeds)

## Content Pages

```
GET /                       # index: map, weekly table, upcoming list
GET /events/{id}            # single event
GET /org/{name}             # organization profile + upcoming events (also serves as the ActivityPub actor)
GET /location/{id}          # location profile + upcoming events at that venue
GET /musicians              # musician directory
GET /musicians/{id}         # single musician profile + upcoming events
GET /instructors            # instructor directory
GET /instructors/{id}       # single instructor profile + upcoming events
GET /tags/{slug}            # events carrying one tag (also serves as an ActivityPub OrderedCollection)
GET /organizations          # organization directory
GET /board                  # community ride-share/ticket/lost-and-found board
GET /search                 # search form
GET /search/results         # search results (same query params as GET /api/v1/events, proxied)
```

All public. Each event/org/location/musician/instructor page carries `schema.org`
JSON-LD (`Event`/`Organization`/`Place`/`MusicGroup`/`Person`) and Open Graph
tags for link previews and search-engine structured data.

Event page `<title>` includes the event's start date (e.g. "Balfolk im
Stadtgarten – 19 Sep 2027") so that recurring events at the same venue get a
unique title per date instead of sharing one across occurrences. The meta
description uses the event's own description when set; otherwise one is
auto-assembled from structured fields (type tag, date, location,
musicians/instructors) so every event page still gets a distinct, non-generic
description.

A room (e.g. "Grand Hall" within a larger venue) is a normal location whose
`parent_id` points at its building — `GET /location/{id}` on the building
lists its rooms with a link to each one's own page; a room's own page links
back to its building. Events assigned to any of a building's rooms are
aggregated into that building's own upcoming-events list, in addition to
appearing on the room's own page.

`/org/{name}` doubles as the ActivityPub actor URL — `Accept:
application/activity+json` returns the Actor object instead of HTML; see
`/.well-known/webfinger` under [Discovery Files](#discovery-files).

`/tags/{slug}` works the same way for tags (issue #949): `Accept:
application/activity+json` (or `ld+json`) returns a paged `OrderedCollection`
of `Note` objects (`?page=true` for the embedded items, mirroring
`/org/{name}/outbox`), each attributed to its event's own organization actor
when known, or the relay actor otherwise. `{slug}` must be a real tag slug
(`GET /api/v1/tags`) — anything else 404s rather than rendering an empty
page. The HTML page also carries `<link rel="alternate">` tags to the
Atom/JSON Feed/ActivityPub variants below, and every tag chip shown anywhere
on the site (event/org/location/musician/instructor pages, the weekly table)
links to its `/tags/{slug}` page.

## Feeds

```
GET /events/{id}.ics                        # single event, iCal download
GET /feed/events.{format}                   # all upcoming events
GET /feed/org/{slug}/events.{format}        # one organization's events
GET /feed/musician/{slug}/events.{format}   # one musician's events
GET /feed/instructor/{id}/events.{format}   # one instructor's events
GET /feed/location/{slug}/events.{format}   # one location's events
GET /feed/ball/events.{format}              # events tagged as a ball/bal
GET /feed/workshop/events.{format}          # events tagged as a workshop
GET /feed/festival/events.{format}          # events tagged as a festival
GET /tags/{slug}.atom                       # any tag, Atom 1.0
GET /tags/{slug}.jsonfeed                   # any tag, JSON Feed 1.1 (jsonfeed.org)
```

`{format}` is `ical` (or `ics`), `rss`, or `json`. `{slug}` for org/musician/
location feeds is the same slug used in that entity's page URL
(`orgSlug`/`effectiveSlug`) — not the numeric ID. Instructor feeds use the
numeric `{id}` (matching the `/instructors/{id}` page URL) because instructors
have no unique slug. All feeds are public and contain only published events.

Unlike the other feed families, tag feeds aren't limited to the three legacy
ball/workshop/festival slugs — `/tags/{slug}.atom` and `/tags/{slug}.jsonfeed`
work for any tag in `GET /api/v1/tags` (an unknown slug 404s). They're
additive: `/feed/ball|workshop|festival/events.{format}` are unchanged and
keep working for existing subscribers.

## Discovery Files

```
GET /sitemap.xml                     # XML sitemap (30 min cache) — see below
GET /robots.txt                      # crawler directives + sitemap pointer
GET /llms.txt                        # plain-text site summary for LLM crawlers
GET /manifest.json                   # web app manifest (PWA icons/theme)
GET /opensearch.xml                  # OpenSearch description for browser search-bar integration
GET /.well-known/webfinger           # ActivityPub actor discovery (?resource=acct:...)
GET /.well-known/host-meta           # LRDD pointer to WebFinger, XRD/XML (RFC 6415)
GET /.well-known/host-meta.json      # same, JSON LRDD — some Fediverse clients prefer this over XML
GET /.well-known/nodeinfo            # ActivityPub server metadata pointer
GET /.well-known/security.txt        # security contact (RFC 9116)
GET /{indexnow-key}.txt              # IndexNow ownership verification (only if a key is configured)
```

WebFinger resolves both `acct:slug@domain` and the actor's own `https://` URL
(`webfingerSlug` in `actor.go`). Some Fediverse clients probe common
local-part conventions (`admin@domain`, `info@domain`) speculatively, without
being told they exist first; `web.yaml`'s `webfinger_aliases` map lets an
admin route those to a real actor slug (e.g. `{admin: relay}`) instead of a
404 — unconfigured probes still 404 as before.

`/org/{name}` pages also carry a `<link rel="alternate"
type="application/activity+json" href="…">` pointing at their own actor URL,
so crawlers that only look at page `<head>` (rather than sending `Accept:
application/activity+json` and relying on content negotiation) can still
discover the actor.

`/{indexnow-key}.txt` is served dynamically from the `indexnow_key` set in
webmin's site-config — the path segment must exactly match `{key}.txt`, any
other value 404s. When a key is configured, dansal-web pings the IndexNow API
(`https://api.indexnow.org/indexnow`) in the background after admin
create/save/cancel, bulk-publish/bulk-cancel, and feed-import confirm, so
Bing/Yandex/Seznam can (re)index the affected event pages immediately instead
of waiting for their next crawl.

`sitemap.xml` includes: the homepage, `/musicians`, `/instructors`,
`/organizations`, all published events (past and future), all locations, all
organizations (at their `effectiveSlug()`), all musicians, and all instructors.
It does not include feed URLs, embeds, or the internal web-app flows excluded
from this document.

## Embeds

```
GET /embed/events            # filterable upcoming event list
GET /embed/event/{id}        # single event card
GET /embed/org/{slug}        # organization profile card with upcoming events
GET /embed/next              # minimal upcoming events ticker
GET /embed/locations         # map of all locations with event counts
GET /embed/calendar          # combined map + filterable event list
GET /embed/manifest.json     # machine-readable description of the six widgets above
```

Designed to be placed in an `<iframe>` on a third-party site (e.g. the
[wp-dansal](https://github.com/ademant/wp-dansal) WordPress plugin). All are
public, unauthenticated, and public-events-only.

**`/embed/manifest.json`** is the machine-readable counterpart to this
section — it lists each widget's path and query params (name, type,
`repeatable`, `default`, description) as JSON, so an integration can
introspect available options at runtime instead of hardcoding them:

```json
{
  "widgets": {
    "events": {
      "path": "/embed/events",
      "description": "Filterable upcoming event list",
      "params": {
        "org": {"type": "string", "repeatable": true, "description": "org slug filter; repeatable, e.g. ?org=a&org=b"},
        "location": {"type": "number", "repeatable": true, "description": "location ID filter; repeatable"},
        "mode": {"type": "string", "default": "agenda", "description": "display mode"},
        "lang": {"type": "string", "description": "override display language (2-letter code)"}
      }
    }
  }
}
```

Common query params across widgets:
- `org=` — organization slug filter, repeatable (`?org=a&org=b`); omit for all orgs
- `location=` — location ID filter, repeatable; omit for all locations
- `lang=` — override display language (2-letter code); falls back to `Accept-Language`/cookie detection

### `/embed/events`

| Param | Type | Default | Notes |
|---|---|---|---|
| `org` | string, repeatable | — | |
| `location` | number, repeatable | — | |
| `mode` | string | `agenda` | display mode |
| `lang` | string | — | |

```html
<iframe src="https://balfolk.jetzt/embed/events?org=balfolkmarburg" width="100%" height="600" style="border:0"></iframe>
```

### `/embed/event/{id}`

Single event card. Only `lang=` is accepted.

```html
<iframe src="https://balfolk.jetzt/embed/event/307" width="400" height="300" style="border:0"></iframe>
```

### `/embed/org/{slug}`

| Param | Type | Default | Notes |
|---|---|---|---|
| `events` | number | `3` | how many upcoming events to list; `0` hides the list |
| `lang` | string | — | |

```html
<iframe src="https://balfolk.jetzt/embed/org/balfolkmarburg?events=5" width="400" height="500" style="border:0"></iframe>
```

### `/embed/next`

| Param | Type | Default | Notes |
|---|---|---|---|
| `org` | string, repeatable | — | |
| `location` | number, repeatable | — | |
| `count` | number | `5` | max events shown |
| `lang` | string | — | |

```html
<iframe src="https://balfolk.jetzt/embed/next?count=3" width="100%" height="200" style="border:0"></iframe>
```

### `/embed/locations`

| Param | Type | Default | Notes |
|---|---|---|---|
| `org` | string, repeatable | — | |
| `lang` | string | — | |

```html
<iframe src="https://balfolk.jetzt/embed/locations" width="100%" height="500" style="border:0"></iframe>
```

### `/embed/calendar`

| Param | Type | Default | Notes |
|---|---|---|---|
| `org` | string, repeatable | — | |
| `location` | number, repeatable | — | |
| `from` | string (`YYYY-MM-DD`) | today | |
| `to` | string (`YYYY-MM-DD`) | `from` + 14 days | range capped at 366 days |
| `tag` | string | — | pre-selects a tag in the client-side filter |
| `lang` | string | — | |

```html
<iframe src="https://balfolk.jetzt/embed/calendar?from=2026-08-01&to=2026-08-31" width="100%" height="700" style="border:0"></iframe>
```
