---
nav_order: 13
---

# Multi-Select: Mehrere Einträge gleichzeitig bearbeiten

Dansal unterstützt **Mehrfachauswahl** in den Verwaltungsansichten. Damit kannst du mehrere Einträge gleichzeitig auswählen und gemeinsam bearbeiten – das spart Zeit bei sich wiederholenden Aufgaben.

---

## 🎯 Einträge auswählen

Vor jeder Zeile steht ein **Kontrollkästchen**. Klicke darauf, um einen Eintrag auszuwählen. Wiederhole das für weitere Einträge.

Im Kopf der Tabelle steht ein Kontrollkästchen, mit dem du **alle Einträge der Liste** auswählst bzw. die Auswahl wieder aufhebst.

Sobald etwas ausgewählt ist, zeigt die Aktionsleiste über bzw. neben der Tabelle die Anzahl der ausgewählten Einträge an. Erst dann stehen die Aktionen zur Verfügung.

---

## 📋 Aktionen je Liste

Welche Aktionen du siehst, hängt von deiner Rolle und vom Bereich ab.

### 📅 Veranstaltungen: Verwaltungsansicht (`/admin/events/maintenance`)

| Aktion | Beschreibung |
|---|---|
| **Ort zuweisen** | Alle ausgewählten Veranstaltungen einem anderen Ort zuordnen |
| **Organisation zuweisen** | Alle ausgewählten Veranstaltungen einer Organisation zuordnen |
| **Musiker / Anleiter hinzufügen** | Eine Person zu allen ausgewählten Veranstaltungen hinzufügen |
| **Terminserie** | Alle ausgewählten Veranstaltungen einer Terminserie zuordnen |
| **Tags, Tanzstile** | Pro Tag bzw. Tanzstil: ✓ hinzufügen, □ entfernen, – unverändert lassen |
| **Keine Live-Musik** | Ja (Aufnahme, DJ, Übungsabend) oder Nein (Live-Musik) setzen – siehe unten |
| **Zusammenführen** | Duplikate, die z. B. durch einen Import entstanden sind, zu einer Veranstaltung zusammenführen |
| **Veröffentlichen** | Die ausgewählten Veranstaltungen sichtbar machen |
| **Absagen** | Die ausgewählten Veranstaltungen als abgesagt markieren |
| **Quelle erneut prüfen** | Die Herkunftsquelle der ausgewählten Veranstaltungen neu abrufen |

Die Aktionen **Veröffentlichen**, **Absagen** und **Zusammenführen** fragen vor dem Ausführen nach.

### 📅 Veranstaltungen: Liste (nur Administratoren)

Auf der Veranstaltungsliste und auf den Dashboards von Organisationen, Orten, Musikern und Anleitern gibt es zusätzlich **Löschen**. Gelöschte Veranstaltungen sind endgültig weg; die Aktion fragt vorher nach.

### 🏢 Organisationen, Orte, Benutzer (nur Administratoren)

| Liste | Aktionen |
|---|---|
| Benutzer | Rolle ändern, Organisation zuweisen, Löschen |
| Organisationen | Löschen, Importieren |
| Orte | Löschen, Archivieren |

---

## 🎧 Keine Live-Musik

Eine Veranstaltung ohne Musiker kann ausdrücklich als **keine Live-Musik** gekennzeichnet werden (Aufnahme, DJ oder Übungsabend). Fehlt beides, bleibt die Musik ungeklärt.

Bei der Mehrfachauswahl gilt:
- Wenn du **Keine Live-Musik: Ja** setzt, und eine ausgewählte Veranstaltung Musiker hat, oder du **einen Musiker hinzufügst**, und eine ausgewählte Veranstaltung ist als keine Live-Musik gekennzeichnet, erscheint ein Hinweis mit der Anzahl betroffener Veranstaltungen.
- Du wählst dann:
  - **Überspringen** (Standard): Diese Veranstaltungen bleiben unverändert. Du kannst sie danach einzeln bearbeiten.
  - **Überschreiben**: Bei *Keine Live-Musik* werden die Musiker dieser Veranstaltungen entfernt. Beim Hinzufügen eines Musikers wird die Markierung *keine Live-Musik* entfernt.
  - **Abbrechen**: Es passiert nichts.

---

## 🔍 Filter vor der Auswahl

Die Verwaltungsansicht von Veranstaltungen bietet Filter, die du vor der Auswahl setzt, damit nur passende Einträge in der Liste stehen:
- **Organisation** und **Ort**
- **Stadt**
- **Datum** (von / bis)
- **Musiker** und **Anleiter** (mehrere möglich; es werden Veranstaltungen gezeigt, bei denen mindestens eine der gewählten Personen eingetragen ist). Zeitplan-Einträge zählen dabei nicht.
- **Nicht veröffentlicht** bzw. **mit Quelle** (Veranstaltungen aus einem Import)

Die Filter wirken auf der Server-Seite. Die Liste ist damit vollständig, auch bei vielen Veranstaltungen. Mit **CSV exportieren** lädst du genau die gefilterten Veranstaltungen als Tabelle herunter. Die Datei nutzt das Trennzeichen `|` und ein UTF-8-Kodierungszeichen, damit sie in Tabellenkalkulationen (z. B. LibreOffice Calc) richtig geöffnet wird.

---

## ⚠️ Sicherheitsmechanismen

- **Bestätigungsdialog:** Löschende und markierende Aktionen fragen vorher nach.
- **Berechtigungsprüfung:** Mehrfachauswahl unterliegt denselben Rechten wie Einzelaktionen. Nur Administratoren können Benutzer löschen, und Organisationen können nur ihre eigenen Veranstaltungen bearbeiten.
- **Keine Rückgängig-Funktion:** Mehrfachaktionen lassen sich nicht rückgängig machen. Prüfe die Auswahl deshalb vorher.

---

## 💡 Tipps

1. **Filtern, dann auswählen:** Weniger Einträge bedeuten weniger Fehlergefahr.
2. **Anzahl prüfen:** Kontrolliere vor dem Ausführen die angezeigte Anzahl ausgewählter Einträge.
3. **Im Zweifel einzeln:** Bei Konflikten wie *keine Live-Musik* ist das Überspringen die sichere Wahl.

---

## 🔍 Fehlersuche

| Problem | Lösung |
|---|---|
| Kein Kontrollkästchen sichtbar | Du hast für diese Liste keine Bearbeitungsrechte |
| Aktion fehlt | Nicht alle Aktionen gibt es in jeder Liste (siehe oben); prüfe auch deine Rolle |
| Zu viele Einträge ausgewählt | Setze Filter, um die Auswahl einzugrenzen |
| Hinweis zu *keine Live-Musik* | Entscheide, ob du die betroffenen Veranstaltungen überspringst oder überschreibst |

---

## 📚 Siehe auch

- [Veranstaltungen erstellen & verwalten](Benutzer-Veranstaltungen) – Einzelne Veranstaltungen bearbeiten
- [Daten pflegen](Benutzer-Pflege) – Verwaltungsansichten von Orten und Organisationen
- [Administration](Benutzer-Administration) – Systemweite Einstellungen
- [Organisationen verwalten](Benutzer-Organisationen) – Organisationsspezifische Einstellungen
