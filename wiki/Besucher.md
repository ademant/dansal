---
nav_order: 1
---

# Besucher-Guide – Tanzveranstaltungen finden

Willkommen bei dansal! Dieser Leitfaden hilft dir, Tanzveranstaltungen zu finden und mit der Community in Kontakt zu treten.

Deine Privatsphäre wird respektiert: Es werden keine persönlichen Daten gespeichert. Cookies werden nur verwendet, wenn du die Anzeigesprache änderst. Für manche Aktionen ist die Verifizierung einer E-Mail-Adresse oder eines Telegram-Kontos nötig, um Missbrauch zu vermeiden.

**Vorab:** Bitte kläre vorab, ob die Veranstaltung tatsächlich stattfindet. Diese Seite will die Veranstaltungen nur zentral vorstellen, wir sind nicht Veranstalter und kontrollieren auch nicht, ob Veranstaltungen durchgeführt werden.

## 🗺️ Veranstaltungen durchsuchen

### Interaktive Karte
- Die Startseite zeigt eine Karte mit kommenden Veranstaltungen
- Veranstaltungen werden als Pins angezeigt – anklicken für Details
- Mit dem Wochenkalender darunter kannst du den Zeitraum ändern
- Auf dem Smartphone: nach links/rechts wischen, um zwischen Wochen zu navigieren

### Veranstaltungsdetails
Jede Veranstaltungsseite kann folgende Infos enthalten:
- **Grunddaten**: Titel, Datum, Uhrzeit, Ort
- **Beschreibung**: Details, Ablauf, Preise
- **Veranstaltungsort**: Adresse, Barrierefreiheit, Parkplätze, Tanzboden-Infos
- **Veranstalter**: Kontaktinformationen und Social-Media-Links
- **Musiker**: Verlinkte Profile mit MusicBrainz-Links
- **Community-Pinnwand**: Mitfahrgelegenheiten, Unterkünfte, Ticketangebote und Fundbüro
- **Anfahrt**: Kleine Karte und Link, um über Openstreetmap Fahrtrouten ermitteln zu lassen.

### Veranstaltungen filtern
- **Nach Datum**: Wochenkalender oder die Liste kommender Veranstaltungen nutzen
- **Nach Ort**: Karte zoomen/verschieben oder nach Stadt filtern
- **Nach Typ**: Filter nach Ball, Workshop, Festival usw.

## 🏛️ Organisationen entdecken

Auf der Seite „Organisationen“ (`/organizations`) findest du eine Übersicht aller Veranstalter – auch ohne Benutzerkonto:
- Eine Karte zeigt die Standorte aller Organisationen
- Jede Organisation wird als Karte mit Name, Ort, Kurzbeschreibung sowie der Anzahl an Veranstaltungen und Veranstaltungsorten angezeigt
- Anklicken einer Karte öffnet die Detailseite der Organisation mit Kontaktinformationen, Social-Media-/Fediverse-Links, allen zugehörigen Veranstaltungsorten und kommenden Veranstaltungen

## 🚗 Community-Pinnwand

Jede Veranstaltung hat eine Pinnwand für:
- **Mitfahrgelegenheiten**: Fahrten zur Veranstaltung anbieten oder suchen
- **Unterkunft**: Mitwohngelegenheiten oder Unterkünfte vor Ort finden
- **Ticketbörse**: Tickets sicher kaufen/verkaufen
- **Fundbüro**: Nach der Veranstaltung verlorene Gegenstände melden oder Gefundenes anbieten

**So funktioniert's:**
1. Auf der Veranstaltungsseite „Auf Pinnwand posten“ anklicken
2. Kategorie wählen (Mitfahrgelegenheit, Unterkunft, Ticket, Fundbüro)
3. Nachricht und Kontaktdaten eingeben
4. Per E-Mail oder Telegram verifizieren
5. Dein Beitrag erscheint nach der Verifizierung
6. Der Link in der E-Mail-Adresse kann auch verwendet werden, um den Eintrag wieder zu löschen oder zu ändern.

**So antwortest Du:**
1. Auf eine Nachricht "Antworten" klicken
2. Nachricht und Kontakt-Daten eingeben
3. E-Mail verifizieren
4. Die Nachricht mit Kontaktdaten werden erst jetzt weitergeleitet

## ➕ Veranstaltung vorschlagen

Auch ohne Benutzerkonto kannst du eine fehlende Veranstaltung vorschlagen, damit sie auf dansal erscheint.

**So funktioniert's:**
1. Über „Veranstaltung vorschlagen“ in der Navigation die Seite `/events/suggest` öffnen
2. Entweder das Formular von Hand ausfüllen (Titel, Beschreibung, Datum/Uhrzeit, Ort, Tags, Tänze, Speisen/Getränke, Link, E-Mail-Adresse) oder eine `.ics`-/`.json`-Datei hochladen, deren Termine zur Vorschau eingelesen werden
3. Vorschlag absenden – die Administratoren werden direkt benachrichtigt und der Vorschlag ist sofort für sie zur Prüfung sichtbar, unabhängig von einer E-Mail-Bestätigung
4. Ist E-Mail-Versand eingerichtet, erhältst du zusätzlich einen Link per E-Mail, über den du den Vorschlag später jederzeit selbst einsehen oder bearbeiten kannst – ein Klick darauf ist aber keine Voraussetzung dafür, dass der Vorschlag bei den Administratoren ankommt
5. Ein Administrator prüft den Vorschlag und veröffentlicht ihn – bis dahin ist die Veranstaltung **nicht öffentlich sichtbar**

**Hinweise:**
- Die Funktion ist nur verfügbar, wenn die Instanz E-Mail- oder Telegram-Benachrichtigungen konfiguriert hat – ist beides nicht eingerichtet, ist die Seite nicht erreichbar
- Zum Schutz vor Spam gibt es ein Rate-Limit pro IP-Adresse sowie automatische Bot-Erkennung
- Eine angegebene E-Mail-Adresse wird nur zur Verifizierung verwendet (siehe Abschnitt „Datenspeicherung“ unten)

## 🔄 Feed vorschlagen

Kennst du einen Verein oder eine Location mit eigenem Veranstaltungskalender (iCal- oder JSON-Feed), der noch nicht auf dansal erscheint? Auch das lässt sich ganz ohne Benutzerkonto vorschlagen – anders als beim einzelnen Veranstaltungsvorschlag oben wird hier ein **ganzer, dauerhaft automatisch aktualisierter Feed** angemeldet.

**So funktioniert's:**
1. Über „Feed vorschlagen“ die Seite `/feeds/suggest` öffnen
2. E-Mail-Adresse angeben und entweder eine bestehende Organisation auswählen oder eine neue vorschlagen
3. Feed-URL eingeben – eine Vorschau prüft, ob sich der Feed einlesen lässt und zeigt die enthaltenen Veranstaltungen
4. Enthaltene Ortsangaben einzeln einem bestehenden Ort zuordnen oder mit Adresse/Ort für einen neuen Ort ergänzen
5. Vorschlag absenden – auch hier landet er sofort bei den Administratoren (bzw. bei bereits aktiven Mitgliedern der gewählten Organisation) zur Prüfung
6. Bei Genehmigung wird die Organisation/der Ort bei Bedarf angelegt und die Veranstaltungen sofort erstmalig abgerufen

Auf der Bestätigungsseite und per E-Mail wird direkt ein Link angeboten, um ein Konto einzurichten und die Organisation bzw. den Feed künftig selbst zu verwalten.

**Hinweise:** wie beim Veranstaltungsvorschlag ist die Funktion nur verfügbar, wenn die Instanz E-Mail- oder Telegram-Benachrichtigungen konfiguriert hat, und ebenso mehrschichtig gegen Missbrauch abgesichert (Rate-Limit, Bot-Erkennung).

## 🌍 Mehrsprachigkeit

dansal unterstützt 12 Sprachen. Sprache ändern über:
- Sprachauswahl in der Navigationsleiste (wird in einem Cookie gespeichert)
- Automatische Erkennung der Browsersprache (für neue Besucher)

## 📱 Mobile Nutzung

dansal ist vollständig responsiv:
- **Wischgesten**: Wochen per Wischen navigieren
- **Touch-freundlich**: Große Schaltflächen und Bedienelemente
- **Dunkelmodus**: Automatisch oder manuell umschaltbar

## Datenspeicherung
### Cookies
Cookies werden nur benutzt, wenn die Spracheinstellung dauerhaft geändert werden soll. Beim Ändern der Sprache kommt eine Abfrage zur Speicherung eines Cookies. Wird dies verweigert, ist die Umstellung der Sprache nur für diese Sitzung gültig. Bei der nächsten Sitzung wird die Sprache dargestellt, die der Browser anfragt.

### E-Mail-Adresse zur Verifizierung
Für einzelne Aktionen wird eine Verifizierung verlangt. Dazu wird an eine E-Mail-Adresse ein Link geschickt, um die Aktion zu bestätigen. Mit Verifizierung wird die E-Mail-Adresse sofort gelöscht (Ausnahme Pinnwand).

Wird die Verifizierung nicht innerhalb eines bestimmten Zeitraums durchgeführt, wird die Aktion mitsamt der Kontakt-Daten gelöscht

### Kontakt-Daten für Pinnwand
Um Nachrichten auf die Pinnwand zu setzen, werden Kontaktdaten erwartet. Neben einem Namen auch E-Mail-Adresse. Beides müssen nicht Realnamen sein, sondern gerne Bezeichnungen, unter denen ihr in der Szene bekannt seid. Die E-Mail-Adresse wird nicht öffentlich dargestellt, sondern nur gespeichert, um Antworten weiterleiten zu können.

### IP-Adressen
Es gibt einige Situationen, in denen IP-Adressen zum Schutz vor Mißbrauch gespeichert werden. Erfolgen zu viele Anfragen von einer IP-Adresse, wird diese von der Firewall blockiert. Ansonsten erfolgt keine Speicherung der IP-Adresse.

---

**Brauchst du Hilfe?** Kontaktiere die Veranstalter direkt über die Angaben auf der Veranstaltungsseite.

**Fehler gefunden?** Melde es auf [GitHub](https://github.com/ademant/dansal/issues)

**Möchtest Du mithelfen?** Kontaktiere den Betreiber dieser Seite.
