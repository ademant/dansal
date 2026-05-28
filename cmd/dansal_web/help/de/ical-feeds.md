# Kalender-Feeds (iCal)

Veranstaltungen als Live-Kalender-Feed in jeder iCal-kompatiblen App abonnieren (Google Kalender, Thunderbird, Apple Kalender, …).

**Globaler Feed** — alle veröffentlichten Veranstaltungen:

```
/feed/events.ical
```

**Feed pro Organisation** — nur Veranstaltungen einer bestimmten Gruppe:

```
/feed/org/{slug}/events.ical
```

`{slug}` durch den Kurznamen der Organisation ersetzen (in deren URL sichtbar).

Die vollständige URL (inkl. `https://`) in die Option *Per URL abonnieren* der Kalender-App einfügen. Der Feed aktualisiert sich automatisch.

RSS- und JSON-Feeds sind unter denselben Pfaden mit den Endungen `.rss` bzw. `.json` verfügbar.
