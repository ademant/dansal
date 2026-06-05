# dansal - Comprehensive Dance Event Management System

dansal is a powerful, open-source calendar and event management platform designed specifically for dance communities. Built with SQLite backend and REST API, dansal makes it easy to organize, publish, and discover dance events of all types - from folk dances and tangos to salsa festivals and workshops.

## Key Features

✅ **Multi-Format Import** - Import events via iCal (.ics), Folkdance.json, or direct API integration
✅ **Automatic Publishing** - Events are automatically published to the fediverse via ActivityPub
✅ **Multi-Language Support** - Available in 8 languages with easy localization
✅ **Geospatial Discovery** - Find events near you with interactive map visualization
✅ **Multi-Channel Notifications** - Email, Telegram, and Matrix alert options
✅ **Comprehensive Access Control** - Role-based permissions (admin, publisher, user, viewer)
✅ **Venue Management** - Detailed venue profiles with accessibility, parking, and dance floor information
✅ **Bulletin Board System** - Ride-sharing, ticket exchange, and community messaging

## For Event Organizers

dansal simplifies event management by allowing you to:
- **Integrate existing calendars** - Connect your current iCal feeds for automatic event synchronization
- **Create rich event listings** - Add detailed schedules, pricing tiers, musician information, and more
- **Manage multiple venues** - Maintain venue profiles with accessibility details and amenities
- **Use templates** - Save event configurations as reusable templates
- **Control visibility** - Publish events publicly or keep them private for your organization

## For Dance Enthusiasts

Find your next dance experience with:
- **Interactive map interface** - Browse events by location and date
- **Detailed event information** - View schedules, pricing, venue details, and organizer contacts
- **Community bulletin board** - Connect with others for ride-sharing and ticket exchanges
- **Multi-language support** - Access information in your preferred language
- **Mobile-friendly design** - Swipe through weeks and access information on-the-go

## Technical Overview

- **Backend**: Go-based REST API with SQLite database
- **Frontend**: Responsive web interface with dark/light mode support
- **Integration**: ActivityPub federation, Telegram bot support, MusicBrainz artist linking
- **Deployment**: Simple single-binary deployment with configurable options

dansal is perfect for folk dance clubs, tango communities, salsa schools, and any organization that wants to simplify event management while providing a rich experience for participants.

## Structure
For managing events the service offer following data types to describe events:
### locations
Beside the normal information like street and town, geo coordinates are saved, so this location can be shown on a map. Also more information like link to different ressources can be placed.

For each location the upcoming events are shown.

### Musicians
A list of musicians is integrated. If possible a link to musicbrainz is shown, where discography and more information are stored (band member etc.).

Musicians can be attached to an event and for each musician the list of upcoming events are shown.

### Organisations
A layer over locations are organisations, like folk clubs. To an organisation several locations can be attached. Also fetch url can be stored for organisation, where to fetch iCal information of upcoming events.

For each organisation the list of upcoming events are presented.

### Events
Events are the central object of this page. They have description, overall time and location. They are assigned to an organisation. To the event musicians can be assigned. In an optional timetable detailed information can be stored, e.g. which musician is playing in which room at defined time.
Also pricing informations can be attached.

## Display
On the main page a map of the next events are shown. The time range is set by the weekly table below. Default is the actual week shown with all events for each day. At the table the week can be change to one week earlier or later.

Each events can be displayed in detail.


## Access
Viewing is possible for all.

Importing can be done via iCal-Feeds for feeds which offer events.

Creating inside is possible with an account. Assigned to an organisation the user can create events for this organisation only.

## Requirements

- Go 1.22+
- SQLite3

## Run

```bash
go run .
```

The server starts on the port set in `config.yaml` (default `8000`). On first run an admin user is created and the password is printed to the console.

## Configuration

`config.yaml`:

```yaml
server:
  port: 8000
```

## Build

```bash
go build -o dansal
./dansal
```

## Administrator guide

### First login

On the very first run, dansal creates an admin account and prints the generated password to the console:

```
Admin user created — username: admin  password: <generated>
```

Log in at `/login` with those credentials and change the password immediately in **Settings**.

### User management (`/admin/users`)

Admins can create, edit, and delete user accounts.

**Roles:**

| Role | Can do |
|---|---|
| `admin` | Everything |
| `publisher` | Create and edit events, manage locations and musicians |
| `user` | Create events for their own organisation only |
| `viewer` | Read-only; can see unpublished events |

**Creating a user directly** — fill in username, email, password and role. The user can then log in immediately.

**Invite links** — a safer alternative for self-registration. Go to `/admin/invites`, create a link with an optional role and organisation pre-assignment, and share it. The recipient registers through the link and lands with the configured role already set. Links expire after `invite_expiry_hours` (default 48 h).

**Disabling an account** — tick the *Disabled* checkbox on the edit form. The user cannot log in but the account and its history are preserved.

**Deleting a user** — admin accounts cannot be deleted via the UI; demote to another role first.

### Organisations (`/admin/organizations`)

Organisations represent folk clubs, dance groups, or any recurring promoter. Each organisation can have:

- One or more **locations** (venues it uses regularly)
- One or more **iCal feed sources** that are fetched automatically
- Social links: website, Mastodon, Instagram, Facebook, contact email

**Create** — click *New organisation*, fill in name and optional details.

**Edit** — the edit page also lets you assign locations and feed sources to the organisation.

**Run feeds** — the table has a *Run feeds* button per organisation that immediately fetches all assigned iCal sources and imports new events.

**Delete** — confirmation required; cascades to membership records but not to events or locations.

**Assigning users** — on the edit page, add users as members. Members with the `user` role can then create and edit events belonging to that organisation.

### Locations (`/admin/locations`)

Locations store the venue details used by events. Fields: name, short name, address, postcode, town, country, latitude/longitude, website, organisation.

The geo-coordinates are shown on the map on event detail pages. If coordinates are missing, the event still appears in the list but not on the map. You can look up coordinates from any mapping service and paste them in.

### Events (`/admin/events`)

**Creating an event:**

1. Go to `/admin/events/new`.
2. Set title, start/end time, location, and organisation.
3. Choose type: **Ball**, **Workshop** (with optional difficulty: beginner / advanced / pro), **Festival**, or a combination.
4. Add a description, pricing, booking URL, and tags as needed.
5. Attach musicians from the musicians list.
6. Add a timetable for multi-room or multi-slot events.
7. Save — the event is created as **unpublished** and only visible to logged-in users.

**Publishing** — open the event detail page and click *Publish*, or tick *Published* on the edit form. Only published events appear on the public map and calendar.

**Cancelling** — tick *Cancelled* on the edit form. The event remains visible with a cancellation notice.

**Importing via iCal feed** — go to `/admin/fetchurls`, add a feed URL (type `ical`), optionally assign it to an organisation, and click *Fetch*. Events already present (matched by UID or URL) are updated rather than duplicated. You can also set up recurring fetches by assigning feeds to organisations and using the *Run feeds* button or the scheduled fetch-all endpoint.

### Musicians (`/admin/musicians`)

Musicians can be linked to a MusicBrainz ID for automatic discography lookups. Add social links (Mastodon, Instagram, Facebook, SoundCloud) and a short description. Once created, musicians can be attached to individual events.

### iCal feed sources (`/admin/fetchurls`)

Each feed source has a URL, a type (`ical`), optional tags that are applied to all imported events, and an optional organisation assignment.

**Bulk operations** — select multiple sources and use *Bulk fetch* to run them all at once, or *Bulk assign org* to move a batch to an organisation.

**Last fetched** — the table shows when each source was last successfully fetched.

### Contact board moderation

Each event page has a rideshare/accommodation board. Posts are only visible after the poster confirms their email. As admin (or as a member of the event's organisation) you can delete any post directly from the event page using the *Delete* button next to each entry.

### Verification and account security

- **Email verification** — users verify their email from Settings. Tokens expire after `verification_expiry_hours` (default 24 h).
- **Telegram verification** — users link their Telegram account via a deep link from Settings (see [Telegram integration](#telegram-integration)).
- **Failed login lockout** — after `login_max_failures` failed attempts within `login_failure_window_secs`, the account is automatically disabled. Re-enable it from the user edit page.
- **Magic link login** — users can request a one-time login link sent to their email, useful if they forget their password.
- **Sessions** — users can view and revoke their active sessions from Settings. Admins can see all sessions via the API.

### Reload configuration without restart

Send `SIGHUP` to the running process to reload `config.yaml` without downtime:

```bash
kill -HUP $(pidof dansal)
```

Port, database path, and admin socket changes still require a full restart.

---

## Legal pages (Contact & Impressum)

The web frontend supports an optional contact line in the page footer and an Impressum page at `/impressum`. Both are configured via a separate YAML file referenced by `pages_file` in the web config.

### Configuration

Add `pages_file` to the web frontend's `config.yaml`:

```yaml
pages_file: pages.yaml
```

### pages.yaml format

Both `contact` and `impressum` are maps from language code to text. The fallback language is `de`; if the visitor's language has no entry the German text is shown.

```yaml
contact:
  de: "Kontakt: info@example.org"
  en: "Contact: info@example.org"
  fr: "Contact : info@example.org"

impressum:
  de: |
    Angaben gemäß § 5 TMG
    Max Mustermann
    Musterstraße 1
    12345 Musterstadt
    E-Mail: info@example.org
  en: |
    Legal notice
    Max Mustermann
    1 Example Street
    12345 Example Town
    E-mail: info@example.org
```

### Behaviour

- **Contact** — the text is rendered as a plain line in the site footer on every page. If the entry for the current language is missing, the `de` entry is used as fallback. If neither exists, the footer line is omitted entirely.
- **Impressum** — the text is rendered verbatim (whitespace preserved) on `/impressum`. A link to that page appears in the footer only when an impressum text is configured. Language fallback works the same way as for contact.

If `pages_file` is not set in the config, both features are silently disabled — no footer line, no `/impressum` route content.

## dansal_web layout

The web frontend supports several layout options in its `config.yaml`.

### Banner and logo

```yaml
# Height of the banner image in pixels.
# 0 = banner is not rendered on that page type.
banner_height_main: 200   # shown on the main page  (default: 200)
banner_height_sub:  0     # hidden on all sub-pages (default: 0)

# Height of the logo in the navigation bar in pixels.
logo_height_main: 48      # main page  (default: 48)
logo_height_sub:  32      # sub-pages  (default: 32)
```

### Custom image files

By default the logo, banner and favicon are the SVGs compiled into the binary. Set `images_dir` to serve your own files from disk instead:

```yaml
images_dir: /etc/dansal/images
```

Place `logo.svg`, `banner.svg`, and/or `favicon.svg` in that directory. Missing files fall back to the built-in defaults. Changes take effect after a service restart.

### Colour scheme (dark / light mode)

```yaml
dark_mode: auto    # follow the visitor's system preference (default)
# dark_mode: dark  # always start in dark mode
# dark_mode: light # always start in light mode
```

`auto` uses the `prefers-color-scheme` media query so the page matches the visitor's OS setting. Visitors can override the setting at any time with the **◑** toggle in the navigation bar; their choice is stored in `localStorage`.

## Telegram integration

Dansal supports Telegram in two ways:

1. **Account verification** — users prove they control a Telegram account by clicking a deep link that sends a one-time token to the bot.
2. **Admin messaging** — admins can send a plain-text message to any user who has completed Telegram verification.

### How it works

Because Telegram bots cannot initiate a conversation, the verification flow relies on a deep link:

1. The user adds their Telegram handle (`@username`) in **Settings**.
2. They click **Get Telegram verification link**. The server mints a short-lived token and returns a deep link of the form `https://t.me/BOTNAME?start=TOKEN`.
3. The user clicks the link. Telegram opens the bot and automatically sends `/start TOKEN`.
4. The bot webhook receives the `/start TOKEN` command, validates the token, marks `telegram_verified = 1` on the user record, and stores the numeric Telegram `chat_id` needed for future outbound messages.
5. The bot replies with a confirmation message in Telegram.

### Create a bot

1. Open Telegram and start a conversation with **@BotFather**.
2. Send `/newbot` and follow the prompts to choose a name and a username (must end in `bot`).
3. BotFather gives you a **bot token** (format `123456789:AABBcc…`). Keep this secret.

### Configure dansal

Add the following keys to the backend's `config.yaml`:

```yaml
server:
  telegram_bot_token: "123456789:AABBccDDeeFFggHH"   # from BotFather
  telegram_bot_name: "myDansalBot"                    # username without the leading @
```

`telegram_bot_token` is used to call the Telegram Bot API (send messages, etc.).  
`telegram_bot_name` is used to construct the deep link shown to users in Settings.

### Register the webhook

Telegram must know where to forward incoming messages. Register the webhook once after deploying:

```bash
curl -X POST "https://api.telegram.org/bot<TOKEN>/setWebhook" \
     -H "Content-Type: application/json" \
     -d '{"url": "https://yourdomain.example.com/telegram/webhook"}'
```

Replace `<TOKEN>` with your bot token and the URL with your public server address. The endpoint `/telegram/webhook` is served by the dansal backend and requires no authentication — Telegram calls it directly.

You can verify the webhook is registered:

```bash
curl "https://api.telegram.org/bot<TOKEN>/getWebhookInfo"
```

### Sending a message to a user (admin)

Once a user has verified their Telegram account, an admin can send them a message via the API:

```bash
curl -X POST "https://yourdomain.example.com/api/v1/users/42/telegram/message" \
     -H "Authorization: Bearer <ADMIN_TOKEN>" \
     -H "Content-Type: application/json" \
     -d '{"text": "Hello from dansal!"}'
```

Returns `204 No Content` on success. Returns `400` if the user has no verified Telegram account.

### Verification token lifetime

Verification tokens expire after the number of hours set by `verification_expiry_hours` (default `24`). If a user lets the link expire, they can request a new one by clicking the button again in Settings.

### Notes

- The `/telegram/webhook` route is unauthenticated by design — Telegram servers call it directly. The endpoint validates the token from the payload; there is no secret exposed.
- If `telegram_bot_token` or `telegram_bot_name` is not set, the verification request returns a `500` error and no token is stored.
- Changing the Telegram handle in Settings clears the existing verification status and stored `chat_id`, requiring the user to re-verify.
