# dansal — Dance Event Management System

**Open-source calendar and event platform for folk and social dance communities**

[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26%2B-blue)](https://go.dev/)
[![SQLite](https://img.shields.io/badge/database-SQLite-blue)](https://sqlite.org/)

## 🎯 What is dansal?

dansal helps dance communities organize, publish, and discover events — from bal-folk and fest-noz to workshops, festivals, and open sessions. It runs as a self-hosted service and federates with the wider fediverse via ActivityPub.

## ✨ Key Features

**Event discovery**
- Interactive map with clustered pins, weekly calendar, and filterable future list
- Filter by type (ball, workshop, festival, concert, …), dance style, town, and date range
- Swipe-to-navigate on mobile; synced mini-calendar
- Structured data (JSON-LD Schema.org Event) for rich Google results
- iCal export per event and per venue

**Event management**
- Create, edit, publish, and cancel events via a web admin UI
- Recurring event series with shared banner image and template defaults
- Image uploads (AVIF/JPEG) for events, series, organizations, musicians, venues, and instructors; AI-generated badge overlay when flagged
- Suggest-an-event wizard for unauthenticated submissions (with image upload)
- Multi-step duplicate detection (UID, URL, location+time, title+time, fuzzy)
- Timetable per event (schedule slots)

**Feeds & import**
- Automatic import from iCal and JSON feeds, attached to organizations
- Import preview with duplicate detection before confirming
- Location alias matching so feed venue names auto-resolve across imports

**Organizations & musicians**
- Organization pages with upcoming/past events and musician rosters
- Musician and instructor profiles with MusicBrainz, Wikidata, Discogs, and social links
- Role-based access: admins manage everything; publishers manage their org's events; users create events

**Community & communication**
- Per-event bulletin board: ride-sharing, accommodation offers, ticket exchange, lost & found
- Board posts with optional image attachments (up to 5)
- Email, Telegram, and Matrix notifications for board activity
- Verified board sessions (email confirmation, no account required)

**Fediverse / ActivityPub**
- Each organization has a fediverse actor (`@org@instance`); followers receive event announcements
- Relay actor for cross-instance discovery
- IndexNow pings on publish for fast search-engine indexing

**Booking**
- Optional per-event registration with ticket capacity, pricing (free / donation / fixed / tiered), and booking URL
- Capacity tracking; admin dashboard for registrations

**Authentication**
- Password + optional TOTP second factor
- Passkey (WebAuthn)
- Magic link via email, Telegram, or Matrix
- Invitation-link registration (admin-confirmed or self-service)

**Internationalisation**
- 12 UI languages: Breton, Catalan, Czech, German, English, Spanish, French, Italian, Dutch, Polish, Portuguese, Ukrainian
- Per-visitor language preference stored in a cookie; Accept-Language fallback
- Configurable date and time notation

**Operations**
- SQLite databases; automatic schema migrations
- Five binaries: API server, web frontend, admin CLI, web admin UI, docs server
- Multi-instance support (dev/test/prod) from one install
- Runtime config without restarts (IndexNow key, site name, banner, board settings, …)
- Automated SQLite backups; optional Docker deployment

## 📖 Documentation

| Audience | Guide |
|---|---|
| Visitors & dance enthusiasts | [Visitor Guide](VISITOR_GUIDE.md) |
| Event organizers & users | [User Guide](USER_GUIDE.md) |
| System administrators | [Admin Guide](ADMIN_GUIDE.md) |
| Developers | [Developer Guide](DEVELOPER_GUIDE.md) |
| REST API reference | [API.md](API.md) |
| Docker deployment | [Docker Guide](DOCKER.md) |

## 🔧 Quick Start

```bash
# 1. Build
go version   # must be 1.26+
make build

# 2. Set up a new instance (interactive — asks for ports, domain, SMTP, …)
sudo scripts/install-instance

# 3. Deploy
sudo make deploy INSTANCE=dev
```

See [Admin Guide](ADMIN_GUIDE.md) for full installation and configuration instructions.

## 📐 Architecture

| Component | Binary | Default port |
|---|---|---|
| REST API | `dansal` | 8000 |
| Web frontend + ActivityPub | `dansal-web` | 8080 |
| Web admin UI | `dansal-webmin` | 8090 |
| Per-instance docs server (serves `wiki/`) | `dansal-doc` | 8070 |
| Admin CLI | `dansal_admin` | — |

`dansal` is the only component with real data — the other three services are all clients of its REST API (`dansal-webmin` also uses a local Unix socket for privileged admin actions). nginx sits in front of everything as a reverse proxy and TLS terminator; every service binds to `127.0.0.1` only.

Two SQLite databases: `dansal`'s `calendar.db` (events, locations, organizations, musicians, users — the source of truth) and `dansal-web`'s own `web.db` (ActivityPub state, runtime site settings, caches). Uploaded images (events, organizations, musicians, venues) live under one shared directory tree, separate from `dansal-web`'s own tiny directory for instance branding (logo/banner/favicon).

Config: `/etc/dansal/<instance>/{config,web,webmin,doc}.yaml`

## 📞 Support & Community

- **Bug reports & feature requests**: [GitHub Issues](https://github.com/ademant/dansal/issues)
- **Discussions**: [GitHub Discussions](https://github.com/ademant/dansal/discussions)

## 🤝 Contributing

Contributions are welcome — code, translations, and documentation alike. See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## 📜 License

[MIT License](LICENSE) — Copyright © 2024 dansal contributors
