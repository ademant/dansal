---
nav_order: 13
---

# Multi-Select: Mehrere Einträge gleichzeitig bearbeiten

Dansal unterstützt **Multi-Select** (Mehrfachauswahl) in vielen Listenansichten. Damit kannst du mehrere Einträge gleichzeitig auswählen und gemeinsam bearbeiten – das spart Zeit bei sich wiederholenden Aufgaben.

---

## 🎯 Multi-Select aktivieren

### Methode 1: Einzelne Einträge auswählen

1. Bewege den Mauszeiger über einen Eintrag in einer Liste
2. Ein **Checkbox** (Kontrollkästchen) erscheint am Anfang der Zeile
3. Klicke auf das Checkbox, um den Eintrag auszuwählen
4. Wiederhole für weitere Einträge

### Methode 2: Bereich auswählen (Shift+Klick)

1. Klicke auf das Checkbox des **ersten** Eintrags
2. Halte die **Shift-Taste** gedrückt
3. Klicke auf das Checkbox des **letzten** Eintrags
4. Alle Einträge dazwischen werden automatisch ausgewählt

### Methode 3: Alle Einträge auswählen

1. Oberhalb der Liste erscheint eine **Symbolleiste** beim ersten Klick auf ein Checkbox
2. Klicke auf **"Alle auswählen"** (oder "Select All")
3. Alle sichtbaren Einträge werden markiert

### Methode 4: Auswahl umkehren

1. Wähle einige Einträge manuell aus
2. Klicke auf **"Auswahl umkehren"** in der Symbolleiste
3. Alle nicht ausgewählten Einträge werden ausgewählt und umgekehrt

### Symbolleiste (wenn aktiv)

| Button | Funktion | Tastenkürzel |
|---|---|---|
| ✅ Alle auswählen | Markiert alle sichtbaren Einträge | Strg/Cmd + A |
| ❌ Auswahl aufheben | Hebt Auswahl aller Einträge auf | Esc |
| ↔️ Auswahl umkehren | Kehrt die aktuelle Auswahl um | – |
| 📋 Ausgewählte: N | Zeigt Anzahl ausgewählter Einträge | – |

---

## 📋 Unterstützte Multi-Select-Aktionen

Multi-Select funktioniert in verschiedenen Bereichen von dansal. Die verfügbaren Aktionen hängen vom aktuellen Kontext ab.

### 📅 Veranstaltungen-Liste

| Aktion | Beschreibung | Effekt |
|---|---|---|
| **Ausgewählte absagen** | Mehrere Veranstaltungen gleichzeitig als abgesagt markieren | Alle ausgewählten Veranstaltungen erhalten den Status "Abgesagt" |
| **Ausgewählte löschen** | Mehrere Veranstaltungen dauerhaft entfernen | Alle ausgewählten Veranstaltungen werden gelöscht (mit Bestätigung) |
| **Tags zuweisen** | Ein oder mehrere Tags zu allen ausgewählten Veranstaltungen hinzufügen | Alle ausgewählten Veranstaltungen erhalten die ausgewählten Tags |
| **Tags entfernen** | Ein oder mehrere Tags von allen ausgewählten Veranstaltungen entfernen | Alle ausgewählten Veranstaltungen verlieren die ausgewählten Tags |
| **Organisation zuweisen** | Alle ausgewählten Veranstaltungen einer anderen Organisation zuweisen | Alle Veranstaltungen werden der neuen Organisation zugeordnet |
| **Ort zuweisen** | Alle ausgewählten Veranstaltungen einem anderen Ort zuweisen | Alle Veranstaltungen werden dem neuen Ort zugeordnet |
| **Exportieren** | Ausgewählte Veranstaltungen als iCal oder JSON exportieren | Download einer Datei mit allen ausgewählten Veranstaltungen |

### 👥 Benutzer-Liste (nur Administratoren)

| Aktion | Beschreibung | Effekt |
|---|---|---|
| **Rolle ändern** | Rolle für alle ausgewählten Benutzer gleichzeitig ändern | Alle Benutzer erhalten die neue Rolle (Benutzer, Publisher, Administrator) |
| **Organisation zuweisen** | Alle ausgewählten Benutzer einer Organisation zuweisen | Alle Benutzer werden der neuen Organisation zugeordnet |
| **Ausgewählte löschen** | Mehrere Benutzerkonten gleichzeitig löschen | Alle ausgewählten Benutzer werden gelöscht (mit Bestätigung) |
| **Exportieren** | Liste der ausgewählten Benutzer exportieren | Download einer CSV-Datei mit Benutzerdaten |

### 🏢 Organisationen-Liste (nur Administratoren)

| Aktion | Beschreibung | Effekt |
|---|---|---|
| **Ausgewählte löschen** | Mehrere Organisationen gleichzeitig löschen | Alle ausgewählten Organisationen werden gelöscht (mit Bestätigung) |
| **Importieren** | Mehrere Organisationen aus einer Datei importieren | – |

### 📍 Orte-Liste

| Aktion | Beschreibung | Effekt |
|---|---|---|
| **Ausgewählte löschen** | Mehrere Orte gleichzeitig löschen | Alle ausgewählten Orte werden gelöscht (mit Bestätigung) |
| **Ausgewählte archivieren** | Mehrere Orte gleichzeitig archivieren | Alle ausgewählten Orte erhalten den Status "Archiviert" |
| **Exportieren** | Ausgewählte Orte als CSV exportieren | Download einer CSV-Datei mit Ortdaten |

---

## ⚠️ Sicherheitsmechanismen

Multi-Select-Aktionen, die Daten verändern oder löschen, sind mit mehreren Schutzmechanismen ausgestattet:

### 1. Bestätigungsdialog
- Jede löschende oder verändernde Aktion erfordert eine **explizite Bestätigung**
- Der Dialog zeigt:
  - **Anzahl** der betroffenen Einträge
  - **Typ** der Aktion (z. B. "3 Veranstaltungen löschen")
  - **Folgen** der Aktion

### 2. Undo-Funktion
- Viele Aktionen können **rückgängig gemacht** werden
- Ein **"Rückgängig"**-Button erscheint nach der Aktion
- Zeitfenster: Normalerweise **5 Minuten** nach der Aktion

### 3. Berechtigungsprüfung
- Multi-Select-Aktionen unterliegen den **gleichen Berechtigungen** wie Einzelaktionen
- Beispiel: Nur Administratoren können Benutzer löschen
- Beispiel: Nur Besitzer einer Organisation können deren Veranstaltungen bearbeiten

### 4. Rate Limiting
- Bei sehr großen Auswahlen (z. B. > 100 Einträge) wird eine **Warnung** angezeigt
-che Massenactionen erfordern eine zusätzliche **Sicherheitsabfrage**

---

## 💡 Tipps für effizientes Arbeiten

### 1. Filter vor der Auswahl
- Nutze die **Filterfunktion** oben in der Liste, um nur relevante Einträge anzuzeigen
- Beispiel: Filtere nach Datum, Organisation oder Typ, bevor du Multi-Select verwendest
- **Vorteil**: Weniger Einträge = weniger Fehlergefahr

### 2. Sortierung nutzen
- Sortiere die Liste nach dem Kriterium, das für deine Aktion relevant ist
- Beispiel: Nach Datum sortieren, um alle Veranstaltungen eines Monats auszuwählen
- **Tipp**: Halte die Shift-Taste gedrückt und klicke auf den ersten und letzten Eintrag

### 3. Auswahl überprüfen
- Vor dem Ausführen einer Aktion:
  - Prüfe die **Anzahl ausgewählter Einträge** in der Symbolleiste
  - Scrolle durch die Liste, um sicherzustellen, dass nur die gewünschten Einträge ausgewählt sind
  - Nutze **"Auswahl umkehren"**, falls du versehentlich zu viele ausgewähnt hast

### 4. Tastenkürzel

| Tastenkürzel | Funktion |
|---|---|
| Strg/Cmd + A | Alle sichtbaren Einträge auswählen |
| Esc | Auswahl aufheben |
| Shift + Klick | Bereich auswählen |
| Strg/Cmd + Klick | Einzelnen Eintrag zur Auswahl hinzufügen/entfernen |

---

## 🔍 Fehlersuche

| Problem | Lösung |
|---|---|
| Multi-Select funktioniert nicht | Prüfe, ob du die nötigen Berechtigungen hast |
| Checkboxes erscheinen nicht | Bewege den Mauszeiger über einen Eintrag oder klicke in die Liste |
| "Alle auswählen" Button fehlt | Wähle mindestens einen Eintrag aus, dann erscheint die Symbolleiste |
| Aktion nicht verfügbar | Nicht alle ausgewählten Einträge unterstützen diese Aktion |
| Zu viele Einträge ausgewählt | Nutze Filter, um die Auswahl einzugrenzen |

---

## 📚 Siehe auch

- [Veranstaltungen erstellen & verwalten](Benutzer-Veranstaltungen) – Einzelne Veranstaltungen bearbeiten
- [Administration](Benutzer-Administration) – Systemweite Einstellungen für Multi-Select
- [Organisationen verwalten](Benutzer-Organisationen) – Organisationsspezifische Einstellungen
