# Compliance Audit — dansal

| | |
|---|---|
| **Author** | ademant (AI-assisted, opencode/big-pickle) |
| **Date** | 2026-10-06 |
| **Reviewed commit** | `daecfc5` (line references valid as of this commit) |
| **Filed as** | `452ecde` — issues [#1438–#1452](https://github.com/ademant/dansal/issues?q=label%3Acompliance) (`compliance` label) |
| **Status** | Current — update the date and reviewed commit when revisiting the audit |

Regulatory gap analysis for a private, non-commercial, Europe-hosted event information
service. Read-only review of the source tree; all `file:line` references are as of
commit `daecfc5` and will drift with development.

**This document is an engineering audit, not legal advice.** It identifies what the
software does and does not implement; the final word on applicability rests with the
operator and, where needed, a lawyer in the operator's member state.

## Assumptions

- Private, volunteer-run, non-commercial service; no revenue, no ads, no affiliate links.
- Tickets are not sold on dansal — events link out to external booking platforms.
- No AI features (no generated content, no chatbot, no model inference).
- Hosted in the EU/EEA by a micro/small enterprise (fewer than 50 staff, < €10M turnover).

## Table of contents

1. [Regulatory applicability](#1-regulatory-applicability)
2. [Personal data inventory](#2-personal-data-inventory)
3. [Retention and deletion status](#3-retention-and-deletion-status)
4. [Cookies, storage, third-party browser loads](#4-cookies-storage-third-party-browser-loads)
5. [Transfer register (outbound dependencies)](#5-transfer-register-outbound-dependencies)
6. [DSA assessment](#6-dsa-assessment)
7. [Feed import and copyright](#7-feed-import-and-copyright)
8. [Security baseline (GDPR Art. 32)](#8-security-baseline-gdpr-art-32)
9. [Gap register](#9-gap-register)
10. [Operator tasks vs code tasks](#10-operator-tasks-vs-code-tasks)

---

## 1. Regulatory applicability

| Regulation | Applies? | Reason |
|---|---|---|
| **GDPR** (Reg. 2016/679) | **Yes** | Personal data of organizers, admins, board posters, booking attendees, fediverse followers. Non-commercial status is irrelevant — no de-minimis threshold. |
| **ePrivacy Directive** 2002/58/EC + national cookie law | **Yes** | Cookies/local storage in the browser; rules on storing/accessing terminal equipment. |
| **DSA** (Reg. 2022/2065) | **Yes (reduced)** | Hosting provider; arguably an online platform (public event suggestions, contact board). As a micro/small enterprise, exempt from the Section 3 platform obligations by Art. 19. |
| **Copyright / Database Directive** 96/9/EC, DSM 2019/790 | **Yes** | Third-party feeds republished; OSM map data. |
| **AI Act** (Reg. 2024/1689) | No | No AI system is provided or deployed. |
| **Consumer law** (CRD 2011/83/EU, UCPD 2005/29/EC) | No | No sales, no prices, no contracts on dansal. *If any outbound link is a paid affiliate link, an advertising disclosure becomes mandatory (UCPD Art. 7(2)) — verify with operator.* |
| **European Accessibility Act** 2019/882 | No (likely) | Scope is a closed service list (e-commerce, banking, transport, e-books, AV media, e-comms); none apply without sales. Microenterprises providing services are exempt anyway (Art. 14(5)). EN 301 549 remains a sensible quality benchmark. |
| **Web Accessibility Directive** 2016/2102 | No | Public-sector bodies only. |
| **NIS2** | No (EU level) | Below the size threshold for a small volunteer service. **Check the national transposition** — some member states impose below-threshold duties or registration. |
| **CRA** (Reg. 2024/2847) | No | Applies to products with digital elements *placed on the market*; a hosted service is not a product. Distributing packaged binaries for third-party self-hosting would change this analysis. |
| **ePrivacy / national spam law** | Yes (limited) | All outbound mail is transactional (verification, login, moderation, bookings, contact relay). **No subscriber list, newsletter, or broadcast feature exists**, so no unsubscribe obligation is currently triggered. |
| **Consumer ADR / ODR** | No | No consumer contracts concluded on dansal. |

## 2. Personal data inventory

Status legend: **PRESENT** = deleted/expired by code; **PARTIAL** = some paths only;
**ABSENT** = nothing ever removes it.

### 2.1 Direct identifiers (`calendar.db`, schema: `cmd/dansal/main.go:3719`)

| Category | Columns / where | Retention | Refs |
|---|---|---|---|
| User account | email, display_name, password_hash, telegram, matrix, mastodon, website, description, telegram_chat_id, totp_secret, failed_login_count/since | PRESENT on delete | `main.go:3721-3744`; deletion `cmd/dansal/users.go:506-527`, `cmd/dansal/dbhelpers.go:29-64` |
| Session tokens | token (SHA-256), user_agent, ip, fingerprint, last_seen_at | PRESENT — hourly sweep + idle timeout | `main.go:3860-3872`, `:362-375` |
| SSO identity / OIDC flows | issuer_url, subject; state, nonce, pkce_verifier | PRESENT (CASCADE / TTL) | `main.go:3758-3776`, `:388` |
| Passkeys / WebAuthn / TOTP replay | credential_id, public_key; ceremony data; used codes | PRESENT (CASCADE / TTL) | `main.go:4337-4359`, `cmd/dansal/totp.go:75` |
| Invites / verification / magic tokens | preset_email, channel, message_id | PRESENT (TTL) | `main.go:4024-4058`, `:368-369` |
| Pending org registrations | email, org_contact_email, telegram_chat_id | PRESENT | `main.go:4205-4231`, `cmd/dansal/admin.go:794` |
| Anonymous board sessions | email, nickname | PRESENT — 30 d sliding / 90 d absolute | `main.go:4370-4384`, `cmd/dansal/board_sessions.go:12-13,279-281` |
| Matrix room map | matrix_id | ABSENT (only removed on verify-cancel) | `main.go:4385-4389`, `cmd/dansal/verify.go:249` |

### 2.2 Content carrying personal data

| Category | Columns | Retention | Refs |
|---|---|---|---|
| Event contacts | contact_name, contact_email | ABSENT (lives with event) | `main.go:3805-3806` |
| Suggesters | suggester_email, suggester_name, suggestion_token, pending_edit_json | PARTIAL — cleared on publish only, not for rejected/never-published suggestions | `main.go:3807-3813`, `cmd/dansal/events.go:2914-2946` |
| Attribution columns | changed_by, changed_by_id, created_by_id | ABSENT (intentionally preserved on user delete) | `cmd/dansal/dbhelpers.go:21-29` |
| Bookings | name, email, message, lang, verify_token, qr_token, expires_at | PRESENT (#1440) — pending and all terminal statuses (confirmed/approved/checked_in/cancelled) swept past `expires_at` = event end + `data_retention_days` | `main.go:376`, `sweepRetentionData` vs `cmd/dansal/bookings.go:bookingLongExpiry` |
| Contact board posts | nickname, email, telegram_username, poster_telegram_chat_id, message, lat/lon, manage_token, expires_at | PRESENT (sweep `main.go:370`) | `main.go:4142-4162` |
| Contact board replies | sender_email, sender_telegram, verify_token, expires_at | PRESENT (#1440) — consumed replies deleted on verify; expired-unclaimed ones now also swept hourly | `cmd/dansal/telegram.go:206`, `cmd/dansal/contact_posts.go:1120`, `sweepRetentionData` |
| Feed suggestions | email (NOT NULL), org_contact_email | PRESENT (#1440) — swept once `created_at` passes `data_retention_days`, regardless of status | `sweepRetentionData` |
| Org / musician / instructor contacts | contact_email, email, members_json, socials, notes_md | ABSENT (admin-managed entities) | `main.go:3997-4015`, `:3915-3942`, `:4106-4119` |
| Locations free text | address, notes_md | ABSENT (may contain names) | `main.go:3877-3908` |
| API keys | api_key (SHA-256), signing_secret_enc | PRESENT (CASCADE) | `main.go:3986-3996`, `cmd/dansal/apikeys.go:38-42` |

### 2.3 Federation data (`web.db`, `cmd/dansal_web/db.go:20`)

| Category | Columns | Retention | Refs |
|---|---|---|---|
| AP actors | RSA keypair (private key encrypted at rest) | ABSENT | `db.go:38-45`, `dbcrypto.go` |
| Followers / tag followers / outgoing follows | actor_uri, inbox_url | PRESENT only on manual Undo | `db.go:46-73`, `:556`, `:394`, `:711` |
| Delivery failures | inbox_url + **full activity JSON** | PRESENT — deleted on success or after `maxDeliveryAttempts` (8); #1440 closed the one gap where an org whose actor was gone stalled the attempt counter forever | `delivery.go:retryFailedDeliveries` |
| Federated events cache | raw_json, actor_id | PARTIAL (per-item delete) | `db.go:74-88`, `:831` |
| Geocode cache | **stored search query text** (visitor-derived) | PRESENT (#1440) — swept hourly past `data_retention_days` (default 90), was read-time-TTL-only before | `cmd/dansal_web/db.go:sweepGeocodeCache` |
| Timetable history | changed_by + snapshot | PRESENT (#1440) — swept past `data_retention_days`, in addition to the existing event-delete CASCADE | `sweepRetentionData` |

### 2.4 Authentication quality (good by default)

- argon2id password KDF with PBKDF2 fallback and legacy rehash-on-login — `cmd/dansal/password_kdf.go`, `cmd/dansal/users.go:75-100`
- All bearer/session/magic/verification/API tokens stored as SHA-256 — `cmd/dansal/helpers.go:18-19`, `cmd/dansal/auth.go:176`, `cmd/dansal/apikeys.go:41`
- Password-breach check via HIBP k-anonymity (5-char prefix only, fail-open) — `cmd/dansal/users.go:629-668`
- PII stripped from user list endpoints — `cmd/dansal/users.go:167-186`
- Caveats: TOTP seed and OIDC client_secret stored in clear; no SQLite at-rest encryption;
  an in-memory plaintext password is held up to 5 min during the TOTP second step
  (`cmd/dansal_web/auth.go:26-63`).

## 3. Retention and deletion status

### 3.1 Existing automatic sweeps (hourly goroutine, `cmd/dansal/main.go:356-401`)

| Table | Rule | Ref |
|---|---|---|
| tokens (incl. idle) | `expires_at < now` | `main.go:362,374` |
| verification_tokens, magic_login_tokens | TTL | `main.go:368-369` |
| contact_posts | `expires_at < now` | `main.go:370` |
| board sessions | 30 d / 90 d absolute | `board_sessions.go:279-281` |
| bookings (pending) | `status='pending' AND expires_at < now` | `main.go:376` |
| bookings (confirmed/approved/checked_in/cancelled) | `status != 'pending' AND expires_at < now` (expiry = event end + `data_retention_days`, set at verify time) | `sweepRetentionData`, `bookings.go:bookingLongExpiry` |
| `pending_fetch_suggestions` | `created_at` older than `data_retention_days` | `sweepRetentionData` |
| `contact_requests` (expired, unclaimed) | `verify_token IS NOT NULL AND expires_at < now` | `sweepRetentionData` |
| `timetable_history` | `changed_at` older than `data_retention_days` | `sweepRetentionData` |
| `geocode_cache` (dansal-web) | `fetched_at` older than `data_retention_days` | `cmd/dansal_web/db.go:sweepGeocodeCache`, hourly via `startDataRetentionSweep` |
| `delivery_failures` (dansal-web) | deleted on success or after `maxDeliveryAttempts` (8, with backoff); an org whose actor row is gone now also counts toward give-up instead of stalling forever | `cmd/dansal_web/delivery.go:retryFailedDeliveries` |
| webauthn_sessions, oidc_flows, totp_used_codes, invite_links, pending_registrations | TTL | `main.go:387-388`, `totp.go:75`, `register.go:1025`, `admin.go:794` |

`data_retention_days` (`server.data_retention_days` in `config.yaml`, same key in `dansal-web`'s `web.yaml`) defaults to 90 and is operator-configurable per instance (#1440).

### 3.2 Never swept (retention gaps) — closed by #1440

The six gaps below (confirmed bookings, `pending_fetch_suggestions`, expired-unclaimed `contact_requests`, `geocode_cache`, `delivery_failures`, `timetable_history`) are now covered by the sweeps in §3.1. `delivery_failures` turned out to already have a give-up-after-N-attempts dead-letter path from unrelated later work; #1440 only closed its one remaining edge case (an actor-lookup failure no longer stalls the attempt counter).

### 3.3 Erasure and access

- Erasure: self-service `DELETE /api/v1/me` (`cmd/dansal/users.go:506-527`, web UI
  `cmd/dansal_web/settings.go:287`) + admin/CLI delete; schema-driven cascade nulls
  attribution or deletes dependent rows (`cmd/dansal/dbhelpers.go:29-64`).
- **Access/export (Art. 15/20): closed by #1479/#1480.** Registered users:
  `GET /api/v1/me/export` + `dansal_admin export-user <id>`, schema-driven via
  the same foreign-key walk as erasure above (`internal/userexport`). Anonymous
  visitors (bookings/suggestions/board posts, email-only, no `user_id`):
  `dansal_admin export-user-by-email <email>`, a manual-process tool run after
  a request arrives at the instance's contact address (#1478) — deliberately
  not a public endpoint, since an unauthenticated email-matched lookup would
  let anyone read someone else's data.
- Anonymisation routine: ABSENT (deletion only).
- AP follower export/erasure: ABSENT.

### 3.4 Backups

- Nightly `VACUUM INTO` snapshot; **credentials stripped** (`password_hash=''`,
  `totp_secret=NULL`) — `cmd/dansal/backup.go:96-112`.
- Encrypted variant exists (`dansal_admin password-backup`) but is **not the default**;
  default archives are unencrypted, `backup_keep=0` = keep forever
  (`cmd/dansal/config.go:98-102`). Backups still contain all other personal data.

### 3.5 Logs

- **Closed by #1481/#1482.** Auth-failure logs (`cmd/dansal/auth.go`) and the
  registration log (`cmd/dansal_web/register.go`) now log user IDs instead of
  emails; the raw IP stays where fail2ban's `<HOST>` match needs it
  (`deploy/fail2ban/filter.d/*.conf`), verified unaffected. No app log files
  exist to retain (journald only, `deploy/journald/dansal.conf`, 30 days);
  nginx access/error/feed logs get an explicit per-instance logrotate stanza
  (`deploy/logrotate/dansal.conf`, 30 days) regardless of distro defaults.
  #1482 additionally fixed a pre-existing bug where fail2ban's jail never
  matched the actual multi-instance systemd unit names at all.
- nginx main format records `$remote_addr`, UA, XFF
  (`deploy/nginx/dansal-log-formats.conf:19-22`); feed log is anonymised (`:28`).
- **No logrotate stanza shipped** (README defers to the distro,
  `deploy/nginx/README.md:263-265`) and **no journald retention config**.

## 4. Cookies, storage, third-party browser loads

### 4.1 Cookies — 8 first-party, 0 third-party, 0 analytics

| Cookie | Purpose | Classification |
|---|---|---|
| `dsw_token`, `dsw_user` | Web session (HMAC-signed) | Strictly necessary — `internal/websession/websession.go:74-97` |
| `dwm_token`, `dwm_user` | Webmin session (24 h, + mTLS) | Strictly necessary — `cmd/dansal_webmin/auth.go:25-28` |
| `dsw_lang` | Language preference, 365 d | Functional — **opt-in via consent modal, never written on deny** (`static/base.js:524-540`, `templates/base.html:406-415`, `cmd/dansal_web/lang.go:11`); DNT/Sec-GPC honored (`cmd/dansal_web/privacy.go:10-20`) |
| `pending_reg` | Registration flow continuity | Functional, short TTL |
| 2 OIDC-flow cookies | OAuth state/PKCE | Strictly necessary, short TTL (`cmd/dansal_web/oidc.go:43-45,129-131`) |

All session cookies: `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`
(`internal/websession/websession.go:74-114`). Logout sends
`Clear-Site-Data: "cookies", "storage"` — purges localStorage across tabs
(`cmd/dansal_web/auth.go:239-250`).

**Conclusion: no cookie banner is required** — there is nothing non-essential to consent
to. The cookie list must still be disclosed in the privacy notice.

### 4.2 Browser storage

Two keys only: `colorScheme`, `dansal_starred_<eventID>` — client-side, purged on logout.

### 4.3 Third-party browser loads (disclose / consider self-hosting)

| Provider | Where | Notes |
|---|---|---|
| ~~`challenges.cloudflare.com` Turnstile~~ | — | **Removed by #1487**: the other anti-abuse layers on the suggest form (honeypot, form-token timing, rate limits, email verification) were already judged sufficient on their own. |
| `unpkg.com` flatpickr (**US**) | `templates/embed_calendar.html:7,101-102` | Calendar embeds only; SRI-pinned. Main site already self-hosts (`cmd/dansal_web/frontend.go:439`). Self-hosting this too tracked in #1448. |
| `unpkg.com` qrcode (**US**) | `cmd/dansal_webmin/templates/users.html:59` | Admin UI only; **no SRI**. |

Map tiles are **server-side proxied** (`cmd/dansal_web/tiles.go:37-43`) — visitor IPs
never reach OSM tile servers; OSM attribution always rendered (`static/base.js:212-224`).

### 4.4 Link handling

Outbound booking links are direct `<a href>` navigations with `noopener` and reduced
referrer — no dansal redirect layer, no click tracking, no UTM/personal-data passthrough.

### 4.5 Privacy UI

- Routes exist: `/privacy`, `/terms`, `/impressum` → `cmd/dansal_web/main.go:369-371`;
  content is operator-supplied markdown from `legal_dir` (`cmd/dansal_web/pages.go:73-90`).
- **No default privacy/terms text ships** with the product (zero matches for
  "Datenschutz"/"privacy policy"/"legal basis" outside i18n).
- **No template links `/privacy` or `/terms`** — footer only has help + impressum
  (`templates/base.html:508`). i18n keys `nav_privacy`/`nav_terms` exist in all 12
  languages and the sitemap lists both (`cmd/dansal_web/sitemap.go:91`).

## 5. Transfer register (outbound dependencies)

| Destination | Data sent | Side | Refs |
|---|---|---|---|
| `api.pwnedpasswords.com` (**US**) | 5-char SHA-1 prefix of new password | server | `cmd/dansal/users.go:641` |
| `unpkg.com` (**US**) | visitor IP | browser | `templates/embed_calendar.html`, webmin `users.html` |
| `api.telegram.org` (**Dubai/BVI**) | chat_id, verification/magic links | server | `cmd/dansal/telegram.go:21` |
| Matrix homeserver (operator-configured) | verification/magic messages | server | `cmd/dansal/verify.go:231` |
| SMTP relay (operator-configured) | recipient address, message body | server | `cmd/dansal/email.go:114-160` |
| OIDC / Mastodon IdP (operator-configured) | auth code, identity claims incl. email | server | `cmd/dansal/oidc.go:158` |
| `api.indexnow.org` (**US**) | public page URLs only (no personal data) | server | `cmd/dansal_web/indexnow.go:67-72` |
| Eventbrite / social-dance.today (opt-in syndication, off by default) | event payload | server | `cmd/dansal/syndication.go:373-506`, `config.go:96-101` |
| `nominatim.openstreetmap.org` (**DE**) | search query text, server IP | server | `cmd/dansal_web/geocode.go:198-205` |
| Arbitrary feed URLs | server IP + UA | server | `cmd/dansal/fetchurl.go` |
| Arbitrary fediverse inboxes | public event activities (by design) | server | `cmd/dansal_web/delivery.go:371-379` |
| Webhooks (operator-configured) | event/org/user **IDs** only | server | `cmd/dansal/webhooks.go:285-295` |
| MusicBrainz / Discogs (**US**) / Wikidata | admin search terms only | server | `cmd/dansal_web/enrichment_proxy.go:70-78` |

No visitor-facing analytics, advertising, or CDN third parties beyond §4.3.

## 6. DSA assessment

Role: **provider of intermediary services** (hosting) and, via anonymous event
suggestions + the contact board, an **online platform**.

| Duty | Article | Status | Evidence |
|---|---|---|---|
| Points of contact / legal representative | 10–12 | ⚠️ Via `/impressum` only | `main.go:369-371` |
| Terms and conditions | 13/14 | ⚠️ Route exists; operator-supplied, unlinked, may be empty | `pages.go:73-90` |
| Transparency reporting | 15 | Likely exempt (micro/small) — *verify Art. 15(4)* | — |
| **Notice-and-action mechanism** | **16** | ✅ Footer `Contact` field (webmin-editable) doubles as the reporting channel — **closed by #1478** | `base.html:506`, `cmd/dansal_webmin/templates/siteconfig.html:165` |
| **Statement of reasons** | **17** | **❌ Absent** — rejections return a generic status, no persisted reasoned decision | `cmd/dansal/main.go:4726,4743` |
| Notify suspicions of criminal offences | 18 | ❌ (no procedure) | — |
| Internal complaint handling, out-of-court dispute, trusted flaggers, misuse measures, recommender transparency, risk assessment | 20–35 | **Exempt** as micro/small enterprise (Art. 19) unless designated a VLOP | Art. 19(1) |
| Moderated public submissions | — | ✅ Honeypot + Turnstile + throttles + pending review; public API writes edge-blocked | `cmd/dansal/suggest.go:174-230`, `deploy/nginx/dansal.conf:147-176` |
| Federation abuse handling | — | ❌ No abuse contact, no block list for AP peers | `cmd/dansal_web/actor.go`, `delivery.go` |

## 7. Feed import and copyright

| Item | Status | Evidence |
|---|---|---|
| Identifying User-Agent | ✅ | `cmd/dansal/fetchurl.go:127-129` |
| **robots.txt / Crawl-delay compliance** | ✅ Closed by #1484 | `cmd/dansal/robotstxt_import.go`, checked in `importFromSource` (the recurring fetch-all path only — a one-off admin recheck or suggest-a-feed preview is not "crawling") |
| Per-source ToS / licence / permission record | ✅ Closed by #1483 | `fetch_sources.licence/attribution/terms_url/opt_out` |
| Original event URL shown publicly | ✅ | `templates/event.html:654-656` |
| Feed-source attribution shown publicly | ✅ Closed by #1485 | New public block in `event.html` (separate from the admin-only edit-link block) + `creditText`/`isBasedOn` in JSON-LD |
| Third-party HTML sanitisation | ✅ Closed by #1447 | `bluemonday.StrictPolicy()` (`cmd/dansal/text_plain.go`), applied in `insertEvent` (every event-creation path) and the anonymous suggest handler, before storage |
| OSM map attribution | ✅ | `static/base.js:212-224` (project rule: `attachTileLayer`, see AGENTS.md) |
| Rate/politeness on fetches | ✅ (basic) | Hourly timer + backoff/Retry-After (`fetchurl.go:162`, `systemd/dansal-fetch.timer`) |

## 8. Security baseline (GDPR Art. 32)

Already strong — **do not re-audit**; future work should preserve these:

- TLS 1.3, HTTP→HTTPS, 2-year HSTS, session tickets off, `server_tokens off`
  (`deploy/nginx/dansal.conf:84-141`); Traefik alternative included.
- Security headers split by design: nginx sets HSTS/nosniff/Referrer-Policy/
  Permissions-Policy/COOP even on 429/5xx; app sets nonce-CSP, `X-Frame-Options`,
  COEP/CORP (`cmd/dansal_web/main.go:688-841`); API returns `default-src 'none'`
  (`cmd/dansal/main.go:327-340`).
- CSRF: `Sec-Fetch-Site: cross-site` mutation rejection (`main.go:843-868`), one-time
  form tokens optionally IP-bound (`cmd/dansal_web/formguard.go:123-161`).
- Rate limiting: 7 web limiters (`cmd/dansal_web/main.go:84-116`), email
  backpressure tiers (`formguard.go:292-348`), per-account mutation limiter, nginx
  `limit_req`/`limit_conn`, fail2ban jails (`deploy/fail2ban/`).
- Auth: argon2id, TOTP with replay protection, WebAuthn/passkeys, OIDC, client-cert
  login, magic links (single-use hashed), login throttling.
- Edge protections: public write endpoints blocked at nginx so anti-abuse can't be
  bypassed (`dansal.conf:147-176`); loopback-only backends; internal shared secret.
- systemd hardening on all units (`NoNewPrivileges`, `ProtectSystem=strict`,
  `SystemCallFilter`, `MemoryDenyWriteExecute`, `UMask=0027`, …).
- Config/DB file modes `0640` root:dansal / dansal:dansal (`packaging/postinst:15-26`).
- Backups strip credentials; admin CLI mutations are audit-logged
  (`cmd/dansal/admin.go:96-130`); admin socket `0600`.

Known weaknesses: 2FA never enforced (opt-in only), default backups unencrypted,
no CI vulnerability scanning (no govulncheck/Dependabot), raw `err.Error()` returned on
one body-read path (`cmd/dansal/errors.go:70`).

## 9. Gap register

Priorities: **P0** = blocks a credible compliance claim · **P1** = strong hygiene ·
**P2** = nice to have. "Code" = implementable in this repo; "Operator" = content/process.

| ID | Gap | Pri | Type | Refs |
|---|---|---|---|---|
| G1 | **No privacy notice content.** Write default `privacy.md`/`terms.md`: controller identity, purposes + legal bases, cookie list (§4.1), transfer register (§5), retention schedule (§3), data-subject rights + contact, moderation/DSR notice. | P0 | Operator | `pages.go:73-90` |
| G2 | **Privacy/terms not discoverable.** Link both from the footer using existing `nav_privacy`/`nav_terms` i18n keys. Art. 12 requires the notice to be *easily accessible*. | P0 | Code | `templates/base.html:508` |
| G3 | ~~**Retention sweeps missing** for 6 stores (§3.2).~~ **Closed by #1440**: all six now swept (§3.1), horizon configurable via `data_retention_days` (default 90). | P0 | Code | `main.go:356-401`, §3.1 |
| G4 | ~~**DSA notice-and-action (Art. 16).** Public report endpoint (or `abuse@` contact surfaced on every page) → admin queue, with receipt confirmation.~~ **Closed by #1478**: the existing footer `Contact` field is the reporting channel (webmin hint now says so); operator still needs to set a reachable address and mention it in the privacy notice (G1). | P0 | Code | `siteconfig.html:165` |
| G5 | **DSA statement of reasons (Art. 17).** Persist reason + ground + redress path when suggestions/replies are rejected; show it to the submitter. | P0 | Code | `main.go:4726,4743` |
| G6 | ~~**No data export (Art. 15/20).** CLI/API to dump one subject's data (account, sessions, bookings, posts, suggestions) as JSON.~~ **Closed by #1479** (registered users) **and #1480** (anonymous visitors, manual). | P0 | Code | §3.3 |
| G7 | ~~**Log retention undefined.** Ship a logrotate stanza + journald `SystemMaxUse`; reduce routine email/IP logging where fail2ban doesn't need it; document the retention period in the notice.~~ **Closed by #1481** (retention + PII trims) **and #1482** (fail2ban multi-instance fix found while scoping this). Retention period (30 days) still needs folding into the operator's privacy notice (G1). | P0 | Code+Ops | §3.5 |
| G8 | **No breach runbook.** Document the 72h Art. 33 workflow: detection → assessment → authority notification → data-subject notification → record. | P1 | Operator | — |
| G9 | ~~**Feed robots.txt compliance + per-source licence/attribution fields**; show a public "imported from" credit on event pages; surface opt-out requests from publishers.~~ **Closed by #1483/#1484/#1485.** | P1 | Code | §7 |
| G10 | ~~**No vetted HTML sanitizer** for imported descriptions (bluemonday); RSS bodies stored raw.~~ **Closed by #1447.** | P1 | Code | §7 |
| G11 | **Third-party JS**: self-host flatpickr on embeds and qrcode in webmin (add SRI at minimum). ~~Disclose Turnstile in the notice or replace with an EU-hosted captcha.~~ **Turnstile removed entirely by #1487**; flatpickr/qrcode tracked in #1448. | P1 | Code | `embed_calendar.html:7`, `users.html:59` |
| G12 | **Federation privacy**: consider `authorized_fetch: true` by default (follower lists are currently unauthenticated-readable); define follower-data retention/erasure; add an abuse contact for AP peers. | P1 | Code | `config.go:50-54` |
| G13 | **Backups**: default to encrypted archives, set a finite `backup_keep`, document restore test cadence. | P1 | Code+Ops | `config.go:98-102`, `backup.go:118` |
| G14 | **Admin 2FA enforcement** option (require TOTP/WebAuthn for admin roles). | P2 | Code | — |
| G15 | **CI supply-chain**: govulncheck + Dependabot; fix raw `err.Error()` leak; backup restore verification. | P2 | Code | `errors.go:70`, `.github/` |

### Checks that passed (no action)

- Cookie posture needs **no consent banner** (§4.1); language cookie already opt-in, DNT/GPC honored.
- **No bulk email / newsletter** exists → no unsubscribe/List-Unsubscribe machinery required today. *(Adding any broadcast feature triggers ePrivacy Art. 13 + opt-out obligations.)*
- **No click tracking** on outbound booking links (§4.4).
- Tile proxy keeps visitor IPs away from OSM; OSM attribution present.
- Erasure path exists end-to-end with attribution null-out.
- Credential-stripped backups; hashed tokens; argon2id; hardened systemd units.

## 10. Operator tasks vs code tasks

**Operator (outside this repo):**
1. Write/approve the privacy notice and terms content (G1) — including the transfer
   register, retention schedule and cookie table from this audit.
2. ~~Decide on Turnstile (keep + disclose, or switch to EU-hosted alternative) (G11).~~ Decided: removed entirely (#1487).
3. Confirm **no affiliate/commission links** in event listings (else UCPD disclosure).
4. Document the breach runbook (G8) and check the national NIS2 transposition and any
   national imprint/accessibility duties for the member state of operation.
5. Processor arrangements for the services in §5 you actually enable (SMTP relay,
   Telegram, OIDC IdP) — Art. 28 contracts where applicable.
6. Set log retention policy with the hosting provider (G7) and backup retention (G13).

**Code (this repo, in order):** G2 → G3 → G4/G5 → G6 → G7 → G9–G13 → G14/G15.
