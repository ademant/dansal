# dansal API Reference

REST API served by the `dansal` binary. All endpoints are under the base URL configured as `server.base_url` in `config.yaml`.

## Table of Contents

- [Base URL](#base-url)
- [Authentication](#authentication)
- [Roles and Permissions](#roles-and-permissions)
- [Content Negotiation](#content-negotiation)
- [Info and Health](#info-and-health)
- [Vocabulary](#vocabulary)
- [Authentication Endpoints](#authentication-endpoints)
- [Sessions](#sessions)
- [Users](#users)
- [Registration](#registration)
- [WebAuthn Credentials](#webauthn-credentials)
- [TOTP](#totp)
- [API Keys](#api-keys)
- [Organizations](#organizations)
- [Locations](#locations)
- [Musicians and Instructors](#musicians-and-instructors)
- [Tags and Dances](#tags-and-dances)
- [Events](#events)
- [Event Series](#event-series)
- [Timetable](#timetable)
- [Images](#images)
- [Fetch Sources](#fetch-sources)
- [Contact Posts](#contact-posts)
- [Bookings](#bookings)
- [Anonymous Event Suggestions](#anonymous-event-suggestions)
- [Invites](#invites)
- [Telegram Webhook](#telegram-webhook)
- [Status Codes](#status-codes)

## Base URL

```
http://localhost:8000
```

Production example:
```
https://api.dansal.example.com
```

## Authentication

Protected endpoints require a bearer token:

```http
Authorization: Bearer <token-or-api-key>
```

Tokens are issued by:
- `POST /api/v1/login` (email + password)
- `GET /api/v1/login/magic/{token}` (magic link)
- WebAuthn login (`POST /api/v1/auth/webauthn/login/finish`)
- `POST /api/v1/cert-login` (mTLS client certificate)

**API keys:** Begin with `ak_`. Created via `POST /api/v1/apikeys`. Optional expiration via `expires_at`; no scopes.

**Session tokens:** Expire after `server.token_expiration_hours` (default: 24 hours).

**Public endpoints** return published data only. Authenticated requests may return additional fields (e.g., unpublished events for the owner's organization).

## Roles and Permissions

| Role | Permissions |
|------|-------------|
| `admin` | Full access; bypasses organization membership checks |
| `publisher` | Read + create/publish events within allowed organizations |
| `user` | Read + write for their own organizations only |

There are exactly three roles. Unauthenticated access is equivalent to read-only on published data.

## Content Negotiation

Several GET endpoints support alternative output formats via the `Accept` header:

| `Accept` Header | Format |
|-----------------|--------|
| `application/json` | JSON (default) |
| `text/calendar` | iCalendar (RFC 5545) — events endpoints |
| `application/atom+xml` | Atom feed (RFC 4287) — events, musicians, locations, organizations |
| `application/geo+json` | GeoJSON (RFC 7946) — locations endpoint |

```bash
# Get events as iCalendar
curl -H "Accept: text/calendar" http://localhost:8000/api/v1/events

# Get locations as GeoJSON
curl -H "Accept: application/geo+json" http://localhost:8000/api/v1/locations
```

## Info and Health

```
GET /api/v1/info
GET /api/v1/health
```

Both are public. `/api/v1/info` returns server version and build metadata. `/api/v1/health` returns 200 OK when the server is running.

## Vocabulary

```
GET /api/v1/vocabulary
```

Public. Returns enumerable field values (event types, tag categories, countries, etc.) for building dynamic UIs.

## Authentication Endpoints

### Login

```
POST /api/v1/login
GET  /api/v1/login      (form-based, used by dansal-web)
```

**Request:**
```json
{
  "email": "user@example.com",
  "password": "your_password"
}
```

The `username` field is accepted as an alias for `email` for backward compatibility. You may also log in with `display_name` when the account has a password.

**Response:**
```json
{
  "token": "<token>",
  "expires_at": "<RFC3339>",
  "role": "user"
}
```

### Logout

```
DELETE /api/v1/login
```

Authentication required. Revokes the current session token. Returns `204 No Content`.

### Magic Link

```
POST /api/v1/login/magic
```

Request body: `{ "email": "user@example.com" }`. Sends a one-time login link to the address if it exists. Returns `204 No Content`.

```
GET /api/v1/login/magic/{token}
```

Consumes the magic link and returns a session token (same format as login).

### Certificate Login

```
POST /api/v1/cert-login
```

Used by dansal-webmin for mTLS client certificate authentication.

## Sessions

```
GET    /api/v1/sessions
DELETE /api/v1/sessions/{id}
```

Authentication required. List or revoke active sessions for the current user.

## Users

```
GET    /api/v1/users              # admin only
POST   /api/v1/users              # admin only
GET    /api/v1/users/{id}         # admin or self
PUT    /api/v1/users/{id}         # admin or self
DELETE /api/v1/users/{id}         # admin only
DELETE /api/v1/users/me           # self-deletion
GET    /api/v1/users/{id}/organizations
POST   /api/v1/user/password      # change own password
POST   /api/v1/users/{id}/verify  # admin: send verification email
POST   /api/v1/users/{id}/magic-link   # admin: generate magic link for user
POST   /api/v1/users/{id}/password     # admin: set user password
POST   /api/v1/users/{id}/telegram/message  # admin: send Telegram message to user
```

There is no `GET /api/v1/users/me` endpoint. Use `GET /api/v1/users/{id}` with your own user ID.

**Create user request (admin):**
```json
{
  "email": "user@example.com",
  "display_name": "Alice",
  "role": "publisher"
}
```

**Update user request (`PUT`):**
```json
{
  "email": "new@example.com",
  "display_name": "Alice D.",
  "telegram_handle": "@alice"
}
```

## Registration

Self-registration creates a **pending registration** that an admin must approve. There is no instant account creation via the API.

```
POST   /api/v1/register                         # submit registration request
GET    /api/v1/register/status/{id}             # check pending status
POST   /api/v1/register/resend/{token}          # resend verification email
DELETE /api/v1/register/{token}                 # cancel a pending registration
GET    /api/v1/register/verify/email/{token}    # confirm email address

POST   /api/v1/register/passkey/begin           # passkey-based registration begin
POST   /api/v1/register/passkey/finish          # passkey-based registration finish

GET    /api/v1/pending-registrations            # admin: list pending
GET    /api/v1/pending-registrations/count      # admin: count pending
POST   /api/v1/pending-registrations/{id}/approve  # admin: approve
DELETE /api/v1/pending-registrations/{id}       # admin: reject
```

**Invite-based registration** (bypasses pending queue):
```
GET    /api/v1/invites/{token}                  # get invite info
POST   /api/v1/invites/{token}                  # register with invite
POST   /api/v1/invites/{token}/webauthn/begin   # passkey invite begin
POST   /api/v1/invites/{token}/webauthn/finish  # passkey invite finish
```

**Email verification** (after registration or email change):
```
GET    /api/v1/verify/{token}
```

## WebAuthn Credentials

```
GET    /api/v1/user/webauthn/credentials          # list credentials for current user
POST   /api/v1/user/webauthn/register/begin       # begin adding a new passkey
POST   /api/v1/user/webauthn/register/finish      # complete adding a new passkey
DELETE /api/v1/user/webauthn/credentials/{id}     # remove a passkey

POST   /api/v1/auth/webauthn/login/begin          # begin passkey login (discoverable or with email)
POST   /api/v1/auth/webauthn/login/finish         # complete passkey login → returns session token
```

All credential management endpoints require authentication. Login endpoints are public.

## TOTP

```
GET    /api/v1/auth/totp/setup    # generate TOTP QR code / secret
POST   /api/v1/auth/totp/confirm  # confirm and enable TOTP
DELETE /api/v1/auth/totp          # disable TOTP
```

Authentication required.

## API Keys

```
GET    /api/v1/apikeys
POST   /api/v1/apikeys
DELETE /api/v1/apikeys/{id}
```

Authentication required.

**Create request:**
```json
{
  "name": "My Integration",
  "expires_at": "2027-01-01T00:00:00Z"
}
```

`expires_at` is optional (RFC3339). There are no scopes — the key carries the same permissions as the user who created it.

The full key value (prefix `ak_`) is returned only at creation.

Admins can also create keys for other users by passing `"user_id": <id>`.

## Organizations

```
GET    /api/v1/organizations
GET    /api/v1/organizations/{id}
POST   /api/v1/organizations              # auth required
PUT    /api/v1/organizations/{id}         # member or admin
DELETE /api/v1/organizations/{id}         # admin only
GET    /api/v1/organizations/stats
GET    /api/v1/organizations/{id}/members
POST   /api/v1/organizations/{id}/members
DELETE /api/v1/organizations/{id}/members/{user_id}
GET    /api/v1/organizations/check-actor-name  # check ActivityPub actor name availability
```

List and get are public. Create/update/delete require authentication.

## Locations

```
GET    /api/v1/locations
GET    /api/v1/locations/{id}
POST   /api/v1/locations              # auth required
PATCH  /api/v1/locations/{id}         # auth required
DELETE /api/v1/locations/{id}         # auth required
POST   /api/v1/locations/merge        # admin: merge duplicate locations
POST   /api/v1/locations/bulk-assign-org
POST   /api/v1/locations/unassign-org
POST   /api/v1/locations/{id}/assign-org
GET    /api/v1/locations/event-counts # auth required
```

List and get are public. Locations support `Accept: application/geo+json` on the list endpoint.

## Musicians and Instructors

```
GET    /api/v1/musicians
GET    /api/v1/musicians/{id}
POST   /api/v1/musicians              # auth required
PUT    /api/v1/musicians/{id}         # auth required
DELETE /api/v1/musicians/{id}         # auth required

GET    /api/v1/instructors
GET    /api/v1/instructors/{id}
GET    /api/v1/events/{id}/instructors
POST   /api/v1/instructors            # auth required
PUT    /api/v1/instructors/{id}       # auth required
DELETE /api/v1/instructors/{id}       # auth required
PUT    /api/v1/events/{id}/instructors  # set instructors for an event
```

Musicians are performers linked to events; instructors are teachers linked to workshop slots.

## Tags and Dances

```
GET    /api/v1/tags
GET    /api/v1/dances
POST   /api/v1/dances   # admin only
DELETE /api/v1/dances/{id}  # admin only
```

Both list endpoints are public. Tags have three categories: `format`, `level`, `type`. See the vocabulary endpoint for valid slugs.

## Events

```
GET    /api/v1/events
GET    /api/v1/events/{id}
POST   /api/v1/events             # auth required
PATCH  /api/v1/events/{id}        # auth required
DELETE /api/v1/events/{id}        # auth required
POST   /api/v1/events/{id}/publish
POST   /api/v1/events/{id}/cancel
```

**Query parameters for GET /api/v1/events:**
- `start=YYYY-MM-DD` — filter from date
- `end=YYYY-MM-DD` — filter to date
- `location_id=N` — filter by location
- `organization_id=N` — filter by organization
- `status=published|draft|cancelled`
- `limit=N` — max results (default varies)

List and get are public for published events. Authenticated users see their organization's draft events too.

Events support `Accept: text/calendar` for iCalendar and `Accept: application/atom+xml` for Atom output on both list and get endpoints.

**Create/update request fields include:**
```json
{
  "title": "Summer Ball",
  "start_time": "2026-06-20T19:00:00Z",
  "end_time": "2026-06-21T02:00:00Z",
  "location_id": 42,
  "organization_id": 7,
  "tags": ["bal-folk", "beginners"],
  "musicians": [1, 2],
  "booking_url": "https://tickets.example.com"
}
```

IDs are integers throughout the API, not strings.

## Event Series

```
GET    /api/v1/series
GET    /api/v1/series/{id}
POST   /api/v1/series
PUT    /api/v1/series/{id}
DELETE /api/v1/series/{id}
POST   /api/v1/series/{id}/add-date
POST   /api/v1/series/{id}/descriptions
POST   /api/v1/series/{id}/assign-events
POST   /api/v1/series/{id}/token/regenerate
POST   /api/v1/series/{id}/token/revoke
GET    /api/v1/series/token/{token}
PATCH  /api/v1/series/token/{token}/events/{eventID}
```

Authentication required (except `GET /api/v1/series/token/{token}` and the PATCH for external organizer access).

## Timetable

```
PUT    /api/v1/events/{id}/timetable
```

Authentication required. Replaces the timetable for an event (multi-slot workshop/festival schedules).

## Images

```
GET    /api/v1/images/{event_id}
POST   /api/v1/images/{event_id}    # upload event image (multipart/form-data)
DELETE /api/v1/images/{event_id}    # auth required

GET    /api/v1/musician-images/{id}
POST   /api/v1/musician-images/{id} # auth required
DELETE /api/v1/musician-images/{id} # auth required

GET    /api/v1/org-images/{id}
POST   /api/v1/org-images/{id}      # auth required
DELETE /api/v1/org-images/{id}      # auth required
```

Images are stored as AVIF (or JPEG fallback) and resized on upload to fit within 1024×1024 pixels. Served directly via `http.ServeFile`.

## Fetch Sources

```
GET    /api/v1/fetchurl
GET    /api/v1/fetchurl/{id}
POST   /api/v1/fetchurl
PATCH  /api/v1/fetchurl/{id}
DELETE /api/v1/fetchurl/{id}
POST   /api/v1/fetchurl/{id}/fetch       # trigger a single fetch
POST   /api/v1/fetchurl/bulk-fetch       # trigger fetch for multiple IDs
POST   /api/v1/fetchurl/bulk-delete
POST   /api/v1/fetchurl/bulk-assign-org
```

Authentication required. Fetch sources are iCal/JSON/RSS feeds imported automatically by the `dansal-fetch` timer.

**Note:** The path is `/api/v1/fetchurl` (no trailing `s`).

## Contact Posts

```
GET    /api/v1/contact-posts                    # all posts (auth required)
GET    /api/v1/events/{id}/contact-posts        # posts for one event (public)
POST   /api/v1/events/{id}/contact-posts        # create post (public, verification required)
GET    /api/v1/contact-posts/manage/{token}     # get post by management token
PATCH  /api/v1/contact-posts/{id}               # update (management token or admin)
DELETE /api/v1/contact-posts/{id}               # delete (admin)
DELETE /api/v1/contact-posts/token/{token}      # delete by management token
POST   /api/v1/contact-posts/{id}/contact       # contact the poster (public)
GET    /api/v1/contact-requests/verify/{token}  # verify a contact request
```

Public posts require email or Telegram verification before appearing.

## Bookings

```
POST   /api/v1/events/{id}/bookings         # create booking (public)
GET    /api/v1/bookings/verify/{token}      # verify booking via email link
GET    /api/v1/events/{id}/bookings         # list bookings for event (auth required)
PATCH  /api/v1/bookings/{id}/status         # update status (auth required)
GET    /api/v1/bookings/checkin/{qr_token}  # check in via QR code (auth required)
DELETE /api/v1/bookings/{id}                # auth required
```

## Anonymous Event Suggestions

```
POST   /api/v1/events/suggest-preview       # preview a suggestion before submitting
POST   /api/v1/events/suggest               # submit a suggestion
GET    /api/v1/events/suggest/verify/{token}  # verify suggestion via email link
```

Public. Suggestions are routed to admins via Telegram or email (configured in `web.yaml`). The preview endpoint validates the suggestion and resolves the location without saving anything.

## Invites

```
GET    /api/v1/invites          # list invites (admin)
POST   /api/v1/invites          # create invite (admin)
DELETE /api/v1/invites/{token}  # revoke invite (admin)
GET    /api/v1/pending-invites  # list sent invites that haven't been used
POST   /api/v1/pending-invites/{id}/resend
```

## Telegram Webhook

```
POST /telegram/webhook
```

Public (Telegram calls directly). Optional validation via `telegram_webhook_secret` in `web.yaml`.

## Status Codes

| Code | Meaning |
|------|---------|
| 200 | OK |
| 201 | Created |
| 202 | Accepted |
| 204 | No content |
| 400 | Bad request or invalid input |
| 401 | Missing or invalid credentials |
| 403 | Forbidden (e.g., account disabled, insufficient role) |
| 404 | Not found |
| 409 | Conflict (e.g., duplicate display name) |
| 410 | Gone or expired token |
| 413 | Request entity too large |
| 429 | Rate limit exceeded |
| 500 | Internal server error |

## Usage Examples

### Login and create an event

```bash
# Login
TOKEN=$(curl -s -X POST \
  -H "Content-Type: application/json" \
  -d '{"email":"alice@example.com","password":"secret"}' \
  http://localhost:8000/api/v1/login | jq -r '.token')

# Create an event
curl -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"title":"Summer Ball","start_time":"2026-06-20T19:00:00Z","end_time":"2026-06-21T02:00:00Z","location_id":42,"organization_id":7}' \
  http://localhost:8000/api/v1/events

# Publish it
curl -X POST \
  -H "Authorization: Bearer $TOKEN" \
  http://localhost:8000/api/v1/events/123/publish
```

### Get events as iCalendar

```bash
curl -H "Accept: text/calendar" http://localhost:8000/api/v1/events > events.ics
```

### Get locations as GeoJSON

```bash
curl -H "Accept: application/geo+json" http://localhost:8000/api/v1/locations
```

---

**Found an issue?** Report bugs on [GitHub Issues](https://github.com/ademant/dansal/issues)
