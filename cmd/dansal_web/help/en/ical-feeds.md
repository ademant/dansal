# Calendar feeds (iCal)

Subscribe to events as a live calendar feed in any iCal-compatible app (Google Calendar, Thunderbird, Apple Calendar, …).

**Global feed** — all published events:

```
/feed/events.ical
```

**Per-organisation feed** — events from one organisation only:

```
/feed/org/{slug}/events.ical
```

Replace `{slug}` with the organisation's short name visible in its URL.

Paste the full URL (including `https://`) into your calendar app's *Subscribe by URL* option. The feed refreshes automatically.

RSS and JSON feeds are also available at the same paths with `.rss` and `.json` extensions.
