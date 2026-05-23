# dansal – Benutzerhandbuch

Dieses Handbuch richtet sich an eingeloggte Benutzerinnen und Benutzer mit der Rolle **„user"**.
Administratoren verfügen über zusätzliche Funktionen, die hier nicht beschrieben werden.

---

## Inhaltsverzeichnis

1. [Anmelden und Abmelden](#1-anmelden-und-abmelden)
2. [Kalender und Veranstaltungssuche](#2-kalender-und-veranstaltungssuche)
3. [Veranstaltungen verwalten](#3-veranstaltungen-verwalten)
   - [Neue Veranstaltung erstellen](#neue-veranstaltung-erstellen)
   - [Veranstaltung bearbeiten](#veranstaltung-bearbeiten)
   - [Veranstaltung klonen](#veranstaltung-klonen)
   - [Veranstaltung löschen](#veranstaltung-löschen)
   - [Veranstaltungen importieren](#veranstaltungen-importieren)
4. [Vorlagen](#4-vorlagen)
5. [Buchungen](#5-buchungen)
6. [Musiker verwalten](#6-musiker-verwalten)
7. [Veranstaltungsorte verwalten](#7-veranstaltungsorte-verwalten)
8. [Profil und Einstellungen](#8-profil-und-einstellungen)
9. [Sprache wechseln](#9-sprache-wechseln)

---

## 1. Anmelden und Abmelden

**Anmelden**
Öffne `/login` und gib Benutzername und Passwort ein.
Alternativ kannst du über „Ohne Passwort anmelden" einen Einmallink per E-Mail, Telegram oder Matrix anfordern.

**Abmelden**
Klicke oben in der Navigation auf **Abmelden**.

---

## 2. Kalender und Veranstaltungssuche

Die Startseite `/` zeigt alle bevorstehenden, veröffentlichten Veranstaltungen.

**Filtermöglichkeiten:**
- Suchfeld: Volltextsuche nach Titel und Ort
- Zeitraum: Von/Bis
- Typ: Ball, Workshop, Festival
- Land, Stadt, Veranstalter, Tanzstil
- Eintritt: kostenlos / Spende / kostenpflichtig

**Ansichten:** Liste · Wochensicht · Kartenansicht (oben rechts)

Auf der Detailseite einer Veranstaltung (`/events/{id}`) findest du:
- Datum, Uhrzeit, Ort, Beschreibung
- Timetable/Programm
- Mitwirkende Musiker
- Eintrittspreise und Buchungslink
- Mitfahr- und Unterkunftsbörse (nur angemeldete Nutzer)
- „Zum Kalender hinzufügen"-Link (.ics)

---

## 3. Veranstaltungen verwalten

Nach dem Einloggen erscheint in der Navigation der Admin-Bereich.
Alle Veranstaltungsfunktionen sind unter **Veranst.** → `/admin/events` erreichbar.

### Neue Veranstaltung erstellen

1. Klicke auf **Neue Veranstaltung** oder öffne `/admin/events/new`.
2. Fülle das Formular aus. Pflichtfelder sind mit **\*** markiert; der Speichern-Button bleibt deaktiviert, solange ein Pflichtfeld leer ist.

**Formularabschnitte:**

| Abschnitt | Felder |
|---|---|
| Bild | Drag & Drop oder Datei wählen |
| Grundinfo | Titel \*, Datum, Beginn/Ende, Ball/Workshop/Festival, Schwierigkeit, Beschreibung, Website, Buchungs-URL, Tags |
| Organisation | Bestehende wählen oder neue anlegen |
| Veranstaltungsort | Bestehenden wählen oder neuen anlegen (mit Geocoding-Funktion) |
| Eintritt | Kostenlos / Spende / Festpreis / Staffelpreise |
| Programm | Timetable mit Raum und Musiker-Zuordnung |
| Tanzstile | Mehrfachauswahl |

3. Klicke auf **Speichern**.

> **Tipp – Vorlage laden:**
> Sind Vorlagen vorhanden, erscheint oberhalb des Formulars ein Dropdown „Vorlage laden".
> Wähle eine Vorlage aus – das Formular wird automatisch befüllt.

### Veranstaltung bearbeiten

1. Öffne `/admin/events` und klicke auf den Titel der Veranstaltung oder auf **Bearbeiten**.
2. Ändere die gewünschten Felder.
3. Klicke auf **Speichern**. Der Speichern-Button ist deaktiviert, solange der Titel leer ist.

### Veranstaltung klonen

Klonen erstellt eine Kopie einer bestehenden Veranstaltung mit allen Feldern außer dem Datum.

1. Öffne die Veranstaltung zum Bearbeiten.
2. Klicke auf **Klonen** (neben Speichern).
3. Es öffnet sich das neue Formular, das mit den Daten der Originalveranstaltung vorbefüllt ist.
4. **Das Startdatum muss geändert werden** – der Speichern-Button bleibt gesperrt, bis ein anderes Datum eingetragen ist.
5. Passe alle weiteren Felder an und speichere.

> **Hinweis:** Gehörst du nicht zur Organisation der Originalveranstaltung, werden Organisation und Ort beim Klonen geleert.

### Veranstaltung löschen

1. Öffne die Veranstaltung zum Bearbeiten.
2. Klicke unten auf **Löschen** und bestätige die Rückfrage.

### Veranstaltungen importieren

Über `/admin/events/import` kannst du Veranstaltungen aus einer externen Quelle einlesen:

- **Datei hochladen:** `.ics`-Datei (iCal) oder folkdance-JSON
- **Feed-URL angeben:** URL eines iCal-Feeds oder kompatiblen JSON-Feeds

Ablauf:
1. Datei hochladen oder URL eingeben und auf **Vorschau** klicken.
2. Die erkannten Veranstaltungen werden angezeigt.
3. Wähle die zu importierenden Einträge aus.
4. Klicke auf **Ausgewählte importieren**.

---

## 4. Vorlagen

Vorlagen speichern die Einstellungen einer Veranstaltung (ohne Datum) zur Wiederverwendung.

**Vorlage aus bestehender Veranstaltung speichern:**
1. Öffne eine Veranstaltung zum Bearbeiten.
2. Klappe den Bereich **Als Vorlage speichern** am unteren Ende der Seite auf.
3. Gib einen Namen ein.
4. Wähle optional eine Organisation, damit die Vorlage allen Mitgliedern dieser Organisation zugänglich ist.
5. Klicke auf **Vorlage speichern**.

**Vorlage beim Erstellen einer neuen Veranstaltung laden:**
1. Öffne `/admin/events/new`.
2. Wähle im Dropdown **Vorlage laden** eine Vorlage aus.
3. Das Formular wird sofort mit den gespeicherten Werten befüllt.
4. Passe Datum und weitere Felder an und speichere.

**Vorlagen verwalten:**
Unter `/admin/templates` siehst du alle deine persönlichen Vorlagen und die Vorlagen deiner Organisationen.
Dort kannst du Vorlagen löschen.

---

## 5. Buchungen

Wenn die Buchungsfunktion für eine Veranstaltung aktiviert ist, können Besucher online Plätze reservieren.

**Buchungen einsehen:**
1. Öffne `/admin/events` und klicke auf das Buchungs-Symbol bzw. öffne `/admin/events/{id}/bookings`.
2. Die Liste zeigt alle Anfragen mit Status (Ausstehend / Bestätigt / Eingecheckt / Storniert).

**Buchungen bearbeiten:**
- **Genehmigen:** Buchungsanfrage freigeben.
- **Stornieren:** Buchung absagen.
- **Löschen:** Eintrag entfernen.

**Check-in-Modus:**
Über den Link „Check-in-Modus" auf der Buchungsseite gelangst du in den QR-Code-Scanner für den Einlass.

---

## 6. Musiker verwalten

Unter `/admin/musicians` werden alle Musiker und Bands gepflegt.

**Neuen Musiker anlegen:**
1. Klicke auf **Neu**.
2. Fülle mindestens den Bandnamen aus.
3. Optional: MusicBrainz-ID verknüpfen (Suche direkt im Formular), Biografie, Social-Media-Links, Foto hochladen.
4. Speichere.

**Musiker bearbeiten:**
Klicke in der Liste auf **Bearbeiten**, ändere die Felder, speichere.

---

## 7. Veranstaltungsorte verwalten

Unter `/admin/locations` werden Veranstaltungsorte gepflegt.

**Neuen Ort anlegen:**
1. Klicke auf **Neu**.
2. Gib Name, Adresse, Stadt, Land ein.
3. Nutze **Suchen** für automatisches Geocoding (Koordinaten werden via OpenStreetMap ermittelt).
4. Speichere.

**Ort bearbeiten:**
Klicke in der Liste auf **Bearbeiten**, ändere die Felder, speichere.

> **Tipp:** Beim Erstellen einer Veranstaltung kannst du einen neuen Ort direkt im Formular anlegen – ohne extra auf die Orte-Seite wechseln zu müssen.

---

## 8. Profil und Einstellungen

Unter `/settings` kannst du dein Konto verwalten:

| Bereich | Felder |
|---|---|
| Konto | Benutzername, Rolle (nur lesbar) |
| Profil | Kurzbeschreibung |
| E-Mail | Adresse ändern und bestätigen |
| Kontakt | Telegram, Matrix, Mastodon, Website |
| API-Schlüssel | Neue Schlüssel erstellen (z. B. für Feed-Zugriff), bestehende löschen |

**E-Mail bestätigen:**
Nach einer Adressänderung wird ein Bestätigungslink zugesandt.
Klicke auf **Bestätigungsmail senden** und öffne den Link in der E-Mail.

**API-Schlüssel:**
Schlüssel werden nur einmal beim Erstellen vollständig angezeigt – bitte sofort kopieren.

---

## 9. Sprache wechseln

Klicke in der Navigation auf die Flagge/Sprachbezeichnung oder öffne `/lang`.
Die gewählte Sprache wird in einem Cookie gespeichert und bei jedem weiteren Besuch verwendet.

Verfügbare Sprachen: Deutsch, Brezhoneg, Englisch, Español, Français, Italiano, Nederlands, Українська.

---

*Dieses Dokument beschreibt die Rolle „user". Weitere Funktionen (Organisationsverwaltung, Nutzerverwaltung, Feed-URLs, Systemkonfiguration) sind ausschließlich Administratoren vorbehalten.*
