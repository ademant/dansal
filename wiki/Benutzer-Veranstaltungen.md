---
nav_order: 4
---

# Veranstaltungen erstellen & verwalten

Als Nutzer lassen sich Veranstaltungen an unterschiedlichen Stellen erstellen, entweder komplett leer oder bereits vorausgefüllt:
- **Veranstaltungen**: Komplett leere Veranstaltungen
- **Organisationen**: Bereits der entsprechenden Organisation zugeordnet
- **Veranstaltungsort**: Bereits dem Ort zugeordnet
- **Vorlage**: In der Vorlage gespeicherten Werte sind vorausgefüllt
- **Klonen**: Eine Veranstaltung lässt sich klonen (ohne Datum, das muss zwingend neu gesetzt werden)
- **Terminserie**: Einer Serie lassen sich unkompliziert neue Termine anhängen.

## ✨ Grundlegende Veranstaltung anlegen

1. Navigiere zu **Veranstaltungen → Neue Veranstaltung erstellen**
2. Grunddaten ausfüllen:
   - **Titel**: Klarer, beschreibender Name
   - **Start-/Enddatum & Uhrzeit**: Dauer der Veranstaltung
   - **Ort**: Bestehenden Ort auswählen oder neuen anlegen
   - **Organisation**: Eigene Organisation auswählen
3. **Veranstaltungstyp** wählen:
   - **Ball** (gesellschaftlicher Tanzabend)
   - **Workshop** (mit Schwierigkeitsgrad: Anfänger/Fortgeschritten/Experte)
   - **Festival** (mehrtägige Veranstaltung)
   - **Kombination** (z. B. Workshop + Ball)

## 📋 Veranstaltungsdetails

- **Beschreibung**: Formatierter Text: Markdown wird verwendet. Formatierter Text aus Word wird in Markdown umgewandelt
- **Preise**: Mehrere Preisstufen möglich (kostenlos, Spende, Frühbucher, regulär, Abendkasse usw.)
- **Buchungslink**: Link zum Ticketsystem
- **Tags**: Format-, Typ- und Level-Tags
- **Bild**: Veranstaltungsplakat oder Foto hochladen (Breites Format wird angezeigt)

## 🕒 Erweiterte Optionen: Zeitplan (mehrere Programmpunkte)

Beispiel für eine Veranstaltung mit mehreren Räumen:

```
Raum A:
- 14:00–15:30: Workshop (Anfänger) mit Band X
- 16:00–17:30: Workshop (Fortgeschritten) mit Band Y
- 20:00–01:00: Abendball mit Band Z

Raum B:
- 15:00–16:30: Technikkurs
- 17:00–18:30: Musikalitäts-Workshop
```

## 🎻 Musiker zur Veranstaltung hinzufügen

- Musiker aus der Datenbank suchen und hinzufügen
- Verlinkung zum MusicBrainz-Profil des Musikers
- Social-Media-Links pro Musiker hinzufügen

## 🛠️ Veranstaltung verwalten

- **Bearbeiten**: Beliebige Details aktualisieren
- **Absagen**: Veranstaltung als abgesagt markieren (mit Hinweis sichtbar)
- **Duplizieren**: Eine Kopie der Veranstaltung erstellen
- **Terminserie**: Die Veranstaltung als Basis einer Terminserie verwenden oder einer Terminserie zuordnen

## 📥 Veranstaltungen aus iCal-/JSON-Feed importieren

Unter **Veranstaltungen → Importieren** (`/admin/events/import`) lassen sich mehrere Termine auf einmal aus einer externen Quelle übernehmen – nützlich, wenn ein Verein seine Termine bereits in einem anderen Kalendersystem pflegt.

**So funktioniert's:**
1. Quelle wählen:
   - **Feed-URL** eingeben (z. B. Link zu einem `.ics`-Kalender oder einer JSON-Feed-Adresse), **oder**
   - eine Datei hochladen (`.ics`- oder `.json`-Datei)
2. **Feed-Typ** wählen: `iCal` oder `JSON` (gängige Varianten wie Gancio-JSON werden automatisch erkannt)
3. Bei einer nicht-administrativen Rolle: die **Organisation** auswählen, der die importierten Termine zugeordnet werden sollen
4. **Vorschau laden** – dansal zeigt eine Liste aller im Feed gefundenen Termine an, inklusive Status pro Zeile:
   - bereits vorhanden (wird standardmäßig abgewählt, um Duplikate zu vermeiden)
   - neu (wird zum Import vorgeschlagen)
5. **Veranstaltungsorte zuordnen**: Für jeden im Feed vorkommenden Ortsnamen zeigt dansal an, ob bereits ein passender Ort in der Datenbank existiert. Du kannst:
   - den automatisch erkannten Ort übernehmen,
   - einen anderen bestehenden Ort manuell zuordnen, oder
   - den Feed-Namen unverändert als neuen Ort anlegen lassen

   Eine manuelle Zuordnung wird als **Alias** beim Ort gespeichert, sodass künftige Importe aus derselben Quelle automatisch erkannt werden.
6. Gewünschte Zeilen auswählen (oder „Alle auswählen“) und **Importieren** klicken – die ausgewählten Termine werden als neue Veranstaltungen angelegt
7. Zum Abschluss lässt sich dieser Feed auch auch regelmäßige Quelle für die Organisation abspeichern.

**Hinweise:**
- Wiederkehrende iCal-Termine (RRULE) werden dabei automatisch in einzelne Vorkommen aufgeteilt (siehe auch Abschnitt „Wiederkehrende iCal-Veranstaltungen“ unten)
- Dieser manuelle Import ist getrennt von der **automatischen Feed-Synchronisation** einer Organisation (siehe [Benutzer-Organisationen](Benutzer-Organisationen)) – dort wird ein Feed dauerhaft hinterlegt und regelmäßig automatisch abgerufen, während der Import hier ein einmaliger, von Hand bestätigter Vorgang ist

## 🚀 Erweiterte Funktionen

### Veranstaltungsserien
Regelmäßige Termine, wie Übungsabende, können in einer Veranstaltungsserie gruppiert werden, was deren Verwaltung vereinfacht:
- Vorauswahl eines Ortes, Beschreibung, Uhrzeit und weitere Informationen
- In einer Tabelle lassen sich dann einzelne Tage einfach zufügen. Wie bei einer Vorlage werden dann einzelne Felder vorausgefüllt.
- Über einen Magic-Link lässt sich die Veranstaltungsreihe in einer Orga-Gruppe teilen:
-- Diese Personen können die Beschreibung eines Termins ändern, um die Planung von Übungsabenden abzusprechen
-- Einzelne Termine können in dieser Tabelle einfach abgesagt werden, wenn z.B. Regen eine Außenveranstaltung erschwert.

### Wiederkehrende iCal-Veranstaltungen
Beim Import über iCal-Feeds werden wiederkehrende Termine (RRULE) automatisch in einzelne Vorkommen aufgeteilt.

---

**Weiter zu**: [Benutzer-Veranstaltungsorte](Benutzer-Veranstaltungsorte) | [Benutzer](Benutzer)

