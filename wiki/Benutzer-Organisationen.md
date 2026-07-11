---
nav_order: 6
---

# Organisationen verwalten

Organisationen (Vereine oder wie auch immer ihr organisiert seid) können nur vom Administrator angelegt werden. Bei der Registrierung könnt ihr auch eine neue Organisation beantragen. Die muss dann auch von einem Administrator genehmigt werden.

## 🎭 Organisation anlegen

1. Gehe zu **Organisationen → Neue Organisation**
2. Details der Organisation ausfüllen:
   - Name, Beschreibung
   - Website, Social-Media-Links
   - Kontakt-E-Mail
   - Logo/Bild

## 📌 Funktionen der Organisation

- **Mehrere Veranstaltungsorte**: Alle von der Organisation genutzten Orte zuordnen
- **iCal-Feeds**: Automatischen Veranstaltungsimport einrichten
- **Mitglieder**: Benutzer hinzufügen, die Veranstaltungen für diese Organisation erstellen/verwalten dürfen

## 🔄 Automatische Feed-Synchronisation einrichten

Unter **Quellen → Neue Quelle** (`/admin/fetchurls/new`) richtest du einen dauerhaften Feed ein, der regelmäßig automatisch abgerufen wird – im Unterschied zum einmaligen, von Hand bestätigten Import (siehe [Benutzer-Veranstaltungen](Benutzer-Veranstaltungen)).

**Einrichtung:**
1. **URL** der Quelle eingeben (lässt sich nach dem Anlegen nicht mehr ändern)
2. **Typ** wählen: `iCal`, `RSS` oder `JSON` (gängige Varianten wie Gancio-JSON werden automatisch erkannt)
3. **Tags**: Tags eingeben, die automatisch auf alle aus diesem Feed importierten Veranstaltungen angewendet werden
4. **Organisation** auswählen, der importierte Veranstaltungen zugeordnet werden (als Nicht-Administrator nur eigene Organisationen)
5. **Tanzstile** per Checkbox zuordnen
6. Optional eine **Vorlage** zur Feldzuordnung wählen und den **Vorlage-Modus** festlegen:
   - **Feed gewinnt**: Vorlage füllt nur fehlende Angaben auf
   - **Vorlage gewinnt**: Vorlage überschreibt z. B. den Zeitplan aus dem Feed
7. Speichern

**Wie der automatische Abruf funktioniert:**
- Es gibt **keine pro-Quelle einstellbare Abruffrequenz** – alle eingerichteten Quellen werden systemweit **stündlich** automatisch neu abgerufen
- In der Quellenliste siehst du zu jeder Quelle den Zeitpunkt des letzten Abrufs sowie das Ergebnis (Anzahl importierter Termine oder eine Fehlermeldung)
- Über den **„Jetzt ausführen“**-Button lässt sich ein Abruf auch sofort manuell anstoßen, ohne auf den nächsten automatischen Lauf zu warten
- **Wichtig**: Über diesen automatischen Weg importierte Veranstaltungen werden **sofort veröffentlicht** – es gibt hier keinen Freigabe-Schritt wie beim manuellen Import. Quellen sollten daher nur eingerichtet werden, wenn der Inhalt des Feeds vertrauenswürdig ist

## 📐 Vorlagen (Templates)

Eine **Vorlage** speichert die festen Standardangaben einer Veranstaltung (Ort, Preise, Tags, Tanzstile, Zeitplan usw.), damit diese beim automatischen Feed-Import wiederverwendet werden können – nützlich, wenn ein Veranstaltungsort z. B. ein festes wöchentliches Programm hat, das die externe Feed-Quelle nicht zuverlässig oder vollständig liefert.

**Vorlage erstellen:**
1. Eine bestehende Veranstaltung öffnen, die als Vorlage dienen soll
2. „Als Vorlage speichern“ wählen und einen Namen vergeben
3. Die aktuellen Angaben der Veranstaltung (Zeiten, Ort, Organisation, Preise, Tags, Tanzstile, Speisen/Getränke, Zeitplan usw.) werden als Vorlage übernommen

**Vorlage einer Quelle zuweisen:**
1. Unter **Quellen bearbeiten** die gewünschte Vorlage auswählen
2. Festlegen, welche Feldgruppen aus der Vorlage übernommen werden sollen (z. B. nur Ort und Zeitplan, oder auch Preise/Tags)
3. **Vorlage-Modus** wählen:
   - **Feed gewinnt** (`fetch_master`): Die Vorlage füllt nur Felder auf, die der Feed leer lässt. Ein Zeitplan aus der Vorlage wird nur übernommen, wenn die importierte Veranstaltung noch gar keinen eigenen Zeitplan hat
   - **Vorlage gewinnt** (`template_master`): Die ausgewählten Felder werden immer von der Vorlage überschrieben, unabhängig davon, was im Feed steht – ein vorhandener Zeitplan wird dabei vollständig durch den der Vorlage ersetzt
4. Die Zuordnung von Orten erfolgt dabei automatisch über eine Ähnlichkeitsprüfung der Ortsnamen aus Feed und Vorlage

**Hinweis**: Vorlagen werden getrennt von den eigentlichen Veranstaltungsdaten verwaltet, sodass eine einmal erstellte Vorlage für mehrere Quellen wiederverwendet werden kann.

---

**Weiter zu**: [Benutzer-Musiker](Benutzer-Musiker) | [Benutzer](Benutzer)

