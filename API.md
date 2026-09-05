# dansal API Reference

REST API served by the `dansal` binary. All endpoints are under the base URL configured as `server.base_url` in `config.yaml`.

For the `dansal_web` frontend's own routes (public pages, feeds, `/embed/*` widgets, **OSM tile proxy**), see [WEB.md](WEB.md).

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
- [Publishers](#publishers)
- [Dashboard](#dashboard)
- [Registration](#registration)
- [WebAuthn Credentials](#webauthn-credentials)
- [OIDC / SSO](#oidc--sso)
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
- OIDC/SSO login or invite redemption (`POST /api/v1/oidc/callback`) — see [OIDC / SSO](#oidc--sso)
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

Public. Returns enumerable field values for building dynamic UIs, keyed by field name. Most keys are arrays of `{"slug": ..., "label_key": ...}` — `slug` is the value stored on the record, `label_key` is the i18n key dansal's own admin UI uses to render it (clients may translate `label_key` themselves or fall back to `slug`):

- `food` — `sold`, `potluck`, `none`
- `drink` — `alcohol`, `soft`, `none`
- `floor_condition` — `parquet`, `stone`, `tiles`, `grass`, `sand`, `pavement`
- `parking` (locations only) — `none`, `free`, `paid`
- `workshop_difficulties` — `beginner`, `advanced`, `profi`
- `pricing_types` — `free`, `donation`, `single`, `multiple`
- `contact_post_types` — `ride_offer`, `ride_request`, `sleep_offer`, `sleep_request`, `ticket_offer`, `ticket_request`, `lost_item`, `found_item`
- `timetable_entry_types` — `bal`, `workshop`
- `price_labels` — suggested labels (`normal`, `reduced`, `presale`, `member`, `supporter`) for a `pricing.type = "multiple"` entry's `label` field. **Suggestions only** — `Price.Label` remains free text with no server-side validation against this list, since organizations have tiers this fixed set can't cover.
- `attributes`, `osm_types` — plain string arrays (no `label_key`, self-explanatory).

**Empty string (`""`) semantics** for `food`, `drink`, `floor_condition`, `parking`:
- `floor_condition` on an **event**: inherit from the venue's own `floor_condition`.
- All other fields, and `floor_condition`/`parking` on a **location**: not set.

`food`, `drink`, `floor_condition` (events and locations) and `parking` (locations) are validated server-side on write — `POST`/`PUT`/`PATCH` with a value outside the vocabulary (and not `""`) returns `400 Bad Request`.

### Discovering request body shape: `OPTIONS`

For write endpoints where the accepted JSON body isn't obvious from field names alone, send an `OPTIONS` request to the same path as the `POST`/`PUT`/`PATCH` endpoint to get a JSON schema of expected fields:

```
OPTIONS /api/v1/events
OPTIONS /api/v1/events/{id}
OPTIONS /api/v1/locations
OPTIONS /api/v1/locations/{id}
OPTIONS /api/v1/musicians
OPTIONS /api/v1/musicians/{id}
OPTIONS /api/v1/instructors
OPTIONS /api/v1/instructors/{id}
OPTIONS /api/v1/fetchurl
OPTIONS /api/v1/fetchurl/{id}
OPTIONS /api/v1/events/{id}/contact-posts
OPTIONS /api/v1/contact-posts/{id}
OPTIONS /api/v1/events/{id}/location
OPTIONS /api/v1/events/{id}/organization
```

Public — no auth required, since this describes shape, not data. Response shape:

```json
{
  "fields": {
    "title": {"type": "string", "required": true},
    "food": {"type": "string", "enum": ["sold", "potluck", "none"]},
    "musicians": {"type": "array", "items": "number"},
    "pricing": {"type": "object"}
  }
}
```

`required` is true when the field has no JSON `omitempty` tag. `enum` is present only on fields with a closed vocabulary. Not available for users, organizations, API keys, or publishers — those are account/credential-provisioning endpoints that stay admin-driven rather than a self-service integration target.

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
GET    /api/v1/users/{id}         # any authenticated caller; only the account owner gets full PII
PUT    /api/v1/users/{id}         # admin or self (see restrictions below)
DELETE /api/v1/users/me           # self-deletion (not available to admin accounts)
GET    /api/v1/users/{id}/organizations   # admin or self
GET    /api/v1/me/stats           # own event-authorship counts
POST   /api/v1/user/password      # change own password (empty new_password removes it; see OIDC / SSO)
POST   /api/v1/users/{id}/verify  # admin: send verification email
POST   /api/v1/users/{id}/magic-link   # admin: generate magic link for user
POST   /api/v1/users/{id}/telegram/message  # admin: send Telegram message to user
```

There is no `GET /api/v1/users/me` endpoint. Use `GET /api/v1/users/{id}` with your own user ID.

There is no user-creation or admin-driven password-reset endpoint, and no `DELETE /api/v1/users/{id}` for deleting another account — account lifecycle (creation, email/role changes by an admin, password resets, full deletion) is CLI-only (`dansal_admin`) or handled through the registration/invite flow. This is intentional (#613): the REST API only exposes self-service actions plus a few narrowly-scoped admin actions (verify, magic-link, Telegram message).

**`GET /api/v1/users` / `GET /api/v1/users/{id}` response shape:** callers who are not the account owner (or, for the list endpoint, not admin) receive a public subset only (`id`, `display_name`, `role`, `description`, `website`, `created_at` — no email, telegram handle, or verification flags).

**`user_metadata`:** All user records have an optional `user_metadata` JSON field (string containing a JSON object). For publisher accounts it stores client information set during [connect-link redemption](#connect-link-bootstrap-recommended-setup-flow) (e.g. `{"client_name":"wp-dansal @ example.com","client_url":"https://example.com"}`). The field is returned as part of the full user object for the account owner and admin; it is stripped from public/list responses.

**Update user request (`PUT`) — restrictions:**
```json
{
  "email": "new@example.com",
  "display_name": "Alice D.",
  "telegram_handle": "@alice",
  "role": "user"
}
```
- `email`: only the account owner may change their own email (clears `email_verified`); admins cannot change another user's email via this endpoint.
- `role`: only admin may set this field; publisher accounts can never change role.
- All other fields: owner or admin.

## Publishers

Publishers are service accounts (`role=publisher`) with no email/password/passkeys, authenticated purely via API key — the identity an external integration (e.g. the [wp-dansal](https://github.com/ademant/wp-dansal) WordPress plugin) uses to post events under an organization.

```
POST   /api/v1/publishers                        # create a publisher + API key atomically (admin/user)
POST   /api/v1/publishers/token                  # exchange an API key for a short-lived, IP-pinned session token
POST   /api/v1/publishers/{id}/regenerate-key     # rotate a publisher's API key (raw key, manual copy-paste)
POST   /api/v1/publishers/{id}/reconnect-invite   # mint a self-redeemable reconnect link (see below)
DELETE /api/v1/publishers/{id}                    # delete a publisher account
```

Admin may act on any publisher. A `user`-role caller may create a publisher for any organization they belong to, and may regenerate/delete/reconnect a publisher only if it shares at least one organization with the caller.

**Create request:**
```json
{
  "name": "Venue Feed Bot",
  "org_id": 7,
  "expires_at": "2027-01-01T00:00:00Z"
}
```

`expires_at` is optional (RFC3339) — omit it for a non-expiring key.

**Create response (`201`, API key shown only here):**
```json
{
  "user_id": 42,
  "name": "Venue Feed Bot",
  "key_id": 5,
  "api_key": "ak_...",
  "org_id": 7,
  "expires_at": "2027-01-01T00:00:00Z"
}
```

`regenerate-key` accepts an optional `{"expires_at": "..."}` body (same RFC3339 format) and returns `{"key_id": ..., "api_key": "ak_...", "expires_at": "..."}`, invalidating all previous keys for that publisher. The raw key is returned for manual copy-paste into the integration's settings — for a self-redeemable link instead, see `reconnect-invite` below.

Keys are stored hashed (SHA-256), never in plaintext — the raw value is only ever visible in the create/regenerate response.

**Token exchange (`POST /api/v1/publishers/token`):** authenticate with `Authorization: Bearer <api_key>` as usual; the response is a short-lived session token pinned to the caller's IP address:

```json
{
  "token": "...",
  "expires_at": "2026-07-03T01:00:00Z"
}
```

Default lifetime is `server.publisher_token_expiration_hours` (1 hour). Use the returned `token` as the bearer credential for subsequent requests instead of the API key itself — it's rejected if presented from any IP other than the one it was minted from, so a leaked token is useless off-network. Re-calling this endpoint (from a new IP, or just to refresh) immediately invalidates any previously-issued pinned token for that publisher. Restricted to `role=publisher` callers — admin/user accounts already have ordinary login and gain nothing from IP-pinning on a session that may legitimately move across networks.

### Connect-link bootstrap (recommended setup flow)

Instead of manually creating a publisher and copying the numeric org ID into a plugin's settings, org members can generate a single-use connect link from the `/admin/users` page. The link encodes a publisher invite that the plugin redeems in one call to receive all the credentials it needs.

```
POST /api/v1/invites/{token}/publisher
```

Public (no `Authorization` header required — the token is the credential). The `{token}` is the value from a `role=publisher` invite link; see [Invites](#invites) for how to generate one.

**Request body (all fields optional):**
```json
{
  "name": "wp-dansal @ example.com",
  "user_metadata": {
    "client_name": "wp-dansal @ example.com",
    "client_url": "https://example.com"
  }
}
```

`user_metadata` is a free-form JSON object stored on the publisher's user record and shown in the admin user list. Integrations should include at minimum `client_name` and, if applicable, `client_url` — this lets admins identify which external site each publisher account belongs to. Any JSON object is accepted; unknown keys are preserved and ignored by dansal.

**Response (`201`):**
```json
{
  "api_key": "ak_...",
  "user_id": 42,
  "org_id": 7,
  "org_name": "Bal Folk Berlin",
  "org_slug": "balfolkberlin",
  "base_url": "https://api.balfolk.jetzt",
  "expires_at": "2027-01-01T00:00:00Z"
}
```

The token is consumed on first use (single-use) and expires after `server.invite_publisher_expiry_minutes`. The `api_key` is shown only in this response — store it securely. `expires_at` is present only when the new key was actually given an expiry (invite-created keys have none by default) — record it if present so the integration can proactively renew via `POST /api/v1/apikeys/renew` (see [API Keys](#api-keys)) instead of only discovering an expiry via a failed request.

**End-to-end flow for a WordPress plugin:**

1. Org member opens `/admin/users` → clicks **Connect link** next to the org's publisher row (or creates a new one)
2. The page shows a one-time URL like `https://api.balfolk.jetzt/api/v1/invites/abc123/publisher`
3. Admin pastes the URL into the plugin's settings page
4. Plugin POSTs to the URL with `{"user_metadata": {"client_name": "wp-dansal @ example.com", "client_url": "https://example.com"}}`
5. Plugin stores the returned `api_key`, `org_id`, and `base_url` — setup complete, no manual ID lookup needed

**Reconnecting an existing publisher (`POST /api/v1/publishers/{id}/reconnect-invite`):** for when a publisher's key has already fully expired (past the renewal grace window below) and the original connect flow can't run again without creating a duplicate account. Authenticated (admin, or a caller sharing an org with the publisher) — this is what mints the link; redeeming the resulting token is still the same public `POST /api/v1/invites/{token}/publisher` call above. Response:

```json
{
  "token": "...",
  "redeem_url": "https://api.balfolk.jetzt/api/v1/invites/abc123/publisher",
  "expires_at": "2026-07-03T01:00:00Z"
}
```

Redeeming this token **rotates the target publisher's existing key in place** — old key invalidated, same `user_id`/`org_id`, no new account or org-membership row — instead of creating a new publisher, but the request/response shape at redemption time is identical to a fresh connect link, so the integration's redemption code needs no changes to handle it. On `/admin/users`, this is what the **Reconnect link** button on an existing publisher row generates.

### Building a third-party integration on a publisher account

This is the intended shape for any external integration that authenticates as a publisher.

1. **One key, one org, no OAuth.** Each publisher API key is scoped to exactly one `org_id` at creation time and never changes org. Store the key as the connection's rarely-used credential-exchange secret — rather than sending it on every request, call `POST /api/v1/publishers/token` to mint a short-lived IP-pinned session token (above) and use that for actual API calls. If the integration's IP changes, its pinned token simply stops validating; re-exchange the API key from the new IP for a fresh one. If the API key itself was created with an `expires_at`, call `POST /api/v1/apikeys/renew` (see [API Keys](#api-keys)) shortly before it expires — `POST /api/v1/apikeys/renew` also tolerates a short grace window past `expires_at` (`server.api_key_renew_grace_hours`, default 6h), so a renewal attempt that lands slightly late due to the integration's own polling cadence can still succeed; past that window a human has to reissue the key via `regenerate-key` or a reconnect link.
2. **Location sync — check before creating:**
   - `GET /api/v1/locations?osm_id=<id>&osm_type=<type>` — exact match
   - `GET /api/v1/locations?lat=<lat>&lng=<lng>&radius=<km>` — proximity match (adds `distance_km` to results)
   - If a match exists but isn't yet linked to the org: `POST /api/v1/locations/{id}/assign-org` with `{"organization_id": <org_id>}` (publishers may only assign orgs they belong to)
   - Otherwise: `POST /api/v1/locations` with `organization_ids: [<org_id>]`
   - Once assigned, `PATCH /api/v1/locations/{id}` edits it like any other org member
3. **Event sync — create or update, never duplicate:** `POST /api/v1/events` to create; `PATCH /api/v1/events/{id}` to update. The integration should persist the returned event `id` locally (e.g. WordPress post meta) so subsequent saves `PATCH` instead of `POST`. Publishers may only edit events they created themselves (`created_by_id`).
4. **Reading back for display:** `GET /api/v1/events?organization_id=<org_id>` returns the org's own events (published and, for the publisher's own org, unpublished) for embedding on the third-party site.

## Dashboard

```
GET    /api/v1/dashboard/attention
```

Authentication required (admin or user role). Returns scoped counts of items needing review, used to drive "needs attention" hints on the dansal-web dashboard:

```json
{
  "pending_registrations": 2,
  "pending_event_suggestions": 5,
  "possible_duplicates": 1
}
```

Admins get instance-wide counts. `user`-role callers get counts scoped to their organization(s), including events at locations linked to their org via `location_organizations` (covers shared venues).

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

A registration's email/telegram verification token is valid for **7 days** from submission (`GET /api/v1/register/verify/email/{token}` after that returns expired). To limit spam, at most `max_open_tokens_per_address` (default 5) unverified pending registrations may be open for the same email address at once — further submissions to that address return `429` until one is verified or expires.

**Invite-based registration** (bypasses pending queue): see [Invites](#invites) for the full endpoint list. For human accounts use `POST /api/v1/invites/{token}`; for publisher service accounts use `POST /api/v1/invites/{token}/publisher`.

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
POST   /api/v1/auth/webauthn/login/finish         # complete passkey login → returns session token or totp_required
POST   /api/v1/auth/webauthn/totp-challenge       # complete TOTP second factor after passkey login
```

All credential management endpoints require authentication. Login endpoints are public.

**Passkey login with TOTP:** when a user has both a passkey and TOTP enrolled, `login/finish` returns `200` with `{"totp_required": true, "pending_token": "..."}` instead of a session token. The client must then POST to `totp-challenge`:

```json
POST /api/v1/auth/webauthn/totp-challenge
{ "pending_token": "...", "totp_code": "123456" }
```

On success returns the same session token response as a normal login. The `pending_token` is single-use, expires after 5 minutes, and is invalid outside this endpoint.

## OIDC / SSO

```
GET    /api/v1/oidc/providers            # public: list enabled providers (?org_id=N); admin/user: management view
POST   /api/v1/oidc/providers            # admin: any provider; user: org-scoped only; publisher: none
DELETE /api/v1/oidc/providers/{id}       # admin: any provider; user: org-scoped only; publisher: none

POST   /api/v1/oidc/start                # begin login/invite-redemption flow
POST   /api/v1/oidc/callback             # complete the flow

POST   /api/v1/oidc/link-start                  # auth required: link a provider to the current account
GET    /api/v1/user/oidc-identities             # auth required: list caller's linked identities
DELETE /api/v1/user/oidc-identities/{id}        # auth required: unlink one
```

External SSO login and account-linking (#1095, #1096, #1097). Two provider `kind`s share the same registry:
- `oidc` (default) — spec-compliant OpenID Connect: discovery document + JWKS-verified ID token. Works with any real OIDC IdP (Google, Microsoft/Entra ID, GitLab, Discord's `openid` scope, self-hosted Keycloak, etc.).
- `mastodon` — Mastodon's plain OAuth2 API, chosen for its frictionless self-service client registration (`POST /api/v1/apps` on the instance — instant, no admin review). No discovery document or ID token; identity is instead resolved by calling `GET /api/v1/accounts/verify_credentials` on the instance with the access token.

Both kinds ultimately produce a stable `(issuer, subject)` pair stored in `user_identities` — everything downstream (invite redemption, login, linking, unlinking, the self-lockout guard) is identical regardless of which protocol produced it. For `oidc`, `issuer`/`subject` are the ID token's `iss`/`sub` claims; for `mastodon`, `issuer` is the instance base URL and `subject` is the account's stable numeric ID (never the `@username`, which can change).

**Zero-PII by design:** dansal only ever requests the minimal `openid`/`read:accounts` scope — no email or display name is collected from the provider, even when redeeming an invite through it. An account created via invite+SSO has `email=NULL`, `display_name=NULL`, and `password_hash=''`, the same shape already used for zero-PII passkey invite redemption.

### Provider registry

**`GET /api/v1/oidc/providers?org_id=N`** — public; returns enabled providers only (never `client_secret`). With no `org_id`, only instance-wide (`org_id: null`) providers; with `org_id`, instance-wide plus that org's own providers. A caller authenticated as admin gets every row instead, including disabled and org-scoped ones — used to populate `/admin/oidc-providers`. A caller authenticated as `user` and passing `?scope=mine` gets every row (enabled or not) for orgs they belong to, but never instance-wide rows — same page, org-scoped management view (#1099).

**`POST /api/v1/oidc/providers`** — admin: any provider, instance-wide or org-scoped. `user`: only an org-scoped provider (`org_id` required) for an org they're a member of — a missing `org_id` or an org they don't belong to returns `403`. `publisher`: `403`.
```json
{
  "kind": "oidc",
  "issuer_url": "https://accounts.example.com",
  "client_id": "...",
  "client_secret": "...",
  "display_name": "Example SSO",
  "org_id": 7,
  "enabled": true
}
```
`kind` is `"oidc"` (default) or `"mastodon"`. `org_id` omitted/null makes the provider instance-wide (admin only). On create, dansal validates the provider is reachable before accepting it: for `oidc` it runs OIDC discovery against `issuer_url`; for `mastodon` it calls `GET {issuer_url}/api/v1/instance`. A bad or unreachable URL returns `400` immediately rather than accepting a provider that would fail on every login attempt.

**Mastodon self-service app auto-registration (#1098):** for `kind: "mastodon"`, `client_id`/`client_secret` may both be omitted — dansal then registers a real OAuth app on the instance itself via `POST {issuer_url}/api/v1/apps` (no login/admin approval needed on Mastodon's side) and fills in the returned credentials automatically. It tries registering both the login/invite and account-linking callback URLs together first; if the instance rejects multiple `redirect_uris`, it retries with just the login/invite one and returns the provider with `"link_enabled": false` — `/settings`'s "Link" button is hidden for that provider rather than sending users into a redirect the instance will reject. If self-service registration fails entirely, the request returns `400` and the caller should retry with `client_id`/`client_secret` supplied manually (obtained however the admin prefers).

**`DELETE /api/v1/oidc/providers/{id}`** — admin: any provider. `user`: only a provider whose `org_id` is an org they're a member of (instance-wide or another org's provider returns `403`). `publisher`: `403`.

### Login / invite redemption flow

A two-step authorization-code+PKCE flow, mirroring the shape of the WebAuthn ceremony endpoints above.

**`POST /api/v1/oidc/start`**
```json
{
  "provider_id": 3,
  "redirect_uri": "https://your-app.example.com/oidc/3/callback",
  "invite_token": "..."
}
```
`invite_token` is optional — omit it for a plain returning-user login (the identity must already be linked, via a prior invite redemption or `link-start` below). Returns:
```json
{ "flow_id": "...", "authorize_url": "https://accounts.example.com/authorize?..." }
```
Redirect the browser to `authorize_url`. `flow_id` must be round-tripped to `callback` — store it, e.g., in a short-lived cookie scoped to the callback path.

**`POST /api/v1/oidc/callback`**
```json
{ "flow_id": "...", "code": "...", "state": "..." }
```
`code`/`state` come from the provider's redirect back to your `redirect_uri`. On success:
- If the flow carried an `invite_token`: creates a new zero-PII account and returns `201` with a session token (`{"token":...,"expires_at":...,"user_id":...,"email":"","role":"user"}` — the same flat shape as WebAuthn login).
- Otherwise: looks up the already-linked `(issuer, subject)` and returns `200` with a session token, or `404` if no account is linked to this identity yet.

Both endpoints are public but rate-limited per client IP, same limiter as password login.

### Linking/unlinking SSO on an existing account

**`POST /api/v1/oidc/link-start`** — authenticated. Same request/response shape as `POST /api/v1/oidc/start` (returns `{flow_id, authorize_url}`), but for a user who already has a password or passkey account and wants to add SSO as an additional login method — the flow is bound to the caller's own `user_id` instead of an invite token. Complete it the same way, via `POST /api/v1/oidc/callback` with the returned `flow_id`; on success it attaches the identity to the caller's account and returns `201` with `{"status":"linked"}` — no new session is issued, since the caller is already logged in. Returns `409` if that `(issuer, subject)` is already linked to a *different* account — one external identity links to exactly one dansal account.

**`GET /api/v1/user/oidc-identities`** — authenticated. Lists the caller's own linked identities:
```json
[{ "id": 12, "issuer_url": "https://accounts.example.com", "display_name": "Example SSO", "linked_at": "2026-08-13 10:00:00" }]
```

**`DELETE /api/v1/user/oidc-identities/{id}`** — authenticated. Unlinks one. `{id}` is the value from the list response above, not a provider ID. Rejected with `409` if it's the caller's last login method — the same self-lockout guard also applies to deleting a passkey (`DELETE /api/v1/user/webauthn/credentials/{id}`) and to clearing a password (below): all three require at least one other login method (password, passkey, or a linked identity) to remain.

**Removing a password:** `POST /api/v1/user/password` (see [Users](#users)) accepts an empty `new_password` to mean "remove my password entirely" — allowed only when a passkey or linked identity remains, guarded the same way.

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
DELETE /api/v1/apikeys/current   # self-revoke; see below
POST   /api/v1/apikeys/renew     # self-service rotation; see below
```

Authentication required (except `current` and `renew`, which authenticate via the key itself).

**Create request:**
```json
{
  "name": "My Integration",
  "expires_at": "2027-01-01T00:00:00Z"
}
```

`expires_at` is optional (RFC3339). There are no scopes — the key carries the same permissions as the user who created it.

The full key value (prefix `ak_`) is returned only once, at creation — keys are stored hashed (SHA-256) and cannot be retrieved again afterward.

Admins can also create keys for other users by passing `"user_id": <id>`.

**Self-revoke (`DELETE /api/v1/apikeys/current`):** for a headless integration (e.g. wp-dansal on uninstall/disconnect) to retire its own key without admin credentials, instead of leaving an orphaned live key behind. Send the key to be revoked as the bearer token:

```http
DELETE /api/v1/apikeys/current
Authorization: Bearer ak_<key-to-revoke>
```

`204 No Content` on success (the key is deleted immediately); `401` if the key is already invalid/deleted. Deliberately scoped to "current" (implicit from the auth header) rather than `DELETE /api/v1/apikeys/{id}`, so a key can only ever revoke itself, never a sibling key.

**Renewal (`POST /api/v1/apikeys/renew`):** for a headless integration (e.g. a publisher key held by a WordPress plugin) to rotate its own key without an admin/user session. Send the current key itself as the bearer token:

```http
POST /api/v1/apikeys/renew
Authorization: Bearer ak_<current-key>
```

Only keys with a non-null `expires_at` can be renewed — a key with no expiry returns `400`. A key past its `expires_at` can still be renewed within a grace window (`server.api_key_renew_grace_hours`, default 6h) to tolerate a client's own polling cadence landing slightly late; beyond that window it returns `401` and a human must reissue the key (via `regenerate-key`, or for a publisher, a reconnect link — see [Publishers](#publishers)). On success, the old key is invalidated immediately and a new key is returned with the same lifetime duration as the original, counted from now:

```json
{
  "key_id": 6,
  "api_key": "ak_...",
  "expires_at": "2026-08-01T00:00:00Z"
}
```

## Organizations

```
GET    /api/v1/organizations
GET    /api/v1/organizations/{id}
POST   /api/v1/organizations              # auth required
PUT    /api/v1/organizations/{id}         # member or admin — full replace
PATCH  /api/v1/organizations/{id}         # member or admin — partial merge
DELETE /api/v1/organizations/{id}         # admin only
GET    /api/v1/organizations/stats
GET    /api/v1/organizations/{id}/members
POST   /api/v1/organizations/{id}/members
DELETE /api/v1/organizations/{id}/members/{user_id}
GET    /api/v1/organizations/members?org_ids=1,2,3   # bulk membership lookup for several orgs
GET    /api/v1/organizations/check-actor-name  # check ActivityPub actor name availability
```

List and get are public. Create/update/delete require authentication. `GET /api/v1/organizations/members` requires authentication; non-admin callers only get results for orgs they belong to (others are silently omitted).

`PUT` replaces every editable field with the body's values. `PATCH` requires `Content-Type: application/merge-patch+json` (RFC 7396) and only changes fields present in the body. In both, `name`/`actor_name` may only be changed by admins — a non-admin org member's `name`/`actor_name` value in the body is silently ignored rather than rejected, matching the pre-existing `PUT` behavior. A `PATCH` request with any other `Content-Type` is rejected with `415 Unsupported Media Type`. There is no `OPTIONS` schema-discovery endpoint for organizations, matching the exclusion for users/apikeys/publishers (see Vocabulary section) — organization management is meant to stay inside dansal rather than become an external self-service surface.

## Locations

```
GET    /api/v1/locations
GET    /api/v1/locations/{id}
POST   /api/v1/locations              # auth required
PUT    /api/v1/locations/{id}         # auth required — full replace
PATCH  /api/v1/locations/{id}         # auth required — partial merge
DELETE /api/v1/locations/{id}         # auth required
POST   /api/v1/locations/merge        # admin: merge duplicate locations
POST   /api/v1/locations/bulk-assign-org
POST   /api/v1/locations/unassign-org
POST   /api/v1/locations/{id}/assign-org  # admin/user (member of the target org)/publisher (member of the target org)
GET    /api/v1/locations/event-counts # auth required

GET    /api/v1/locations/{id}/children            # list a location's rooms (child locations)
POST   /api/v1/locations/{id}/children            # auth required — create a room under this location
```

List and get are public. Locations support `Accept: application/geo+json` on the list endpoint.

**`PUT` vs `PATCH`:** `PUT` replaces the entire location — send the complete object; any field omitted from the body is cleared to its zero value. `PATCH` requires `Content-Type: application/merge-patch+json` (RFC 7396) and only changes fields present in the body — an omitted key leaves the existing value unchanged, an explicit `""` clears a plain text field. Array/map fields (`organization_ids`, `attributes`, `aliases`) are replaced wholesale when present in a `PATCH` body, never merged element-by-element. A `PATCH` request with any other `Content-Type` is rejected with `415 Unsupported Media Type`.

**Rooms are child locations.** A room (e.g. "Grand Hall", "Studio 2") is a normal `locations` row with `parent_id` set to its building's location `id` — not a separate entity type. This means a room automatically gets everything a location already has: its own `organization_ids` (a room can be assigned to a different org than its building), `aliases`, its own `/location/{id}` page, and its own event dedup. A location can only be a parent OR a child, never both — `parent_id` must reference a top-level location (one with `parent_id: null`); a room cannot itself have children.

A room's `address`/`zipcode`/`town`/`country`/`latitude`/`longitude` are **inherited from its parent at read time**, not copied at creation — leave them unset when creating a room. This keeps a room's address in sync automatically if the building's address is later corrected, rather than drifting out of sync with a stale copy. Only fields that legitimately vary per room — `floor_condition`, `no_street_shoes`, `attributes`, `notes_md`, `capacity`, `size_sqm` — are set directly on the child.

`capacity` (integer, max people) and `size_sqm` (integer, floor area in m²) are optional, purely informational fields available on any location (building or room) — there is no enforcement (no booking/capacity checks), just a display hint for organizers picking a venue or room. Like the other per-room fields above, they are never inherited from a parent.

`GET /api/v1/locations` and `GET /api/v1/locations/{id}` embed a top-level location's rooms as `"children": [...]` (full `Location` objects, omitted when there are none) — `GET .../children` returns the same list on its own. `POST .../children` accepts `name` (required), `floor_condition`, `no_street_shoes`, `attributes`, `capacity`, `size_sqm`; the caller must be an admin or a member of one of the parent location's organizations. To delete a room, `DELETE /api/v1/locations/{id}` on the room's own `id` — no separate sub-resource endpoint, since a room is just a location.

An event's `location_id` points directly at whichever level was chosen — the building, or a specific room within it — there is no separate `room_id` field. Deleting a room whose `id` is referenced by existing events requires `?reassign_to=` like any other location delete (see `DELETE /api/v1/locations/{id}` above).

**`GET /api/v1/locations/event-counts`** returns `{"location_id": count}` for every location with at least one event. The count is **future events only** (`end_time` in the future, any publish status) — it intentionally matches the location dashboard's default (future-only) view rather than the location's lifetime event total.

**Site plan (#877).** A top-level location can have a site-plan image (floor plan/map showing where its rooms are), with each room positioned on it via `plan_x`/`plan_y` (floats 0–1, percentage position on the *parent's* site-plan image — only meaningful on a room, set via the standard `PATCH /api/v1/locations/{id}` merge-patch with just those two fields). The image itself uses a separate small endpoint group, mirroring the org/musician image pattern:

```
POST   /api/v1/locations/{id}/site-plan     # multipart upload, field name "image"; 400 if {id} is itself a room
DELETE /api/v1/locations/{id}/site-plan
GET    /api/v1/location-images/{id}         # serves the image; public, no auth
```

`Location.site_plan_url` is set (to `/api/v1/location-images/{id}`) when a top-level location has an uploaded site-plan image; omitted otherwise. Same auth rules as editing the location itself (admin or member of one of its organizations).

```json
POST /api/v1/locations/42/children
{ "name": "Grand Hall", "floor_condition": "parquet" }
```

**`attributes` (map of string → bool, tri-state):** free-form venue attributes, e.g. `{"wheelchair": true, "toilet": false}`. Key *absent* means unspecified/unknown (not "no") — a venue can be wheelchair-accessible without that ever having been explicitly recorded; only an explicit `true`/`false` asserts a known fact. The dansal-web admin UI's known keys are `wheelchair`, `hearing_loop`, `visual_support`, `bar`, `kitchen`, `toilet`, but the API itself doesn't validate against a fixed key list — any string key is accepted and stored.

**Query parameters for GET /api/v1/locations:**
- `name=` — substring match on location name
- `town=` — substring match on town
- `country=` — comma-separated 2-letter country codes
- `org_id=` — only locations assigned to this org
- `osm_id=` + `osm_type=` — exact match on OSM place (both required together; used to check for an existing location before creating a duplicate)
- `lat=` + `lng=` + `radius=` (km) — proximity search; adds `distance_km` to each result
- `bbox=minLng,minLat,maxLng,maxLat` — bounding-box search
- `with_event_counts=true` — adds future/past published event counts per location

## Musicians and Instructors

```
GET    /api/v1/musicians
GET    /api/v1/musicians/{id}
POST   /api/v1/musicians              # auth required
PUT    /api/v1/musicians/{id}         # auth required — full replace
PATCH  /api/v1/musicians/{id}         # auth required — partial merge
DELETE /api/v1/musicians/{id}         # auth required

GET    /api/v1/instructors
GET    /api/v1/instructors/{id}
GET    /api/v1/events/{id}/instructors
POST   /api/v1/instructors            # auth required
PUT    /api/v1/instructors/{id}       # auth required — full replace
PATCH  /api/v1/instructors/{id}       # auth required — partial merge
DELETE /api/v1/instructors/{id}       # auth required
PUT    /api/v1/events/{id}/instructors  # set instructors for an event
```

Musicians are performers linked to events; instructors are teachers linked to workshop slots. Both have an `email` field (contact address, shown admin-only on the public dansal-web profile pages) — note this is a frontend display decision only: the API itself returns `email` to any caller, public or authenticated, on both the single and list endpoints.

`PATCH` requires `Content-Type: application/merge-patch+json` (RFC 7396) and only changes fields present in the body — an omitted key leaves the existing value unchanged, an explicit `""` clears a plain text field. A `PATCH` request with any other `Content-Type` is rejected with `415 Unsupported Media Type`. Instructor edit permissions are unchanged between `PUT` and `PATCH`: admins may edit any instructor, other users only ones they created.

**Query parameters for GET /api/v1/musicians:**
- `name=` — substring match on bandname
- `organization_id=N` — musicians linked to events of this org
- `country=` — filter by country code
- `mbid=` — MusicBrainz ID exact match
- `wikidata_id=` — Wikidata ID exact match
- `discogs_id=` — Discogs ID exact match
- `with_event_counts=true` — adds `future_event_count` and `past_event_count` to each result

**Query parameters for GET /api/v1/instructors:**
- `name=` — substring match on name
- `organization_id=N` — instructors linked to (published) events of this org
- `with_event_counts=true` — adds `future_event_count` and `past_event_count` to each result

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
PUT    /api/v1/events/{id}        # auth required — full replace
PATCH  /api/v1/events/{id}        # auth required — partial update (merge patch)
DELETE /api/v1/events/{id}        # auth required
POST   /api/v1/events/{id}/publish
POST   /api/v1/events/{id}/cancel
POST   /api/v1/events/{id}/clone            # admin/user: duplicate an event, optionally into another org
POST   /api/v1/events/{id}/assign-org       # admin/user (member of the target org)/publisher
POST   /api/v1/events/{id}/enrich           # admin/publisher: attach musicians/pricing from an external lookup
POST   /api/v1/events/{id}/remove-from-series  # admin/user (member of the event's org)
POST   /api/v1/events/preview               # admin/user: preview-parse a feed without saving (multipart form)
POST   /api/v1/events/bulk-set-attributes   # admin/user: bulk-apply org/tags/dances/musicians/instructors/amenities to event IDs
POST   /api/v1/events/bulk-set-location     # admin/user: bulk-reassign event IDs to a location
POST   /api/v1/events/bulk-set-time         # admin/user: bulk-update start/end time-of-day for event IDs

PUT    /api/v1/events/{id}/location                    # set the event's location
DELETE /api/v1/events/{id}/location                     # clear the event's location
PUT    /api/v1/events/{id}/organization                 # set the event's organization
DELETE /api/v1/events/{id}/organization                 # clear the event's organization
PUT    /api/v1/events/{id}/musicians/{musician_id}      # add one musician
DELETE /api/v1/events/{id}/musicians/{musician_id}      # remove one musician
PUT    /api/v1/events/{id}/instructors/{instructor_id}  # add one instructor
DELETE /api/v1/events/{id}/instructors/{instructor_id}  # remove one instructor
PUT    /api/v1/events/{id}/dances/{dance_id}            # add one dance
DELETE /api/v1/events/{id}/dances/{dance_id}            # remove one dance
```

A room is just a [location](#locations) with `parent_id` set — to assign an event to a specific room rather than the whole venue, set `Event.location_id` directly to the room's `id` instead of the building's. There is no separate `room_id` field.

**Relationship sub-resources:** the eight endpoints above are additive, REST-idiomatic alternatives to embedding `location_id`/`organization_id`/`musicians[]`/`instructors[]`/`dances[]` in the event write body — they don't replace that behavior. In particular, `PUT`/`DELETE .../location` and `.../organization` are the way to *clear* those two nullable references via the API: `PATCH`'s merge-patch semantics can't distinguish "omitted" from "explicitly cleared" for a plain `*int` field (see the `PUT` vs `PATCH` note below), so clearing `location_id`/`organization_id` requires either a full `PUT` on the event or one of these `DELETE` sub-resource calls. All eight require the caller to be an admin or an org member of the event (same check as `PATCH`/`PUT` on the event itself); setting `.../organization` additionally requires membership in the *target* organization. `musicians`/`instructors`/`dances` sub-resource calls are single-item add/remove on top of the existing whole-list `PUT /api/v1/events/{id}/instructors` and the `musicians`/`dances` arrays in the event write body — adding an already-linked ID, or removing one that isn't linked, is a no-op (`204`), not an error.

**`bulk-set-time` request:** adjusts the time-of-day component of `start_time` and/or `end_time` for multiple events while keeping their dates. Times are in `"HH:MM"` format (Berlin timezone). At least one of `start_time` or `end_time` is required; omit the other to leave it unchanged. Events where the caller is not an org member are silently skipped (admins may affect all events). Returns `204`.

```json
{ "ids": [1, 2, 3], "start_time": "19:00", "end_time": "23:00" }
```

**`PUT` vs `PATCH`:** `PUT` replaces the entire event — send the complete object; any field omitted from the body is cleared to its zero value, and `location` may be a full nested object (find-or-create by name/address). `PATCH` requires `Content-Type: application/merge-patch+json` (RFC 7396) and only changes fields present in the body — an omitted key leaves the existing value unchanged, an explicit `""` clears a plain text field. Array fields (`tags`, `musicians`, `instructors`, `dances`) are replaced wholesale when present in a `PATCH` body, never merged element-by-element; `has_ball`/`has_workshop`/`has_festival` *(deprecated — use `tags` instead; see below)* still auto-derive their associated tags for backward compatibility whenever either the booleans or `tags` are part of the patch. `PATCH` has no nested `location` object — repoint an event at an existing location via `location_id`, or use `PUT` to also create/update the location itself. A `PATCH` request with any other `Content-Type` is rejected with `415 Unsupported Media Type`.

> **Deprecated: `has_ball`, `has_workshop`, `has_festival` (tracked in issue #871).** These boolean convenience fields predate the tag system and remain accepted on write for backward compatibility, but `tags` is the authoritative field on both read and write. Prefer tags for new integrations:
> - `has_ball=true` → `"tags": ["bal-folk"]` or `"tags": ["fest-noz"]`
> - `has_workshop=true` → `"tags": ["workshop"]`, `"dance-workshop"`, `"musician-workshop"`, or `"music-course"`
> - `has_festival=true` → `"tags": ["festival"]`
>
> The `type=ball,workshop,festival` query filter is deprecated for the same reason — use `tag=bal-folk`, `tag=workshop`, `tag=festival`, etc. instead. Both the booleans and the `type` filter continue to work but will be removed in a future release.

**Query parameters for GET /api/v1/events:**

Time range (all timestamps are Unix epoch integers):
- `include_past=true` — include events whose `end_time` is in the past (default: future only)
- `start_time_after=N` — events whose `start_time > N`
- `start_time_before=N` — events whose `start_time < N`
- `end_time_after=N` — events whose `end_time > N`
- `end_time_before=N` — events whose `end_time < N`

Content filters:
- `title=` — substring match on title
- `description=` — substring match on description
- `type=ball,workshop,festival` — comma-separated; matches events with any of the named types *(deprecated — use `tag=bal-folk`, `tag=workshop`, `tag=festival`, etc.)*
- `tag=` — filter by tag slug (single value)
- `dance=` — filter by dance name
- `dance_id=N` — filter by dance ID
- `difficulty=` — workshop difficulty level
- `pricing=free` — free events only
- `is_cancelled=1` — only cancelled events
- `musician_id=N` — events featuring this musician
- `instructor_id=N` — events featuring this instructor
- `country=` — comma-separated ISO 3166-1 alpha-2 codes

Location filters:
- `location=` — substring match on location name
- `location_id=N` — exact location
- `lat=` + `lon=` + `radius_km=` — proximity search (bounding-box approximation)
- `bbox=minLng,minLat,maxLng,maxLat` — bounding-box geo filter
- `geohash=` — geohash prefix filter

Structural filters:
- `organization_id=N` — filter by organization
- `series_id=N` — filter by event series
- `source=` — filter by import source URL (admin/user only)
- `created_after=` — created at or after this datetime string
- `wheelchair=1` — wheelchair-accessible events
- `bookable=1` — events with booking enabled

Auth-only filters:
- `is_published=true|false` — filter by publish status (admin/user/publisher); publishers can only list their org's unpublished events
- `include_past=true` — available to all callers but only authenticated callers can combine it with `is_published=false`

Pagination:
- `limit=N` — max results (default 100, max 1000)
- `offset=N` — pagination offset

The response includes an `X-Total-Count` header with the total matching row count before pagination.

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
GET    /api/v1/series/{id}/events           # list events belonging to this series
PUT    /api/v1/series/{id}/events/{event_id}    # add one event to the series
DELETE /api/v1/series/{id}/events/{event_id}    # remove one event from the series
GET    /api/v1/series-by-token/{token}
PATCH  /api/v1/series-by-token/{token}/events/{eventID}
```

Authentication required (except `GET /api/v1/series-by-token/{token}` and the PATCH for external organizer access).

**Ownership:** a series is owned by exactly one of `organization_id`, `musician_id`, or `instructor_id` (all nullable `int`, mutually exclusive in practice though not enforced as such). Musician/instructor ownership exists for teachers or performers who run their own recurring event (e.g. an independent dance course, or a musician booking their own gigs) without going through an organization. Non-admin callers must own whichever of the three fields is present on the request — `POST`/`PUT` independently validates every non-nil owner field (org membership via `isOrgMember`, musician/instructor ownership via `created_by_id`), and at least one owner field is required for non-admin callers. Admins may set/change any of the three. `GET /api/v1/series` accepts `org_id=`, `musician_id=`, or `instructor_id=` to filter by owner; non-admin callers additionally always see series they own via any of the three regardless of these filters. Events generated by `add-date`/series creation are tagged into `event_musicians`/`event_instructors` (in addition to the event's own `organization_id`) when the series has that owner type, so they show up in the owning musician's/instructor's event list.

The `.../events/{event_id}` pair is the single-item counterpart to `assign-events`/`remove-from-series` (#727): `PUT` adds one event to the series (rejecting, with `409 Conflict`, an event whose `organization_id` doesn't match the series' organization — `assign-events` instead skips mismatches silently when bulk-assigning); `DELETE` removes one event, equivalent to `POST /api/v1/events/{id}/remove-from-series` but addressed from the series side.

**Cadence (`cadence`, string, optional):** a single free-text, human-readable description of how often the series recurs (e.g. `"every 2nd + 4th Thursday, except holidays"`), shown to visitors on the event page and the org page's recurring-events list. Disclosure-only — there is no RRULE engine and it never generates occurrences; instances are still created/edited/cancelled individually via `add-date`/`assign-events`/deleting an event. It's denormalized onto every `GET /api/v1/events` response for that series as `series_cadence` (empty string when unset or the event has no series), so clients don't need a separate series lookup to display it.

## Timetable

```
POST   /api/v1/events/{id}/timetable    # append entries to the existing timetable
PUT    /api/v1/events/{id}/timetable    # replace the entire timetable
```

Authentication required (admin or a member of the event's organization). Multi-slot workshop/festival schedules.

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

**`category_filter` (array of strings, optional, iCal sources only):** when set, only VEVENTs whose `CATEGORIES` intersect the filter (case-insensitively) are imported — an empty/unset filter imports everything, same as before. Useful for a shared feed that mixes multiple event types (e.g. import only `CATEGORIES:Balfolk` entries from a venue's general events calendar) without needing separate title-keyword heuristics.

## Contact Posts

```
GET    /api/v1/contact-posts                    # all posts (auth required)
GET    /api/v1/events/{id}/contact-posts        # posts for one event (public)
POST   /api/v1/events/{id}/contact-posts        # create post (public, verification required)
GET    /api/v1/contact-posts/manage/{token}     # get post by management token
PUT    /api/v1/contact-posts/{id}               # full replace (management token)
PATCH  /api/v1/contact-posts/{id}               # partial merge (management token)
DELETE /api/v1/contact-posts/{id}               # delete (admin)
DELETE /api/v1/contact-posts/token/{token}      # delete by management token
POST   /api/v1/contact-posts/{id}/contact       # contact the poster (public)
GET    /api/v1/contact-requests/verify/{token}  # verify a contact request
```

Public posts require email or Telegram verification before appearing.

**Authorization for `PUT`/`PATCH` is different from every other resource in this API**: board posts have no user account backing them, so instead of `auth()`/Bearer, both are gated by the `?token={manage_token}` query parameter issued at creation time (the same token used by `DELETE .../token/{token}`) — an admin Bearer token does not grant access to these two endpoints. `PUT` (`Content-Type: application/json`) replaces `type`, `city`, `persons`, `message`, `nickname` wholesale — any field omitted from the body is cleared to its zero value. `PATCH` requires `Content-Type: application/merge-patch+json` (RFC 7396, 415 otherwise) and only changes fields present in the body. Neither endpoint can change `email`/`telegram`, which stay fixed to the value used at creation for verification purposes.

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
GET    /api/v1/invites                          # list own invites; admin sees all
POST   /api/v1/invites                          # create invite (admin or org member)
DELETE /api/v1/invites/{token}                  # revoke unused invite (creator or admin)
GET    /api/v1/invites/{token}                  # get invite info (public — for registration page)
POST   /api/v1/invites/{token}                  # redeem invite — register a human user account
POST   /api/v1/invites/{token}/publisher        # redeem invite — create a publisher service account
POST   /api/v1/invites/{token}/webauthn/begin   # passkey invite registration begin
POST   /api/v1/invites/{token}/webauthn/finish  # passkey invite registration finish
GET    /api/v1/pending-invites                  # list sent invites that haven't been used
POST   /api/v1/pending-invites/{id}/resend
```

**Create request:**
```json
{
  "type": "link",
  "role": "user",
  "org_id": 7
}
```

`type` is `"link"` (default) or `"qr"` (short-lived, for in-person QR scanning). `role` may be `"user"` (default) or `"publisher"` — admin invites are created via the sysadmin-only CLI/socket path, never through this endpoint. `org_id` is required; non-admin callers must belong to the specified org.

**Redemption for human users (`POST /api/v1/invites/{token}`):** creates a `role=user` account with optional email/password/passkey. See [Registration](#registration).

**Redemption for publisher accounts (`POST /api/v1/invites/{token}/publisher`):** creates a `role=publisher` service account + API key atomically, with optional `user_metadata`. See [Connect-link bootstrap](#connect-link-bootstrap-recommended-setup-flow) in the Publishers section for the full flow and response shape. An invite minted via `POST /api/v1/publishers/{id}/reconnect-invite` instead redeems through this same endpoint but rotates that existing publisher's key in place rather than creating a new account — see the same section.

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

### Request body size limit

All request bodies are capped at `server.max_body_bytes` (default 1MB). Exceeding it returns `413` with a message suggesting the payload be reduced.

For bulk-create endpoints that accept a JSON array (`POST /api/v1/events`, `/locations`, `/musicians` — see their sections below), this 1MB cap is the practical constraint on batch size, not server processing time: a benchmark of event insertion against the production schema showed ~800 events/sec, so the 1MB body limit is reached (roughly 1,000–2,000 events for typical payloads, more for minimal ones) well before any request-processing timeout would be. **Recommended safe batch size: up to ~1,000 items per request** for typical payloads; split larger imports into multiple requests rather than relying on a single very large array.

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
