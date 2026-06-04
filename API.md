# dansal API

REST-style calendar API backed by SQLite. Unless noted otherwise, request and response bodies are JSON and timestamps are RFC3339 strings in API payloads.

## Table of Contents

- [Base URL](#base-url)
- [Authentication](#authentication)
- [Roles](#roles)
- [Content Negotiation](#content-negotiation)
- [Info](#info)
- [Vocabulary](#vocabulary)
- [Authentication Endpoints](#authentication-endpoints)
- [Sessions](#sessions)
- [Users](#users)
- [Registration, Invites, and Verification](#registration-invites-and-verification)
- [WebAuthn Credentials](#webauthn-credentials)
- [API Keys and Publishers](#api-keys-and-publishers)
- [Organizations](#organizations)
- [Locations](#locations)
- [Musicians](#musicians)
- [Dances and Tags](#dances-and-tags)
- [Events](#events)
- [Anonymous Event Suggestions](#anonymous-event-suggestions)
- [iCal Feeds](#ical-feeds)
- [Images](#images)
- [Fetch Sources](#fetch-sources)
- [Contact Posts](#contact-posts)
- [Bookings](#bookings)
- [Telegram Webhook](#telegram-webhook)
- [Status Codes](#status-codes)

## Base URL

```text
http://localhost:8000
```

## Authentication

Protected endpoints require a bearer token from `POST /api/v1/login`, `GET /api/v1/login/magic/{token}`, WebAuthn login, mTLS certificate login, or an API key from `POST /api/v1/apikeys`.

```http
Authorization: Bearer <token-or-api-key>
```

- API keys begin with `ak_`
- Session tokens expire after the configured duration
- Public GET endpoints may accept an optional bearer token
- When present, it can expose user-specific fields such as `editable`

## Roles

| Role | Permissions |
|------|-------------|
| `admin` | Full access; bypasses organization checks |
| `user` | Read + write; must be an organization member for organization-scoped writes |
| `publisher` | Read + create/publish/cancel within allowed organization scope |
| `viewer` | Read published data only |

---

## Content Negotiation

Several GET endpoints support alternative output formats via the `Accept` header. The default is always `application/json`.

| `Accept` header | Format | Supported on |
|-----------------|--------|--------------|
| `application/json` | JSON (default) | all endpoints |
| `text/calendar` | iCalendar (RFC 5545) | `GET /api/v1/events`, `GET /api/v1/events/{id}` |
| `application/atom+xml` | Atom feed (RFC 4287) | events, musicians, locations, organizations |
| `application/geo+json` | GeoJSON (RFC 7946) | `GET /api/v1/locations`, `GET /api/v1/locations/{id}` |

When `Accept: application/atom+xml` is sent, the response contains an Atom `<feed>` with one `<entry>` per resource. When `Accept: application/geo+json` is sent for locations, the response is a GeoJSON `FeatureCollection` (or a single `Feature` for `/{id}`).

---

## Info

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/info` | public | Server version and build time |

**Example response:**
```json
{ "version": "1.2.3", "build_time": "2026-05-15T10:00:00Z" }
```

---

## Vocabulary

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/vocabulary` | public | Enumerable field values used across the API |

Use this endpoint to build dynamic filter UIs without hardcoding strings.

**Response:**
```json
{
  "event_types": [
    { "key": "ball",     "label": "Ball" },
    { "key": "workshop", "label": "Workshop" },
    { "key": "festival", "label": "Festival" }
  ],
  "workshop_difficulties": ["beginner", "intermediate", "advanced"],
  "pricing_types": ["free", "donation", "single", "multiple"],
  "attributes": ["wheelchair", "bar", "kitchen"],
  "osm_types": ["node", "way", "relation"]
}
```

---

## Authentication Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/login` | public | Login with query/form credentials |
| POST | `/api/v1/login` | public | Login with JSON or form credentials |
| DELETE | `/api/v1/login` | bearer | Revoke the current session token |
| POST | `/api/v1/cert-login` | mTLS | Create a session for the certificate-authenticated user |
| POST | `/api/v1/login/magic` | public | Send a magic-link login email |
| GET | `/api/v1/login/magic/{token}` | public | Consume a magic-link token |
| POST | `/api/v1/auth/webauthn/login/begin` | public | Begin passkey login |
| POST | `/api/v1/auth/webauthn/login/finish` | public | Finish passkey login |

**Login body:**
```json
{ "username": "admin", "password": "secret" }
```

`username` may also be an email address. `POST /api/v1/login/magic` accepts:

```json
{ "email": "user@example.com" }
```

**Successful login response:**
```json
{
  "token": "string",
  "expires_at": "2026-06-15T10:00:00Z",
  "user": {
    "id": 1,
    "username": "admin",
    "email": "admin@localhost",
    "role": "admin",
    "created_at": "2026-01-01T00:00:00Z"
  }
}
```

---

## Sessions

Requires authentication.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/sessions` | List the caller's active sessions |
| DELETE | `/api/v1/sessions/{id}` | Revoke one session |

---

## Users

Most user management endpoints require `admin`. `PUT /api/v1/users/{id}`, `POST /api/v1/user/password`, and `DELETE /api/v1/users/me` may be used by the target user where allowed by the handler.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/users` | List users |
| POST | `/api/v1/users` | Create user |
| GET | `/api/v1/users/{id}` | Get user |
| PUT | `/api/v1/users/{id}` | Update user |
| DELETE | `/api/v1/users/{id}` | Delete user |
| DELETE | `/api/v1/users/me` | Delete own account |
| POST | `/api/v1/user/password` | Change own password |
| POST | `/api/v1/users/{id}/password` | Admin-set user password |
| POST | `/api/v1/users/{id}/verify` | Send verification message |
| POST | `/api/v1/users/{id}/magic-link` | Generate a magic login link |
| POST | `/api/v1/users/{id}/telegram/message` | Send a Telegram message to a verified user |
| GET | `/api/v1/pending-invites` | List pending email invites |
| POST | `/api/v1/pending-invites/{id}/resend` | Resend a pending invite |

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
  "created_at": "2026-01-01T00:00:00Z"
}
```

Valid roles: `admin`, `user`, `publisher`, `viewer`.

---

## Registration, Invites, and Verification

### Public Registration and Invite Flows

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/verify/{token}` | public | Consume an account verification token |
| POST | `/api/v1/register` | public | Create a pending registration |
| GET | `/api/v1/register/status/{id}` | public | Get pending registration status |
| POST | `/api/v1/register/resend/{token}` | public | Resend registration verification |
| DELETE | `/api/v1/register/{token}` | public | Cancel pending registration |
| GET | `/api/v1/register/verify/email/{token}` | public | Verify registration email |
| POST | `/api/v1/register/passkey/begin` | public | Begin passkey binding for registration |
| POST | `/api/v1/register/passkey/finish` | public | Finish passkey binding for registration |
| GET | `/api/v1/invites/{token}` | public | Get non-sensitive invite info |
| POST | `/api/v1/invites/{token}` | public | Accept invite and register |
| POST | `/api/v1/invites/{token}/webauthn/begin` | public | Begin invite passkey registration |
| POST | `/api/v1/invites/{token}/webauthn/finish` | public | Finish invite passkey registration |

### Protected Approval and Invite Management

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/pending-registrations` | List pending registrations |
| GET | `/api/v1/pending-registrations/count` | Count verified unactioned registrations |
| POST | `/api/v1/pending-registrations/{id}/approve` | Approve registration |
| DELETE | `/api/v1/pending-registrations/{id}` | Reject registration |
| GET | `/api/v1/invites` | List active invites |
| POST | `/api/v1/invites` | Create invite |
| DELETE | `/api/v1/invites/{token}` | Revoke invite |

**Registration body:**
```json
{
  "email": "user@example.com",
  "description": "Why I need access",
  "reg_type": "join_org",
  "org_id": 3,
  "channel": "email",
  "telegram": "@user"
}
```

`reg_type` is `join_org` or `new_org`. For `new_org`, use `org_name`, `org_actor_name`, `org_description`, `org_website`, and `org_contact_email`.

**Invite creation body:**
```json
{ "role": "user", "max_uses": 1, "expires_in_hours": 48 }
```

---

## WebAuthn Credentials

Requires authentication.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/user/webauthn/credentials` | List caller's passkeys |
| POST | `/api/v1/user/webauthn/register/begin` | Begin adding a passkey |
| POST | `/api/v1/user/webauthn/register/finish` | Finish adding a passkey |
| DELETE | `/api/v1/user/webauthn/credentials/{id}` | Delete passkey |

---

## API Keys and Publishers

Requires authentication. Users manage their own API keys; admins may create keys for another `user_id`.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/apikeys` | List API keys |
| POST | `/api/v1/apikeys` | Create API key |
| DELETE | `/api/v1/apikeys/{id}` | Delete API key |
| POST | `/api/v1/publishers` | Create publisher service account and API key |
| POST | `/api/v1/publishers/{id}/regenerate-key` | Rotate publisher API key |

**Create API key body:**
```json
{ "name": "my-script", "expires_at": "2026-12-31T23:59:59Z" }
```

**Create response (includes secret key only once):**
```json
{ "id": 1, "user_id": 2, "name": "my-script", "key": "ak_...", "created_at": "2026-01-01T00:00:00Z" }
```

**Create publisher body:**
```json
{ "name": "partner-feed", "org_id": 3 }
```

---

## Organizations

GET list/detail endpoints are public with optional auth. Write and membership endpoints require authentication.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/organizations` | List organizations |
| GET | `/api/v1/organizations/stats` | Organization statistics |
| GET | `/api/v1/organizations/check-actor-name` | Check ActivityPub actor-name availability |
| POST | `/api/v1/organizations` | Create organization |
| GET | `/api/v1/organizations/{id}` | Get organization |
| PUT | `/api/v1/organizations/{id}` | Update organization |
| DELETE | `/api/v1/organizations/{id}` | Delete organization |
| GET | `/api/v1/organizations/{id}/members` | List members |
| POST | `/api/v1/organizations/{id}/members` | Add member |
| DELETE | `/api/v1/organizations/{id}/members/{user_id}` | Remove member |

Content negotiation: `Accept: application/atom+xml` returns an Atom feed.

**Organization object:**
```json
{
  "id": 1,
  "name": "string",
  "description": "string",
  "actor_name": "string",
  "website": "https://example.com",
  "instagram": "string",
  "mastodon": "string",
  "facebook": "string",
  "contact_email": "info@example.com",
  "contact_name": "string",
  "wikidata_id": "Q12345",
  "image_url": "/api/v1/org-images/1",
  "notes_md": "string",
  "fetch_source_id": 42,
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": 1748000000
}
```

**Add member body:**
```json
{ "user_id": 42 }
```

---

## Locations

GET endpoints are public with optional auth. Write endpoints require authentication.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/locations` | List locations |
| POST | `/api/v1/locations` | Create one or more locations |
| GET | `/api/v1/locations/{id}` | Get location |
| PATCH | `/api/v1/locations/{id}` | Update location |
| DELETE | `/api/v1/locations/{id}` | Delete location |
| POST | `/api/v1/locations/{id}/assign-org` | Assign location to organization |
| POST | `/api/v1/locations/unassign-org` | Remove one organization assignment |
| POST | `/api/v1/locations/bulk-assign-org` | Bulk assign organization |
| POST | `/api/v1/locations/merge` | Merge two locations |

Content negotiation: `Accept: application/geo+json` returns a GeoJSON `FeatureCollection`; `Accept: application/atom+xml` returns an Atom feed.

### Location List Query Parameters

| Parameter | Description |
|-----------|-------------|
| `country` | Comma-separated ISO country codes (e.g. `DE,FR`) |
| `name` | Case-insensitive substring match on venue name |
| `town` | Case-insensitive substring match on town |
| `org_id` | Locations belonging to the given organization ID |
| `lat`, `lng`, `radius` | Centre point (decimal degrees) + radius in km; converted to bounding box |
| `bbox` | Bounding box: `minLng,minLat,maxLng,maxLat` |

When `lat/lng/radius` is used, each result includes a `distance_km` field.

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
  "country_code": "DE",
  "region": "Berlin",
  "latitude": 52.52,
  "longitude": 13.405,
  "geohash": "u33db2p",
  "internetsite": "https://example.com",
  "osm_id": 123456,
  "osm_type": "way",
  "wikidata_id": "Q12345",
  "mb_place_id": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
  "organization_ids": [3],
  "notes_md": "string",
  "attributes": { "wood_floor": true },
  "parking": "street",
  "floor_condition": "good",
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": 1748000000,
  "distance_km": 2.4
}
```

**Bulk/assignment bodies:**
```json
{ "organization_id": 3 }
```

```json
{ "location_id": 1, "organization_id": 3 }
```

```json
{ "ids": [1, 2], "organization_id": 3 }
```

```json
{ "keep_id": 1, "merge_id": 2 }
```

---

## Musicians

GET endpoints are public with optional auth. Write endpoints require authentication.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/musicians` | List musicians |
| POST | `/api/v1/musicians` | Create one or more musicians |
| GET | `/api/v1/musicians/{id}` | Get musician |
| PUT | `/api/v1/musicians/{id}` | Update musician |
| DELETE | `/api/v1/musicians/{id}` | Delete musician |

Content negotiation: `Accept: application/atom+xml` returns an Atom feed.

### Musician List Query Parameters

| Parameter | Description |
|-----------|-------------|
| `organization_id` | Musicians linked to published events of the given organization |
| `name` | Case-insensitive substring match on bandname |
| `mbid` | Exact MusicBrainz artist ID |
| `wikidata_id` | Exact Wikidata QID (e.g. `Q12345`) |
| `discogs_id` | Exact Discogs artist ID |
| `country` | Exact ISO country code (e.g. `DE`) |

**Musician object:**
```json
{
  "id": 1,
  "bandname": "La Troupe",
  "short_name": "LT",
  "internetsite": "https://latroupe.example.com",
  "description": "string",
  "mbid": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
  "wikidata_id": "Q12345",
  "discogs_id": "123456",
  "country": "FR",
  "begin_year": 2018,
  "biography": "string",
  "members_json": "[]",
  "albums_json": "[]",
  "mastodon": "string",
  "instagram": "string",
  "facebook": "string",
  "soundcloud": "string",
  "spotify": "string",
  "deezer": "string",
  "genre": "bal folk",
  "image_url": "/api/v1/musician-images/1",
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": 1748000000
}
```

---

## Dances and Tags

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/dances` | optional | List dances |
| POST | `/api/v1/dances` | bearer | Create dance |
| DELETE | `/api/v1/dances/{id}` | bearer | Delete dance |
| GET | `/api/v1/tags` | optional | List distinct event tags |

---

## Events

GET endpoints are public with optional auth. Write endpoints require authentication.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/events` | List events |
| POST | `/api/v1/events/preview` | Parse iCal or folkdance JSON without saving |
| POST | `/api/v1/events` | Create event(s) or import iCal |
| GET | `/api/v1/events/{id}` | Get event with locations, musicians, and timetable |
| PUT | `/api/v1/events/{id}` | Full update |
| DELETE | `/api/v1/events/{id}` | Delete event |
| POST | `/api/v1/events/{id}/publish` | Publish event |
| POST | `/api/v1/events/{id}/cancel` | Cancel event |
| POST | `/api/v1/events/{id}/clone` | Clone event as unpublished draft |
| POST | `/api/v1/events/{id}/assign-org` | Assign organization to unpublished event |
| POST | `/api/v1/events/{id}/timetable` | Add timetable entries |
| PUT | `/api/v1/events/{id}/timetable` | Replace timetable |

**Event object:**
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
  "has_festival": false,
  "workshop_difficulty": "beginner",
  "is_cancelled": false,
  "tags": ["bal-folk"],
  "is_published": true,
  "short_code": "8b911390",
  "url": "https://example.com/event/42",
  "source": "https://example.com/feed.ics",
  "image_url": "/api/v1/images/1",
  "organization_id": 3,
  "location_id": 7,
  "location": { "id": 7, "location": "Kulturzentrum", "town": "Berlin", "geohash": "u33db2p", "osm_id": 123456, "osm_type": "way" },
  "locations": [],
  "musicians": [{ "id": 1, "bandname": "La Troupe", "mbid": "...", "wikidata_id": "Q12345", "discogs_id": "123", "image_url": "..." }],
  "dance_names": ["Bourree"],
  "pricing": {
    "type": "multiple",
    "currency": "EUR",
    "prices": [
      { "label": "normal", "amount": 12 },
      { "label": "student", "amount": 8 }
    ]
  },
  "booking_url": "https://tickets.example.com",
  "availability": "limited",
  "tickets_total": 80,
  "booking_enabled": true,
  "food": "snacks",
  "drink": "bar",
  "attributes": { "family_friendly": true },
  "floor_condition": "good",
  "contact_name": "Alice",
  "contact_email": "alice@example.com",
  "timetable": [],
  "created_at": "2026-01-01T00:00:00Z"
}
```

`pricing.type` must be `free`, `donation`, `single`, or `multiple`.

Content negotiation: `Accept: text/calendar` returns iCalendar; `Accept: application/atom+xml` returns an Atom feed.

### Event List Query Parameters

**Text / date:**

| Parameter | Description |
|-----------|-------------|
| `title` | Partial title match |
| `description` | Partial description match |
| `start_time_after`, `start_time_before` | Start-time range (Unix epoch or RFC3339) |
| `end_time_after`, `end_time_before` | End-time range |
| `created_after` | Filter by creation time |
| `include_past` | `true` to include past events |
| `code` | Public short-code lookup |
| `limit`, `offset` | Pagination |

**Type / classification:**

| Parameter | Description |
|-----------|-------------|
| `type` | Comma-separated event types: `ball`, `workshop`, `festival` (OR semantics) |
| `has_ball`, `has_workshop`, `has_festival` | Individual boolean flags (legacy; prefer `type=`) |
| `tag` | Exact tag slug |
| `dance` | Exact dance name |
| `dance_id` | Dance ID (integer) |
| `difficulty` | Exact `workshop_difficulty` value (e.g. `beginner`) |
| `pricing` | Exact pricing type: `free`, `donation`, `single`, `multiple` |
| `wheelchair` | `1` — events at wheelchair-accessible locations |
| `bookable` | `1` — events with online booking enabled |
| `is_cancelled` | `1` — show cancelled events; `0` — exclude (default) |
| `is_published` | Boolean; authenticated callers only |

**Filtering by related entity:**

| Parameter | Description |
|-----------|-------------|
| `organization_id` | Events of the given organization |
| `location_id` | Events at the given location |
| `musician_id` | Events featuring the given musician |
| `location` | Partial location-name match |
| `country` | Exact country code |

**Geo / spatial:**

| Parameter | Description |
|-----------|-------------|
| `lat`, `lon`, `radius_km` | Radius filter (legacy param names; post-filtering) |
| `bbox` | Bounding box: `minLng,minLat,maxLng,maxLat` |
| `geohash` | Geohash prefix (any length); decoded to bounding box |

Unauthenticated callers only see published, non-suggestion events.

### Create or Update Event

`POST /api/v1/events` accepts one event object, an array of event objects, or `Content-Type: text/calendar` for iCal import. `PUT /api/v1/events/{id}` accepts a single event object.

**Example body:**
```json
{
  "uid": "optional-stable-id",
  "title": "Bal Folk",
  "description": "string",
  "start_time": "2026-05-15T20:00:00+02:00",
  "end_time": "2026-05-15T23:00:00+02:00",
  "has_ball": true,
  "has_workshop": false,
  "has_festival": false,
  "tags": ["bal-folk"],
  "organization_id": 3,
  "location_id": 7,
  "location": {
    "location": "Kulturzentrum",
    "address": "Hauptstr. 1",
    "zipcode": "10115",
    "town": "Berlin",
    "country": "Germany",
    "latitude": 52.52,
    "longitude": 13.4,
    "eventsite": "https://example.com/event/42"
  },
  "musicians": [1, 2],
  "dances": [1],
  "pricing": { "type": "single", "amount": 10, "currency": "EUR" },
  "booking_enabled": true,
  "tickets_total": 80
}
```

For event series, include a `date` array on create:

```json
{
  "title": "Weekly Dance",
  "has_ball": true,
  "organization_id": 3,
  "location": { "location": "Kulturzentrum" },
  "date": [
    { "description": "Week 1", "start_time": "2026-05-15T20:00:00+02:00", "end_time": "2026-05-15T23:00:00+02:00" },
    { "description": "Week 2", "start_time": "2026-05-22T20:00:00+02:00", "end_time": "2026-05-22T23:00:00+02:00" }
  ]
}
```

iCal imports map `UID`, `SUMMARY`, `DESCRIPTION`, `DTSTART`, `DTEND`, `DURATION`, `LOCATION`, `GEO`, `CATEGORIES`, `STATUS:CANCELLED`, `ORGANIZER`, image `ATTACH`, and `RRULE`. Optional query parameter `organization_id` assigns imported events to an organization.

**Timetable body:**
```json
[
  {
    "start_time": "20:00",
    "end_time": "21:30",
    "title": "Workshop",
    "description": "string",
    "room": "Hall A",
    "location_id": 7
  }
]
```

---

## Anonymous Event Suggestions

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/api/v1/events/suggest-preview` | public | Parse submitted iCal or folkdance JSON without saving |
| POST | `/api/v1/events/suggest` | public | Submit an event suggestion |
| GET | `/api/v1/events/suggest/verify/{token}` | public | Confirm an email-verified suggestion |

Suggestions are rate-limited and require email verification before publication workflow continues.

---

## iCal Feeds

No authentication required. The dansal-web frontend serves feed URLs — the REST API also supports content negotiation directly (see [Content Negotiation](#content-negotiation)).

| Feed URL | Format | Description |
|----------|--------|-------------|
| `/feed/events.ical` | iCal | All upcoming events |
| `/feed/events.json` | JSON | All upcoming events |
| `/feed/events.rss` | RSS 2.0 | All upcoming events |
| `/feed/org/{slug}/events.{format}` | iCal / JSON / RSS | One organisation's events |
| `/feed/location/{slug}/events.{format}` | iCal / JSON / RSS | One location's events |
| `/feed/musician/{slug}/events.{format}` | iCal / JSON / RSS | One musician's events |
| `/feed/ball/events.{format}` | iCal / JSON / RSS | Events tagged `bal-folk` |
| `/feed/workshop/events.{format}` | iCal / JSON / RSS | Events tagged `workshop` |
| `/feed/festival/events.{format}` | iCal / JSON / RSS | Events tagged `festival` |
| `/events/{id}.ics` | iCal | Single event download |

The REST API supports content negotiation on `GET /api/v1/events` and `GET /api/v1/events/{id}` for `text/calendar` (iCal) and `application/atom+xml` (Atom), with full query-parameter filtering support.

---

## Images

Event images are public with optional auth on read. Musician and organization image reads are public. Writes require authentication.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/images/{event_id}` | Get event image |
| POST | `/api/v1/images/{event_id}` | Upload event image |
| DELETE | `/api/v1/images/{event_id}` | Delete event image |
| GET | `/api/v1/musician-images/{id}` | Get musician image |
| POST | `/api/v1/musician-images/{id}` | Upload musician image |
| DELETE | `/api/v1/musician-images/{id}` | Delete musician image |
| GET | `/api/v1/org-images/{id}` | Get organization image |
| POST | `/api/v1/org-images/{id}` | Upload organization image |
| DELETE | `/api/v1/org-images/{id}` | Delete organization image |

Uploads accept common image formats. Stored images are normalized by the server image pipeline.

---

## Fetch Sources

Fetch sources store external calendar feeds and import events from them. All endpoints require authentication.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/fetchurl` | List sources |
| POST | `/api/v1/fetchurl` | Add or update one source and import |
| GET | `/api/v1/fetchurl/{id}` | Get source |
| PATCH | `/api/v1/fetchurl/{id}` | Update source metadata |
| DELETE | `/api/v1/fetchurl/{id}` | Delete source |
| POST | `/api/v1/fetchurl/{id}/fetch` | Re-import one source |
| POST | `/api/v1/fetchurl/bulk-delete` | Delete multiple sources |
| POST | `/api/v1/fetchurl/bulk-fetch` | Re-import multiple sources |
| POST | `/api/v1/fetchurl/bulk-assign-org` | Assign multiple sources to an organization |

**Fetch source object:**
```json
{
  "id": 1,
  "url": "https://example.com/calendar.ics",
  "type": "ical",
  "tags": ["bal-folk"],
  "organization_id": 3,
  "last_fetched_at": "2026-05-15T10:00:00Z",
  "created_at": "2026-01-01T00:00:00Z"
}
```

Supported types include `ical`, `folkdance-json`, RSS/Gancio-style sources where enabled by the importer, and auto-detection when type is omitted.

**Create body:**
```json
{
  "url": "https://example.com/calendar.ics",
  "type": "ical",
  "tags": ["bal-folk"],
  "organization": "My Dance Club",
  "organization_id": 3
}
```

**Bulk bodies use `ids` plus the relevant operation data:**
```json
{ "ids": [1, 2, 3] }
```

```json
{ "ids": [1, 2, 3], "organization_id": 3 }
```

---

## Contact Posts

Contact posts are public event-local board entries for ride shares, accommodation, and similar coordination.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/contact-posts` | public | List all contact posts |
| GET | `/api/v1/events/{id}/contact-posts` | public | List posts for one event |
| POST | `/api/v1/events/{id}/contact-posts` | optional | Create post |
| GET | `/api/v1/contact-posts/manage/{token}` | public | Get a post by management token |
| PATCH | `/api/v1/contact-posts/{id}` | token | Update using `?token={manage_token}` |
| DELETE | `/api/v1/contact-posts/token/{token}` | public | Delete using management token |
| DELETE | `/api/v1/contact-posts/{id}` | bearer | Delete as authorized user |
| POST | `/api/v1/contact-posts/{id}/contact` | optional | Contact poster |
| GET | `/api/v1/contact-requests/verify/{token}` | public | Verify a contact request |

**Create body:**
```json
{
  "type": "ride_offer",
  "city": "Berlin",
  "persons": 2,
  "message": "Leaving Friday afternoon",
  "nickname": "Alice",
  "email": "alice@example.com",
  "telegram": "@alice"
}
```

**Contact body:**
```json
{ "email": "bob@example.com", "telegram": "@bob", "message": "Can I join?" }
```

---

## Bookings

Public booking creation is available only for events with booking enabled. Management requires authentication and admin or event-organization membership.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/api/v1/events/{id}/bookings` | public | Create pending booking and send verification email |
| GET | `/api/v1/bookings/verify/{token}` | public | Verify booking and return QR token |
| GET | `/api/v1/events/{id}/bookings` | bearer | List bookings for an event |
| GET | `/api/v1/bookings/checkin/{qr_token}` | bearer | Check in a booking |
| PATCH | `/api/v1/bookings/{id}/status` | bearer | Set booking status |
| DELETE | `/api/v1/bookings/{id}` | bearer | Delete booking |

**Booking object:**
```json
{
  "id": 1,
  "event_id": 42,
  "name": "Alice",
  "email": "alice@example.com",
  "persons": 2,
  "message": "string",
  "status": "confirmed",
  "qr_token": "string",
  "created_at": "2026-01-01T00:00:00Z"
}
```

**Create body:**
```json
{ "name": "Alice", "email": "alice@example.com", "persons": 2, "message": "string" }
```

**Status body:**
```json
{ "status": "approved" }
```

Allowed management statuses are `approved` and `cancelled`.

---

## Telegram Webhook

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/telegram/webhook` | public | Telegram bot webhook |

---

## Status Codes

| Code | Meaning |
|------|---------|
| 200 | OK |
| 201 | Created |
| 202 | Accepted |
| 204 | No content |
| 400 | Bad request or invalid input |
| 401 | Missing or invalid credentials |
| 403 | Forbidden |
| 404 | Not found |
| 410 | Gone or expired token |
| 413 | Request entity too large |
| 415 | Unsupported media type |
| 422 | Unprocessable content |
| 429 | Rate limit exceeded |
| 500 | Internal server error |
| 502 | Bad gateway or upstream fetch failed |