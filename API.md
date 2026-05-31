# dansal API

RESTful calendar API backed by SQLite. All timestamps are RFC3339.

## Base URL
```
http://localhost:8000
```

## Authentication

Protected endpoints require a Bearer token obtained from `POST /api/v1/login`, or an API key created via `POST /api/v1/apikeys`.

```
Authorization: Bearer <token-or-api-key>
```

API keys begin with `ak_` and never expire. Session tokens expire after the configured duration (default 24 h).

Public GET endpoints accept an optional `Authorization` header; an invalid or expired token is still rejected with 401.

## Roles

| Role | Permissions |
|------|-------------|
| `admin` | Full access; bypasses all organization checks |
| `user` | Read + write; must be org member for org-linked resources |
| `publisher` | Read + create; no update or delete |
| `viewer` | Read published events only |

---

## Info

### GET /api/v1/info
Returns server version and build time. Public.

```json
{ "version": "1.2.3", "build_time": "2026-05-15T10:00:00Z" }
```

---

## Authentication endpoints

### POST /api/v1/login

```json
{ "username": "admin", "password": "secret" }
```

Also accepts `application/x-www-form-urlencoded`.

Response `200`:
```json
{
  "token": "string",
  "expires_at": "2026-06-15T10:00:00Z",
  "user": { "id": 1, "username": "admin", "email": "admin@localhost", "role": "admin", "created_at": "..." }
}
```

### DELETE /api/v1/login

Revokes the current session token. No body required.

Response `204`.

### POST /api/v1/login/magic

Sends a one-time magic-link login email.

```json
{ "email": "user@example.com" }
```

### GET /api/v1/login/magic/{token}

Consumes a magic-link token and returns a session token (same shape as `POST /api/v1/login` response).

---

## Sessions

Requires authentication.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/sessions` | List own active sessions |
| DELETE | `/api/v1/sessions/{id}` | Revoke a session |

---

## Users

All user endpoints require admin role, except `PUT /api/v1/users/{id}` which also allows the user themselves.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/users` | List all users |
| POST | `/api/v1/users` | Create user |
| GET | `/api/v1/users/{id}` | Get user |
| PUT | `/api/v1/users/{id}` | Update user |
| DELETE | `/api/v1/users/{id}` | Delete user (non-admin only) |
| POST | `/api/v1/users/{id}/verify` | Send verification message |

**User object:**
```json
{
  "id": 1,
  "username": "alice",
  "email": "alice@example.com",
  "role": "user",
  "telegram": "@alice",
  "matrix": "@alice:matrix.org",
  "email_verified": false,
  "telegram_verified": false,
  "matrix_verified": false,
  "disabled": false,
  "created_at": "..."
}
```

**Create body:**
```json
{ "username": "string", "email": "string", "password": "string", "role": "user",
  "telegram": "string", "matrix": "string" }
```

**Update body** — all fields optional; only admin may change `role`, `*_verified`, `disabled`:
```json
{ "email": "string", "role": "user", "telegram": "string", "matrix": "string",
  "email_verified": true, "telegram_verified": false, "disabled": false }
```

Valid roles: `admin`, `user`, `publisher`, `viewer`.

---

## Invites

| Method | Path | Description | Role |
|--------|------|-------------|------|
| GET | `/api/v1/invites` | List active invites | admin |
| POST | `/api/v1/invites` | Create invite link | admin |
| DELETE | `/api/v1/invites/{token}` | Revoke invite | admin |
| POST | `/api/v1/invites/{token}` | Accept invite (register) | public |

**Create body:**
```json
{ "role": "user", "max_uses": 1, "expires_in_hours": 48 }
```

---

## API Keys

Requires authentication. Users manage their own keys; admins can manage any.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/apikeys` | List own API keys |
| POST | `/api/v1/apikeys` | Create API key |
| DELETE | `/api/v1/apikeys/{id}` | Delete API key |

**Create body:** `{ "name": "my-script" }`

**Create response `201`** — the `key` field is only returned once:
```json
{ "id": 1, "name": "my-script", "key": "ak_...", "created_at": "..." }
```

---

## Organizations

| Method | Path | Description | Role |
|--------|------|-------------|------|
| GET | `/api/v1/organizations` | List | public |
| POST | `/api/v1/organizations` | Create | admin |
| GET | `/api/v1/organizations/{id}` | Get | public |
| PUT | `/api/v1/organizations/{id}` | Update | admin |
| DELETE | `/api/v1/organizations/{id}` | Delete | admin |
| GET | `/api/v1/organizations/{id}/members` | List members | any |
| POST | `/api/v1/organizations/{id}/members` | Add member | admin |
| DELETE | `/api/v1/organizations/{id}/members/{user_id}` | Remove member | admin |

**Organization object:**
```json
{
  "id": 1,
  "name": "string",
  "description": "string",
  "actor_name": "string",
  "website": "string",
  "instagram": "string",
  "mastodon": "string",
  "facebook": "string",
  "contact_email": "string",
  "contact_name": "string",
  "created_at": "2026-01-01T00:00:00Z",
  "image_url": "string",
  "notes_md": "string",
  "fetch_source_id": 42
}
```

`fetch_source_id` — when present, indicates this organization has an associated fetch source for automatic event importing. Use `GET /api/v1/fetchurl/{id}` to retrieve the source details and `POST /api/v1/fetchurl/{id}/fetch` to trigger an immediate import.

**Add member body:** `{ "user_id": 42 }`

---

## Locations

GET endpoints are public (optional auth). Write endpoints require authentication.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/locations` | List |
| POST | `/api/v1/locations` | Create |
| GET | `/api/v1/locations/{id}` | Get |
| PUT | `/api/v1/locations/{id}` | Update |
| DELETE | `/api/v1/locations/{id}` | Delete |

**Location object:**
```json
{
  "id": 1,
  "location": "Kulturzentrum",
  "short_name": "KZ",
  "address": "Hauptstr. 1",
  "zipcode": "10115",
  "town": "Berlin",
  "country": "Germany",
  "latitude": "52.5200",
  "longitude": "13.4050",
  "internetsite": "https://example.com",
  "organization_id": 3,
  "created_at": "..."
}
```

**Create/Update body** — all fields optional except `location` on create:
```json
{
  "location": "string", "short_name": "string",
  "address": "string", "zipcode": "string",
  "town": "string", "country": "string",
  "latitude": "string", "longitude": "string",
  "internetsite": "string", "organization_id": 3
}
```

`organization_id` is required for `user`/`publisher` (must be org member).

---

## Musicians

GET endpoints are public (optional auth). Write endpoints require authentication.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/musicians` | List |
| POST | `/api/v1/musicians` | Create |
| GET | `/api/v1/musicians/{id}` | Get |
| PUT | `/api/v1/musicians/{id}` | Update |
| DELETE | `/api/v1/musicians/{id}` | Delete |

**Musician object:**
```json
{
  "id": 1,
  "bandname": "La Troupe",
  "short_name": "LT",
  "internetsite": "https://latroupe.example.com",
  "description": "string",
  "created_at": "..."
}
```

**Create/Update body:**
```json
{ "bandname": "string", "short_name": "string", "internetsite": "string", "description": "string" }
```

---

## Events

### Event object

```json
{
  "id": 1,
  "uid": "abc123@example.com",
  "title": "Bal Folk",
  "description": "string",
  "start_time": "2026-05-15T20:00:00+02:00",
  "end_time": "2026-05-15T23:00:00+02:00",
  "has_ball": true,
  "has_workshop": false,
  "is_cancelled": false,
  "tags": ["bal-folk", "Köln"],
  "is_published": true,
  "short_code": "8b911390",
  "url": "https://example.com/event/42",
  "image_url": "/api/v1/images/1",
  "organization_id": 3,
  "location_id": 7,
  "location": "Kulturzentrum",
  "location_town": "Berlin",
  "location_country": "Germany",
  "pricing": {
    "type": "multiple",
    "currency": "EUR",
    "prices": [
      { "label": "normal", "amount": 12 },
      { "label": "student", "amount": 8 }
    ]
  },
  "musicians": [
    { "id": 2, "bandname": "La Troupe" }
  ],
  "timetable": [
    {
      "id": 1, "event_id": 1,
      "start_time": "20:00", "end_time": "21:30",
      "title": "Workshop", "description": "string", "room": "Hall A",
      "location_id": 7, "location_name": "Kulturzentrum",
      "created_at": "..."
    }
  ],
  "created_at": "..."
}
```

The `pricing` field is optional. `type` must be one of:

| Value | Description |
|-------|-------------|
| `free` | No admission fee |
| `donation` | Pay what you want |
| `single` | One fixed price; set `amount` and optionally `currency` |
| `multiple` | Tiered pricing; set `prices` as `[{label, amount}]` and optionally `currency` |

`uid` is set from the iCal `UID` field on import and used for deduplication: re-importing the same feed updates existing events. Without `uid`, deduplication falls back to title + location + start time (±3 h).

`timetable` and `musicians` are only populated on `GET /api/v1/events/{id}`, not in the list endpoint.

### Endpoints

GET endpoints are public (optional auth). Write endpoints require authentication.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/events` | List events |
| POST | `/api/v1/events` | Create event(s) |
| PATCH | `/api/v1/events` | Bulk-update events |
| GET | `/api/v1/events/{id}` | Get event (includes timetable + musicians) |
| DELETE | `/api/v1/events/{id}` | Delete event |
| POST | `/api/v1/events/{id}/publish` | Publish event |
| POST | `/api/v1/events/{id}/timetable` | Add timetable entries |
| PUT | `/api/v1/events/{id}/timetable` | Replace timetable |
| DELETE | `/api/v1/events/{id}/timetable/{entry_id}` | Delete timetable entry |
| POST | `/api/v1/events/{id}/locations` | Attach location |
| DELETE | `/api/v1/events/{id}/locations/{location_id}` | Detach location |

**Write access:** `user`/`publisher` must be members of the event's `organization_id`. `admin` bypasses this check.

**Publication:** events created by `user` or `admin` are published immediately; `publisher` and `viewer` create unpublished events.

### GET /api/v1/events — query parameters

| Parameter | Description |
|-----------|-------------|
| `title` | Partial match |
| `description` | Partial match |
| `start_time_after` | RFC3339 |
| `start_time_before` | RFC3339 |
| `end_time_after` | RFC3339 |
| `end_time_before` | RFC3339 |
| `location` | Partial match on location name |
| `country` | Exact match on location country |
| `lat`, `lon`, `radius_km` | Geo bounding box (all three required) |
| `tag` | Partial match |
| `has_ball` | `true`/`false` |
| `has_workshop` | `true`/`false` |
| `is_published` | `true`/`false` — admin/user only |
| `include_past` | `true` to include past events (default: future only) |
| `limit` | Default 100, max 1000 |
| `offset` | Default 0 |
| `code` | Short-code lookup (public, unauthenticated) |

Unauthenticated requests and `viewer` role only see published events.

### POST /api/v1/events — JSON

**Single event:**
```json
{
  "uid": "optional-stable-id",
  "title": "Bal Folk",
  "description": "string",
  "start_time": "2026-05-15T20:00:00",
  "end_time": "2026-05-15T23:00:00",
  "has_ball": true,
  "has_workshop": false,
  "tags": ["bal-folk"],
  "organization_id": 3,
  "musicians": [1, 2],
  "location": {
    "location": "Kulturzentrum", "address": "Hauptstr. 1",
    "zipcode": "10115", "town": "Berlin", "country": "Germany",
    "latitude": "52.52", "longitude": "13.40",
    "eventsite": "https://example.com/event/42"
  },
  "pricing": { "type": "single", "amount": 10, "currency": "EUR" }
}
```

**Event series** — share title/location, differ by date:
```json
{
  "title": "Weekly Dance",
  "has_ball": true,
  "tags": ["bal-folk"],
  "location": { "location": "Kulturzentrum" },
  "date": [
    { "description": "Week 1", "start_time": "2026-05-15T20:00:00", "end_time": "2026-05-15T23:00:00" },
    { "description": "Week 2", "start_time": "2026-05-22T20:00:00", "end_time": "2026-05-22T23:00:00" }
  ]
}
```

**Multiple events** — POST a JSON array of event objects.

Response `201`: array of created event objects.

### POST /api/v1/events — iCal upload

```
Content-Type: text/calendar
```

Send a `.ics` file body. Optional query parameter `organization_id` assigns all imported events to an organization. iCal fields mapped:

| iCal field | Event field |
|------------|-------------|
| `UID` | `uid` (dedup key) |
| `SUMMARY` | `title` |
| `DESCRIPTION` | `description` |
| `DTSTART` / `DTEND` | `start_time` / `end_time` |
| `DURATION` | used to derive `end_time` when `DTEND` absent |
| `LOCATION` | location name |
| `GEO` | location latitude/longitude |
| `CATEGORIES` | `tags` |
| `STATUS: CANCELLED` | `is_cancelled` |
| `ORGANIZER` | organization (found or created by CN/email) |
| `ATTACH` with `FMTTYPE=image/*` | event image |
| `RRULE` | recurring events expanded into individual occurrences |

### PATCH /api/v1/events

Bulk-update multiple events. Body is an array of partial event objects each containing at minimum an `"id"` field.

```json
[
  { "id": 1, "is_published": true },
  { "id": 2, "tags": ["bal-folk", "Köln"] }
]
```

### POST /api/v1/events/{id}/publish

Publishes an unpublished event. Requires `user` or `admin` role and org membership.

Response `200`: updated event object.

### Timetable entries

**Add (POST) / Replace (PUT) body** — array of:
```json
[
  {
    "start_time": "20:00", "end_time": "21:30",
    "title": "Workshop", "description": "string",
    "room": "Hall A", "location_id": 7
  }
]
```

`start_time` and `end_time` are `HH:MM` clock times relative to the event date.

---

## Images

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/images/{event_id}` | Get event image (AVIF) — public |
| POST | `/api/v1/images/{event_id}` | Upload event image |
| DELETE | `/api/v1/images/{event_id}` | Delete event image |

Upload accepts any common image format. The image is resized to configured dimensions and stored as AVIF.

---

## Tags

### GET /api/v1/tags — public

Returns all distinct tags across published events.

```json
["Bal Folk", "Workshop", "Köln"]
```

---

## iCal feeds — public

No authentication required.

| Path | Description |
|------|-------------|
| `GET /api/v1/events.ics` | All published events |
| `GET /api/v1/events/tag/{tag}.ics` | Published events for a specific tag |
| `GET /api/v1/events/town/{town}.ics` | Published events for a specific town |

Supports `Accept: text/calendar` on `GET /api/v1/events` as an alternative to the `.ics` paths.

---

## Fetch Sources

Stores external calendar feeds and imports events from them on demand. All endpoints require at least `user` role.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/fetchurl` | List sources |
| POST | `/api/v1/fetchurl` | Add single source and import |
| POST | `/api/v1/fetchurl/bulk` | Add multiple sources and import |
| GET | `/api/v1/fetchurl/{id}` | Get source |
| POST | `/api/v1/fetchurl/{id}/fetch` | Re-import from source |
| POST | `/api/v1/fetchurl/fetch-all` | Re-import from all sources |

**Fetch source object:**
```json
{
  "id": 1,
  "url": "https://example.com/calendar.ics",
  "type": "ical",
  "tags": ["bal-folk"],
  "organization_id": 3,
  "last_fetched_at": "2026-05-15T10:00:00Z",
  "created_at": "..."
}
```

`organization_id` — when set, all events imported from this source are assigned to that organization, overriding any organizer information in the feed itself.

**Supported types:**

| Type | Description |
|------|-------------|
| `ical` | Standard iCalendar feed |
| `folkdance-json` | folkdance.page JSON API |

Type is auto-detected via a HEAD request when omitted.

### POST /api/v1/fetchurl

```json
{ "url": "https://example.com/calendar.ics", "type": "ical", "tags": ["bal-folk"], "organization": "My Dance Club" }
```

`type` and `tags` are optional. `organization` (string name) finds or creates an organization with that name and assigns it to the source. `organization_id` (integer) takes precedence when both are provided.

Re-POSTing the same URL updates its `type`, `tags`, and `organization_id` (upsert by URL).

Response: array of imported event objects. `201` when all events were newly created; `200` if any were updates.

### POST /api/v1/fetchurl/bulk

Registers and imports multiple sources in one call. Entries that do not contain `://` are treated as tags applied to every source in the batch.

```json
{
  "entries": [
    "https://example.com/feed1.ics",
    "https://example.com/feed2.ics",
    "bal-folk"
  ],
  "tags": ["Germany"],
  "organization": "My Dance Club"
}
```

`type` — optional; auto-detected per URL when omitted. `organization`, `organization_id`, and `tags` are applied to all sources. `organization_id` takes precedence over `organization` when both are set.

Response: array of per-source results:
```json
[
  { "url": "https://…/feed1.ics", "source_id": 1, "events": 12, "all_created": true },
  { "url": "https://…/feed2.ics", "source_id": 2, "events": 3,  "all_created": false },
  { "url": "https://…/feed3.ics", "source_id": 3, "events": 0,  "error": "remote returned 404" }
]
```

### POST /api/v1/fetchurl/fetch-all

Re-imports all stored sources in parallel. Same response shape as `/bulk`.

---

## Status codes

| Code | Meaning |
|------|---------|
| 200 | OK |
| 201 | Created |
| 204 | No content (delete / logout) |
| 400 | Bad request / invalid input |
| 401 | Missing or invalid credentials |
| 403 | Forbidden (wrong role or not org member) |
| 404 | Not found |
| 415 | Unsupported media type |
| 429 | Rate limit exceeded (login) |
| 502 | Bad gateway (upstream fetch failed) |
| 500 | Internal server error |
