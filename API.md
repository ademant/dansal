# 📡 dansal API Reference

REST-style calendar API backed by SQLite. Comprehensive reference for developers integrating with dansal.

## 📋 Table of Contents

- [🌐 Base URL](#-base-url)
- [🔐 Authentication](#-authentication)
- [👥 Roles & Permissions](#-roles--permissions)
- [📦 Content Negotiation](#-content-negotiation)
- [ℹ️ Info Endpoint](#-info-endpoint)
- [📚 Vocabulary](#-vocabulary)
- [🔑 Authentication Endpoints](#-authentication-endpoints)
- [🔄 Sessions](#-sessions)
- [👤 Users](#-users)
- [📝 Registration & Verification](#-registration--verification)
- [🔑 WebAuthn Credentials](#-webauthn-credentials)
- [🗝️ API Keys](#-api-keys)
- [🏢 Organizations](#-organizations)
- [📍 Locations](#-locations)
- [🎻 Musicians](#-musicians)
- [💃 Dances & Tags](#-dances--tags)
- [🎭 Events](#-events)
- [📬 Anonymous Suggestions](#-anonymous-suggestions)
- [📅 iCal Feeds](#-ical-feeds)
- [🖼️ Images](#-images)
- [🔗 Fetch Sources](#-fetch-sources)
- [📞 Contact Posts](#-contact-posts)
- [🎟️ Bookings](#-bookings)
- [🤖 Telegram Webhook](#-telegram-webhook)
- [❓ Status Codes](#-status-codes)

## 🌐 Base URL

```text
http://localhost:8000
```

**Production example:**
```text
https://api.dansal.example.com
```

## 🔐 Authentication

Protected endpoints require a bearer token from:
- `POST /api/v1/login` (username/password)
- `GET /api/v1/login/magic/{token}` (magic link)
- WebAuthn login
- mTLS certificate login
- API key from `POST /api/v1/apikeys`

```http
Authorization: Bearer <token-or-api-key>
```

**Authentication Types:**
- **API keys**: Begin with `ak_`, no expiration
- **Session tokens**: Expire after configured duration (default: 30 days)
- **Public endpoints**: May accept optional bearer token for user-specific fields

## 👥 Roles & Permissions

| Role | Permissions |
|------|-------------|
| `admin` | Full access; bypasses organization checks |
| `user` | Read + write; must be organization member for org-scoped writes |
| `publisher` | Read + create/publish/cancel within allowed organization scope |
| `viewer` | Read published data only |

**Permission Examples:**
- `admin`: Can delete any event
- `user`: Can only edit events for their own organizations
- `publisher`: Can publish events but not manage users
- `viewer`: Can see unpublished events but not modify anything

## 📦 Content Negotiation

Several GET endpoints support alternative output formats via `Accept` header.

| `Accept` Header | Format | Supported Endpoints |
|-----------------|--------|---------------------|
| `application/json` | JSON (default) | All endpoints |
| `text/calendar` | iCalendar (RFC 5545) | Events endpoints |
| `application/atom+xml` | Atom feed (RFC 4287) | Events, musicians, locations, organizations |
| `application/geo+json` | GeoJSON (RFC 7946) | Locations endpoints |

**Examples:**
```bash
# Get events as iCalendar
curl -H "Accept: text/calendar" /api/v1/events

# Get locations as GeoJSON
curl -H "Accept: application/geo+json" /api/v1/locations
```

## ℹ️ Info Endpoint

Get server version and build information.

**Endpoint:**
```
GET /api/v1/info
```

**Authentication:** Public

**Response:**
```json
{
  "version": "1.2.3",
  "build_time": "2026-05-15T10:00:00Z",
  "api_version": "v1"
}
```

## 📚 Vocabulary

Get enumerable field values used across the API.

**Endpoint:**
```
GET /api/v1/vocabulary
```

**Authentication:** Public

**Use Case:** Build dynamic filter UIs without hardcoding strings.

**Response:**
```json
{
  "event_types": ["ball", "workshop", "festival", "combination"],
  "difficulties": ["beginner", "advanced", "pro"],
  "countries": ["DE", "AT", "CH", "FR", "IT"],
  "tags": ["folk", "tango", "salsa", "swing", "balfolk"]
}
```

## 🔑 Authentication Endpoints

### Login

```
POST /api/v1/login
```

**Request:**
```json
{
  "username": "your_username",
  "password": "your_password"
}
```

**Response:**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "expires_at": "2026-01-01T00:00:00Z",
  "user": {
    "id": "user_id",
    "username": "your_username",
    "role": "user"
  }
}
```

### Logout

```
POST /api/v1/logout
```

**Authentication:** Required
**Response:** `204 No Content`

### Magic Link Login

```
GET /api/v1/login/magic/{token}
```

**Response:** Same as login endpoint

### Password Reset

```
POST /api/v1/password/reset
```

**Request:**
```json
{
  "email": "user@example.com"
}
```

**Response:** `204 No Content` (email sent if user exists)

## 🔄 Sessions

### List Sessions

```
GET /api/v1/sessions
```

**Authentication:** Required
**Response:** Array of active sessions

### Revoke Session

```
DELETE /api/v1/sessions/{id}
```

**Authentication:** Required
**Response:** `204 No Content`

## 👤 Users

### Get Current User

```
GET /api/v1/users/me
```

**Authentication:** Required

**Response:**
```json
{
  "id": "user_id",
  "username": "alice",
  "email": "alice@example.com",
  "role": "user",
  "created_at": "2026-01-01T00:00:00Z",
  "last_login": "2026-01-02T10:00:00Z",
  "organizations": [{"id": "org1", "name": "Dance Club"}]
}
```

### List Users (Admin)

```
GET /api/v1/users
```

**Authentication:** Admin required
**Query Parameters:** `?role=user&disabled=false`

### Get User (Admin)

```
GET /api/v1/users/{id}
```

**Authentication:** Admin required

### Create User (Admin)

```
POST /api/v1/users
```

**Authentication:** Admin required

**Request:**
```json
{
  "username": "bob",
  "email": "bob@example.com",
  "password": "secure123",
  "role": "user"
}
```

### Update User

```
PATCH /api/v1/users/{id}
```

**Authentication:** Required (self or admin)

**Request:**
```json
{
  "email": "new-email@example.com",
  "telegram_handle": "@bob_dancer"
}
```

## 📝 Registration & Verification

### Register (Invite Required)

```
POST /api/v1/register
```

**Request:**
```json
{
  "invite_token": "abc123",
  "username": "newuser",
  "email": "new@example.com",
  "password": "secure123"
}
```

### Request Email Verification

```
POST /api/v1/verify/email/request
```

**Response:** `204 No Content` (email sent)

### Verify Email

```
POST /api/v1/verify/email
```

**Request:**
```json
{
  "token": "verification_token"
}
```

## 🔑 WebAuthn Credentials

### List Credentials

```
GET /api/v1/webauthn/credentials
```

**Authentication:** Required

### Register Credential

```
POST /api/v1/webauthn/credentials/register
```

**Request:** WebAuthn registration response

### Authenticate

```
POST /api/v1/webauthn/login
```

**Request:** WebAuthn authentication response

## 🗝️ API Keys

### Create API Key

```
POST /api/v1/apikeys
```

**Authentication:** Required

**Request:**
```json
{
  "name": "My App",
  "scopes": ["events:read", "events:write"]
}
```

**Response:**
```json
{
  "id": "ak_abc123",
  "name": "My App",
  "created_at": "2026-01-01T00:00:00Z",
  "scopes": ["events:read", "events:write"]
}
```

**Note:** The full key is only shown once at creation!

### List API Keys

```
GET /api/v1/apikeys
```

**Authentication:** Required

### Revoke API Key

```
DELETE /api/v1/apikeys/{id}
```

**Authentication:** Required

## 🏢 Organizations

### List Organizations

```
GET /api/v1/organizations
```

**Authentication:** Public (limited fields) or required (full access)

**Query Parameters:**
- `?name=Dance` - Filter by name
- `?country=DE` - Filter by country
- `?limit=50&page=1` - Pagination

**Response:**
```json
[
  {
    "id": "org1",
    "name": "Folk Dance Club",
    "description": "Traditional dance organization",
    "website": "https://folk.example.com",
    "email": "info@folk.example.com"
  }
]
```

### Get Organization

```
GET /api/v1/organizations/{id}
```

**Authentication:** Public (limited) or required (full)

### Create Organization

```
POST /api/v1/organizations
```

**Authentication:** Required

**Request:**
```json
{
  "name": "New Dance Group",
  "description": "Modern dance collective",
  "website": "https://newdance.example.com",
  "mastodon": "@newdance",
  "email": "contact@newdance.example.com"
}
```

### Update Organization

```
PATCH /api/v1/organizations/{id}
```

**Authentication:** Required (member or admin)

### Delete Organization

```
DELETE /api/v1/organizations/{id}
```

**Authentication:** Admin required

## 📍 Locations

### List Locations

```
GET /api/v1/locations
```

**Authentication:** Public

**Query Parameters:**
- `?country=DE` - Filter by country
- `?town=Berlin` - Filter by town
- `?has_geo=true` - Only locations with coordinates
- `?organization=org1` - Filter by organization

**Response:**
```json
[
  {
    "id": "loc1",
    "name": "Dance Hall Berlin",
    "address": "Main Street 1",
    "postcode": "10115",
    "town": "Berlin",
    "country": "DE",
    "latitude": 52.5200,
    "longitude": 13.4050,
    "website": "https://dancehall.example.com"
  }
]
```

### Get Location

```
GET /api/v1/locations/{id}
```

**Authentication:** Public

### Create Location

```
POST /api/v1/locations
```

**Authentication:** Required

**Request:**
```json
{
  "name": "New Venue",
  "short_name": "NV",
  "address": "Dance Street 42",
  "postcode": "12345",
  "town": "Dance City",
  "country": "DE",
  "latitude": 52.5200,
  "longitude": 13.4050,
  "website": "https://newvenue.example.com",
  "organization_id": "org1",
  "accessibility": {
    "wheelchair": true,
    "parking": "free",
    "floor_surface": "wood"
  }
}
```

### Update Location

```
PATCH /api/v1/locations/{id}
```

**Authentication:** Required

### Delete Location

```
DELETE /api/v1/locations/{id}
```

**Authentication:** Required

## 🎻 Musicians

### List Musicians

```
GET /api/v1/musicians
```

**Authentication:** Public

**Query Parameters:**
- `?name=Band` - Filter by name
- `?musicbrainz=id` - Filter by MusicBrainz ID

**Response:**
```json
[
  {
    "id": "mus1",
    "name": "The Folk Band",
    "description": "Traditional folk music",
    "musicbrainz_id": "abc123",
    "mastodon": "@folkband",
    "website": "https://folkband.example.com"
  }
]
```

### Get Musician

```
GET /api/v1/musicians/{id}
```

**Authentication:** Public

### Create Musician

```
POST /api/v1/musicians
```

**Authentication:** Required

**Request:**
```json
{
  "name": "New Band",
  "description": "Experimental folk music",
  "musicbrainz_id": "def456",
  "mastodon": "@newband",
  "instagram": "@newband_official"
}
```

### Update Musician

```
PATCH /api/v1/musicians/{id}
```

**Authentication:** Required

### Delete Musician

```
DELETE /api/v1/musicians/{id}
```

**Authentication:** Required

## 💃 Dances & Tags

### List Dances

```
GET /api/v1/dances
```

**Authentication:** Public

**Response:** Array of dance types

### List Tags

```
GET /api/v1/tags
```

**Authentication:** Public

**Response:** Array of available tags

## 🎭 Events

### List Events

```
GET /api/v1/events
```

**Authentication:** Public (published) or required (all)

**Query Parameters:**
- `?start=2026-01-01` - Filter by start date
- `?end=2026-12-31` - Filter by end date
- `?location=loc1` - Filter by location
- `?organization=org1` - Filter by organization
- `?type=ball` - Filter by event type
- `?published=true` - Only published events
- `?limit=50&page=1` - Pagination

**Response:**
```json
[
  {
    "id": "event1",
    "title": "Summer Ball",
    "description": "Annual summer dance event",
    "start_time": "2026-06-20T19:00:00Z",
    "end_time": "2026-06-21T02:00:00Z",
    "location_id": "loc1",
    "organization_id": "org1",
    "type": "ball",
    "status": "published",
    "is_public": true,
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-02T10:00:00Z"
  }
]
```

### Get Event

```
GET /api/v1/events/{id}
```

**Authentication:** Public (published) or required (all)

**Response:** Full event details including timetable, pricing, musicians

### Create Event

```
POST /api/v1/events
```

**Authentication:** Required

**Request:**
```json
{
  "title": "New Event",
  "description": "Description here",
  "start_time": "2026-12-25T19:00:00Z",
  "end_time": "2026-12-26T02:00:00Z",
  "location_id": "loc1",
  "organization_id": "org1",
  "type": "ball",
  "difficulty": "beginner",
  "is_public": true,
  "pricing": [
    {"name": "Early Bird", "amount": 15.00, "currency": "EUR"},
    {"name": "Door", "amount": 20.00, "currency": "EUR"}
  ],
  "musicians": ["mus1", "mus2"],
  "tags": ["folk", "traditional"]
}
```

### Update Event

```
PATCH /api/v1/events/{id}
```

**Authentication:** Required

### Publish Event

```
POST /api/v1/events/{id}/publish
```

**Authentication:** Required
**Response:** `204 No Content`

### Cancel Event

```
POST /api/v1/events/{id}/cancel
```

**Authentication:** Required
**Response:** `204 No Content`

### Delete Event

```
DELETE /api/v1/events/{id}
```

**Authentication:** Required

## 📬 Anonymous Event Suggestions

### Submit Suggestion

```
POST /api/v1/suggestions
```

**Authentication:** Public

**Request:**
```json
{
  "title": "Suggested Event",
  "description": "Event details",
  "start_time": "2026-12-25T19:00:00Z",
  "location": "Venue Name, City",
  "contact": "suggester@example.com"
}
```

**Response:** `204 No Content`

## 📅 iCal Feeds

### Get Events as iCalendar

```
GET /api/v1/events.ics
```

**Authentication:** Public
**Accept Header:** `text/calendar`

### Get Single Event as iCalendar

```
GET /api/v1/events/{id}.ics
```

**Authentication:** Public
**Accept Header:** `text/calendar`

## 🖼️ Images

### Upload Image

```
POST /api/v1/images
```

**Authentication:** Required
**Content-Type:** `multipart/form-data`

**Request:** Form with `file` field

**Response:**
```json
{
  "id": "img1",
  "url": "/images/img1.jpg",
  "thumbnail_url": "/images/img1_thumb.jpg",
  "width": 800,
  "height": 600
}
```

### Get Image

```
GET /api/v1/images/{id}
```

**Authentication:** Public

## 🔗 Fetch Sources

### List Fetch Sources

```
GET /api/v1/fetchurls
```

**Authentication:** Required

### Get Fetch Source

```
GET /api/v1/fetchurls/{id}
```

**Authentication:** Required

### Create Fetch Source

```
POST /api/v1/fetchurls
```

**Authentication:** Required

**Request:**
```json
{
  "url": "https://example.com/events.ics",
  "type": "ical",
  "organization_id": "org1",
  "tags": ["imported"]
}
```

### Update Fetch Source

```
PATCH /api/v1/fetchurls/{id}
```

**Authentication:** Required

### Delete Fetch Source

```
DELETE /api/v1/fetchurls/{id}
```

**Authentication:** Required

### Trigger Fetch

```
POST /api/v1/fetchurls/{id}/fetch
```

**Authentication:** Required
**Response:** `202 Accepted`

## 📞 Contact Posts

### List Contact Posts

```
GET /api/v1/contactposts
```

**Authentication:** Required (for event)

**Query Parameters:**
- `?event_id=event1` - Filter by event
- `?status=confirmed` - Filter by status

### Get Contact Post

```
GET /api/v1/contactposts/{id}
```

**Authentication:** Required

### Create Contact Post

```
POST /api/v1/contactposts
```

**Authentication:** Public (email verification required)

**Request:**
```json
{
  "event_id": "event1",
  "name": "Alice",
  "email": "alice@example.com",
  "category": "ride",
  "message": "Offering ride from Berlin",
  "persons": 3
}
```

### Update Contact Post

```
PATCH /api/v1/contactposts/{id}
```

**Authentication:** Required (owner or admin)

### Delete Contact Post

```
DELETE /api/v1/contactposts/{id}
```

**Authentication:** Required (owner or admin)

## 🎟️ Bookings

### List Bookings

```
GET /api/v1/bookings
```

**Authentication:** Required (admin or event organizer)

**Query Parameters:**
- `?event_id=event1` - Filter by event
- `?status=confirmed` - Filter by status

**Response:**
```json
[
  {
    "id": "booking1",
    "event_id": "event1",
    "name": "Alice",
    "email": "alice@example.com",
    "persons": 2,
    "message": "Vegetarian meals needed",
    "status": "confirmed",
    "qr_token": "ABC123",
    "created_at": "2026-01-01T00:00:00Z"
  }
]
```

### Get Booking

```
GET /api/v1/bookings/{id}
```

**Authentication:** Required

### Create Booking

```
POST /api/v1/bookings
```

**Authentication:** Public

**Request:**
```json
{
  "event_id": "event1",
  "name": "Alice",
  "email": "alice@example.com",
  "persons": 2,
  "message": "Vegetarian meals needed"
}
```

### Update Booking Status

```
PATCH /api/v1/bookings/{id}/status
```

**Authentication:** Required (admin or event organizer)

**Request:**
```json
{
  "status": "approved"
}
```

## 🤖 Telegram Webhook

### Webhook Endpoint

```
POST /telegram/webhook
```

**Authentication:** Public (Telegram calls directly)
**Content-Type:** `application/json`

**Request:** Telegram webhook payload
**Response:** `200 OK` on success

## ❓ Status Codes

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

## 📖 API Usage Examples

### Complete Event Creation Flow

```bash
# 1. Login
TOKEN=$(curl -s -X POST \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"secret"}' \
  http://localhost:8000/api/v1/login | jq -r '.token')

# 2. Create event
curl -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"title":"Summer Ball","start_time":"2026-06-20T19:00:00Z","end_time":"2026-06-21T02:00:00Z","location_id":"loc1","organization_id":"org1","type":"ball"}' \
  http://localhost:8000/api/v1/events

# 3. Publish event
EVENT_ID=$(curl -s -X POST ... | jq -r '.id')
curl -X POST \
  -H "Authorization: Bearer $TOKEN" \
  http://localhost:8000/api/v1/events/$EVENT_ID/publish
```

### Working with iCal

```bash
# Get events as iCalendar
curl -H "Accept: text/calendar" \
  http://localhost:8000/api/v1/events > events.ics

# Import into calendar application
# Or use with icalBuddy, etc.
```

### Using WebSockets

```javascript
// Connect to notifications websocket
const socket = new WebSocket('ws://localhost:8000/ws/notifications');

socket.onmessage = (event) => {
  const data = JSON.parse(event.data);
  console.log('New notification:', data);
};

// Authenticate
socket.send(JSON.stringify({
  type: 'auth',
  token: 'your_token_here'
}));
```

## 🔒 Security Notes

- Always use HTTPS in production
- Store tokens securely (not in client-side code)
- Rotate API keys regularly
- Use shortest possible token expiration times
- Implement proper error handling

## 📚 Additional Resources

- **[Developer Guide](DEVELOPER_GUIDE.md)** - Architecture and development
- **[Admin Guide](ADMIN_GUIDE.md)** - Deployment and configuration
- **[User Guide](USER_GUIDE.md)** - Using the platform
- **OpenAPI/Swagger**: Future planned addition

---

**Found an issue?** Report bugs on [GitHub Issues](https://github.com/ademant/dansal/issues)

**Need help?** Ask questions on [GitHub Discussions](https://github.com/ademant/dansal/discussions)