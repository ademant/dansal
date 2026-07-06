# dansal – Veranstaltungsplattform für Tanzgemeinschaften

**dansal** ist eine Open-Source-Kalender- und Veranstaltungsplattform für Tanzgemeinschaften – von Bal-folk und Fest-noz über Tango bis hin zu Salsa-Festivals und Workshops.

## Was kann dansal?

- 📅 **Veranstaltungen verwalten**: Bälle, Workshops, Festivals und Kombinationen davon, inklusive mehrtägiger Veranstaltungen und Zeitpläne mit mehreren Räumen
- 🗺️ **Interaktive Karte**: Alle kommenden Veranstaltungen auf einer Karte mit Geodaten
- 🔄 **Automatischer Import**: Veranstaltungen können automatisch aus iCal- oder JSON-Feeds importiert werden
- 🌐 **Fediverse-Anbindung**: Veranstaltungen werden automatisch über ActivityPub veröffentlicht (Mastodon & Co.)
- 🌍 **Mehrsprachigkeit**: Die Oberfläche ist in 8 Sprachen verfügbar
- 🔐 **Rollenbasierte Zugriffsrechte**: Administrator, Publisher, Benutzer und Besucher haben unterschiedliche Rechte
- 📌 **Community-Pinnwand**: Mitfahrgelegenheiten, Unterkünfte und Ticketbörse direkt an jeder Veranstaltung

## Für wen ist diese Dokumentation?

| Zielgruppe | Beschreibung | Seite |
|---|---|---|
| **Besucher** | Veranstaltungen finden, Karte nutzen, Pinnwand verwenden | [Besucher](Besucher) |
| **Angemeldete Benutzer** | Veranstaltungen, Orte, Organisationen und Musiker anlegen und verwalten | [Benutzer](Benutzer) |
| **Systemadministratoren** | Installation, Konfiguration, Wartung und Fehlerbehebung | [Benutzer/Administration](Benutzer/Administration) |

## Rollenübersicht

| Rolle | Berechtigungen |
|---|---|
| **Besucher** (nicht angemeldet) | Veranstaltungen ansehen, Pinnwand-Einträge erstellen (mit Verifizierung) |
| **Benutzer** | Veranstaltungen, Orte, Organisationen und Musiker für die eigene Organisation anlegen und verwalten |
| **Publisher** | Veranstaltungen, Orte und Musiker erstellen/bearbeiten (Dienstkonto, z. B. für automatisierte Importe) |
| **Administrator** | Voller Zugriff auf alle Funktionen und Einstellungen der Instanz |

## Datenschutz

dansal speichert grundsätzlich keine personenbezogenen Daten. Für Besucher werden keine Daten gespeichert – Cookies werden nur verwendet, um die gewählte Anzeigesprache zu merken. Für bestimmte Aktionen (z. B. Pinnwand-Beiträge) ist eine Verifizierung per E-Mail oder Telegram erforderlich, um Missbrauch zu verhindern. Hierzu sollten anonymisierte E-Mail-Adressen verwendet werden. Diese Kontaktdaten werden mit Löschen des Pinnwandeintrag automatisch gelöscht.

## Weitere Informationen

- **Fehler melden**: [GitHub Issues](https://github.com/ademant/dansal/issues)
- **Quellcode**: Dieses Repository
- **Lizenz**: MIT

