---
nav_order: 6
---

# Veranstaltungen erstellen & verwalten – Tagesablauf

Diese Anleitung begleitet dich durch den typischen Arbeitsablauf beim Anlegen und Verwalten von Veranstaltungen in dansal – vom einfachen Tanzabend bis zur komplexen Mehrtagesveranstaltung mit mehreren Räumen und Musikern.

---

## 📚 Einführung: Wie dansal Veranstaltungen organisiert

Dansal verwendet eine **klare, hierarchische Datenstruktur**, die sich an den realen Gegebenheiten von Tanzveranstaltungen orientiert:

```
Instanz
  ├── Organisation (Verein, Folkclub, Veranstalter)
  │     ├── Veranstaltungsort (Gebäude)
  │     │     └── Raum (Saal 1, Saal 2, ...) [optional]
  │     └── Musiker / Anleiter
  └── Veranstaltung
        ├── Zeitplan (mehrere Abschnitte)
        ├── Eintrittspreise (mehrere Stufen)
        └── Bilder
```

**Vorteile dieser Struktur:**
- Informationen werden nur einmal gepflegt (z. B. Adresse eines Ortes) und automatisch an alle Veranstaltungen dort vererbt
- Veränderungen an einer Organisation oder einem Ort wirken sich sofort auf alle zugehörigen Veranstaltungen aus
- Mehrere Organisationen können denselben Ort nutzen

---

## 🏢 Organisationen verwalten

Organisationen sind das Herzstück von dansal. Jede Veranstaltung ist genau einer Organisation zugeordnet.

### Organisation erstellen

1. Navigiere zu **Organisationen → Neue Organisation erstellen**
2. Grunddaten eingeben:
   - **Name**: Vollständiger Name (z. B. "Balfolk Verein München e.V.")
   - **Kurzname**: Wird in URLs verwendet – nur Buchstaben, Ziffern, Bindestriche, Unterstriche
   - **Beschreibung**: Öffentliche Beschreibung der Organisation
   - **Webseite**: Link zur eigenen Homepage
   - **Kontakt-E-Mail**: Öffentliche E-Mail-Adresse
   - **Social Media**: Mastodon, Instagram, Facebook, Telegram, Matrix

### Organisation bearbeiten

Alle Daten lassen sich später über **Organisationen → [Name der Organisation] → Bearbeiten** ändern.

**Wichtig:** Änderungen an der Organisation (z. B. neue Kontakt-E-Mail) wirken sich auf alle zugehörigen Veranstaltungen aus.

---

## 📍 Veranstaltungsorte verwalten

Ein Veranstaltungsort kann ein Gebäude (z. B. "Tanzsaal Musterstadt") oder ein Raum innerhalb eines Gebäudes sein (z. B. "Saal A" im "Tanzsaal Musterstadt").

### Gebäudestammdaten anlegen

1. Navigiere zu **Orte → Neuer Ort**
2. Grunddaten ausfüllen:
   - **Name / Kurzname**: Offizieller Name und interne Abkürzung
   - **Adresse**: Straße, PLZ, Ort, Land (mit ISO-Code)
   - **Koordinaten**: Für die Karte – werden automatisch aus der Adresse abgeleitet oder manuell eingegeben
   - **OpenStreetMap-ID**: Automatische Verknüpfung mit OSM (optional)
   - **Barrierefreiheit**: Rollstuhlgerecht, Induktionsschleife, sehbehindertengerecht
   - **Parkplatzsituation**: Beschreibung der Parkmöglichkeiten
   - **Bodenbelag**: Parkett, Holz, Beton, etc.
   - **Straßenschuhe**: Erlaubt oder verboten

### Räume als untergeordnete Orte anlegen

Falls ein Gebäude mehrere Säle hat:

1. Zuerst das **Gebäude** als Ort anlegen
2. Dann im Gebäude den Button **"Raum hinzufügen"** klicken
3. Raumdaten eingeben:
   - **Name**: Saal A, Saal B, etc.
   - **Kapazität**: Maximale Personenanzahl
   - **Größe**: Quadratmeter
   - **Barrierefreiheit**: Kann vom Gebäude abweichen
   - **Bodenbelag**: Kann vom Gebäude abweichen

**Automatische Vererbung:** Adresse, Koordinaten und Parkplatzangaben werden automatisch vom Gebäude übernommen – müssen nicht neu eingegeben werden.

### Ort bearbeiten

Alle Daten lassen sich später über **Orte → [Name des Ortes] → Bearbeiten** ändern.

**Tipp:** Wenn ein Ort in einem Feed mit einem anderen Namen auftaucht (z. B. "Tanzsaal" statt "Tanzsaal Musterstadt"), wird dieser alternative Name als **Alias** gespeichert. Bei zukünftigen Importen aus derselben Quelle wird der Ort dann automatisch erkannt.

---

## 📅 Veranstaltungen erstellen – Grundablauf

Es gibt mehrere Wege, eine neue Veranstaltung anzulegen:

| Methode | Vorteile | Typischer Einsatz |
|---|---|---|
| **Leere Veranstaltung** | Volle Flexibilität | Einmalige, individuelle Veranstaltungen |
| **Über Organisation** | Organisation bereits vorausgewählt | Veranstaltungen einer bestimmten Organisation |
| **Über Ort** | Ort bereits vorausgewählt | Veranstaltungen an einem bestimmten Ort |
| **Über Vorlage** | Viele Felder vorausgefüllt | Wiederkehrende Veranstaltungen |
| **Klonen** | Alle Daten einer bestehenden Veranstaltung | Ähnliche Veranstaltungen |
| **Terminserie** | Mehrere Termine mit gemeinsamen Daten | Regelmäßige Veranstaltungen (z. B. wöchentliche Tanzabende) |

### Leere Veranstaltung anlegen

1. Navigiere zu **Veranstaltungen → Neue Veranstaltung erstellen**
2. **Grunddaten** ausfüllen:
   - **Titel**: Klarer, beschreibender Name (z. B. "Balfolk-Tanzabend mit Band XY")
   - **Start-/Enddatum & Uhrzeit**: Dauer der Veranstaltung
   - **Ort**: Bestehenden Ort auswählen oder neuen anlegen
   - **Organisation**: Eigene Organisation auswählen

3. **Veranstaltungstyp** wählen:
   - **Ball** – Gesellschaftlicher Tanzabend
   - **Workshop** – Tanz- oder Musikworkshop (mit Schwierigkeitsgrad)
   - **Festival** – Mehrtägige Veranstaltung
   - **Session** – Offene Jam-Session
   - **Kombination** – Mehrere Typen in einer Veranstaltung (z. B. Workshop + Ball)

---

## 🎯 Veranstaltungsdetails ausfüllen

Nach den Grunddaten können weitere Informationen hinzugefügt werden:

### Beschreibung
- Formatierter Text mit **Markdown** (Überschriften, Listen, Links, Fett, Kursiv)
- Formatierter Text aus Word wird automatisch in Markdown umgewandelt
- **Tipp:** Kurze, prägnante Beschreibung mit den wichtigsten Informationen

### Preise
- Mehrere Preisstufen möglich:
  - Normalpreis
  - Ermäßigt
  - Frühbucher
  - Abendkasse
  - Spende
  - Kostenlos
- Für jede Stufe: Betrag und Beschreibung

### Buchungslink
- Link zu externem Ticketsystem (z. B. Eventbrite, Pretix)
- Wird prominent auf der Veranstaltungsseite angezeigt

### Tags
Dansal verwendet ein **flexibles Tag-System** statt starrer Ja/Nein-Felder:

| Kategorie | Werte | Beispiel |
|---|---|---|
| **Format** | Ball, Fest Noz, Session, Konzert, Festival, Open Air, Workshop, Musikkurs | `bal-folk`, `fest-noz` |
| **Typ** | Tanz-Workshop, Musiker-Workshop | `dance-workshop`, `musician-workshop` |
| **Niveau** | Anfänger, Fortgeschrittene, Profis | `beginner`, `advanced` |

**Vorteile:**
- Eine Veranstaltung kann mehrere Tags gleichzeitig haben (z. B. `bal-folk` + `workshop`)
- Neue Tag-Werte können einfach hinzugefügt werden
- Keine starren Kategorien

### Bilder
- **Hauptbild**: Veranstaltungsplakat oder Foto (breites Format wird empfohlen)
- **Banner**: Wird in Sozialen Medien geteilt (automatisch generiert falls nicht vorhanden)
- Unterstützte Formate: AVIF (empfohlen), JPEG
- Maximale Größe: Wird automatisch verkleinert

---

## 👥 Musiker und Anleiter zuordnen

### Musiker hinzufügen

1. Im Veranstaltung-Formular den Abschnitt **Musiker** öffnen
2. existing Musiker aus der Datenbank suchen
3. Neuen Musiker anlegen, falls nicht vorhanden:
   - **Name, Kurzname**
   - **Beschreibung, Genre, Biografie**
   - **Mitglieder, Alben**
   - **Externe IDs**: MusicBrainz, Wikidata, Discogs
   - **Social Media & Streaming**: Mastodon, Instagram, Facebook, Soundcloud, Spotify, Deezer
   - **Foto / Profilbild**

**Automatische Verknüpfung:** Musiker werden mit **MusicBrainz** verknüpft – Änderungen dort (z. B. neue Alben) können automatisch übernommen werden.

### Anleiter hinzufügen

Ähnlich wie Musiker, aber mit reduziertem Datensatz:
- **Name**
- **Biografie**
- **Profilbild**
- **Kontakt** (Webseite, E-Mail)
- **Social Media**

---

## 🕐 Zeitplan für Mehrtagesveranstaltungen

Falls eine Veranstaltung aus mehreren zeitlich getrennten Abschnitten besteht (z. B. Workshop am Nachmittag, Ball am Abend):

1. Im Veranstaltung-Formular den Abschnitt **Zeitplan** öffnen
2. Für jeden Programmpunkt:
   - **Zeitraum** (Start, Ende)
   - **Titel** (z. B. "Tanzworkshop Anfänger")
   - **Beschreibung**
   - **Musiker / Anleiter** (optional)
   - **Raum** (falls mehrere Räume vorhanden)

**Beispiel:**

```
Raum A:
- 14:00–15:30: Tanzworkshop (Anfänger) mit Band X
- 16:00–17:30: Tanzworkshop (Fortgeschritten) mit Band Y
- 20:00–01:00: Abendball mit Band Z

Raum B:
- 15:00–16:30: Technikkurs
- 17:00–18:30: Musikalitäts-Workshop
```

**Mehrtägige Veranstaltungen:** Für jeden Tag kann ein eigener Zeitplan angelegt werden.

---

## 🔄 Veranstaltungen verwalten – Tagesgeschäft

> 💡 **Tipp für mehrere Veranstaltungen**: Nutze [Multi-Select](Benutzer-Multi-Select), um mehrere Veranstaltungen gleichzeitig zu bearbeiten (z. B. absagen, Tags zuweisen, Organisation ändern).

### Veranstaltung bearbeiten

Jede Veranstaltung lässt sich jederzeit bearbeiten:
- Alle Felder können nachträglich geändert werden
- Änderungen werden sofort auf der öffentlichen Seite sichtbar
- **Ausnahme:** Datumänderungen erfordern eine erneute Bestätigung

### Veranstaltung absagen

1. Veranstaltung öffnen
2. Button **"Absagen"** klicken
3. Absagegrund eingeben (optional)
4. Bestätigen

**Effekt:**
- Veranstaltung bleibt in der Datenbank erhalten
- Wird mit einem **"Abgesagt"**-Hinweis angezeigt
- Besucher sehen die Absage auf der Veranstaltungsseite und auf der Karte

### Veranstaltung klonen

1. Veranstaltung öffnen
2. Button **"Klonen"** klicken
3. Neue Veranstaltung wird mit allen Daten der Originalveranstaltung erstellt
4. **Datum muss neu gesetzt werden** (wird automatisch geleert)

**Typischer Einsatz:** Wiederkehrende Veranstaltungen mit ähnlichem Ablauf (z. B. monatlicher Ball).

### Als Vorlage speichern

1. Veranstaltung öffnen
2. Button **"Als Vorlage speichern"** klicken
3. Vorlage wird gespeichert und kann für neue Veranstaltungen verwendet werden

**Typischer Einsatz:** Veranstaltungen mit immer gleichen Daten (z. B. immer derselben Ort, gleiche Preise).

### Zu Terminserie hinzufügen

1. Veranstaltung öffnen
2. Button **"Zur Serie hinzufügen"** klicken
3. Bestehende Serie auswählen oder neue erstellen

**Vorteile von Serien:**
- Gemeinsame Grunddaten (Ort, Beschreibung, Uhrzeit)
- Einzelne Termine können unabhängig voneinander verwaltet werden
- Magic-Link für gemeinsame Planung in einer Orga-Gruppe
- Einzelne Termine können abgesagt werden, ohne die gesamte Serie zu löschen

---

## 📥 Veranstaltungen importieren

Für Organisationen, die bereits ein anderes Kalendersystem nutzen.

### Manueller Import aus iCal/JSON-Feed

1. Navigiere zu **Veranstaltungen → Importieren** (`/admin/events/import`)
2. Quelle auswählen:
   - **Feed-URL** eingeben (z. B. `.ics`- oder `.json`-Link)
   - **Datei hochladen** (`.ics` oder `.json`)
3. Feed-Typ wählen: `iCal` oder `JSON`
4. Bei nicht-Admin-Rolle: **Organisation** auswählen
5. **Vorschau laden** – dansal zeigt alle gefundenen Termine an mit Status:
   - ✅ bereits vorhanden (abgewählt, um Duplikate zu vermeiden)
   - ⭐ neu (zum Import vorgeschlagen)
6. **Orte zuordnen:**
   - Automatisch erkannt: Ort übernehmen
   - Manuell zuordnen: anderen bestehenden Ort wählen
   - Als neuen Ort anlegen: Feed-Name als neuen Ort speichern
7. Gewünschte Termine auswählen und **Importieren** klicken

**Wichtig:**
- **Wiederkehrende Termine** (RRULE in iCal) werden automatisch in einzelne Vorkommen aufgeteilt
- **Aliase:** Manuelle Zuordnungen werden als Alias gespeichert → zukünftige Importe aus derselben Quelle erkennen den Ort automatisch
- Dies ist ein **manueller Import** – für automatische Synchronisation siehe [Benutzer-Organisationen](Benutzer-Organisationen)

---

## 🎯 Erweiterte Funktionen

### Veranstaltungsserien

Für regelmäßige Termine (z. B. wöchentliche Tanzabende):

- **Vorauswahl** von Ort, Beschreibung, Uhrzeit und weiteren Informationen
- **Tabellarische Übersicht** aller Termine in der Serie
- **Einfaches Hinzufügen** neuer Termine
- **Gemeinsame Planung** per Magic-Link mit Orga-Gruppe
- **Einfache Absage** einzelner Termine (z. B. bei Regen für Außenveranstaltungen)

### Wiederkehrende iCal-Veranstaltungen

Beim Import aus iCal-Feeds werden **wiederkehrende Termine** (RRULE) automatisch in einzelne, datierte Veranstaltungen aufgeteilt. Jede hat dann ein eigenes Datum und kann individuell bearbeitet werden.

---

**Weiter zu**: [Benutzer-Organisationen](Benutzer-Organisationen) | [Benutzer-Veranstaltungsorte](Benutzer-Veranstaltungsorte) | [Benutzer](Benutzer)
