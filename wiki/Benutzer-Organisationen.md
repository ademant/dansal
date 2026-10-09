---
nav_order: 8
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
- **Feeds**: Automatischen Veranstaltungsimport über iCal, RSS oder JSON einrichten
- **Mitglieder**: Benutzer hinzufügen, die Veranstaltungen für diese Organisation erstellen/verwalten dürfen

## 🏠 Die Organisations-Übersicht

Öffnest du eine Organisation (**Organisationen** → auf den Namen klicken), erreichst du ihre Übersichtsseite. Von hier aus lässt sich fast alles rund um die Organisation direkt erledigen:

- **Orte zuweisen und neu anlegen**: Beim Bearbeiten der Organisation gibt es den Abschnitt **Orte**, in dem vorhandene Veranstaltungsorte der Organisation zugeordnet oder wieder entfernt werden. Über **+ Neuer Veranstaltungsort** legst du direkt einen neuen, der Organisation zugeordneten Ort an.
- **Veranstaltungen direkt für einen Ort erstellen**: In der Übersicht steht bei jedem zugeordneten Ort der Button **+ Veranstaltung** – er öffnet das Veranstaltungsformular mit bereits gesetztem Ort und bereits gesetzter Organisation. Auch oben über **+ Veranstaltung** kannst du eine Veranstaltung für die Organisation anlegen.
- **Veranstaltungen einem anderen Ort zuweisen**: In der Veranstaltungsliste der Organisation (und in der allgemeinen Veranstaltungsliste) kannst du eine oder mehrere Veranstaltungen markieren und über die Sammelbearbeitung (**Ort**) auf einmal einem anderen Ort zuordnen.
- **Vorlagen und Terminserien verwalten**: Die Übersicht zeigt die **Zugeordneten Vorlagen** und **Zugeordneten Terminserien**. Beide lassen sich hier neu anlegen (**+ Neue Vorlage** bzw. **+ Neue Terminserie**) und über **+ Veranstaltung** direkt für die Erstellung einer Veranstaltung nutzen.

## 🔗 Medien-Links

Auch eine Organisation kann eine Liste externer Links pflegen, z. B. ein Vorstellungsvideo, Fotoalben oder Pressematerial. Die Bedienung ist dieselbe wie bei Musikern (siehe [Benutzer-Musiker](Benutzer-Musiker)):

1. Die Organisation bearbeiten und den Abschnitt **Medien-Links** öffnen
2. **+ Link hinzufügen**, Art wählen, optional Titel eingeben, Link (`https://…`) einfügen
3. Mit **↑ / ↓** sortieren, mit **×** entfernen, speichern

Die Links erscheinen als einfache Links auf der Organisationsseite; es wird nichts eingebettet oder beim Seitenaufruf nachgeladen. Bis zu 20 Links pro Organisation. Auch Mitglieder der Organisation (nicht nur Administratoren) dürfen die Links pflegen.

## 🔄 Automatische Feed-Synchronisation einrichten

Unter **Feeds → Hinzufügen** (`/admin/fetchurls/new`) richtest du einen dauerhaften Feed ein, der regelmäßig automatisch abgerufen wird – im Unterschied zum einmaligen, von Hand bestätigten Import (siehe [Benutzer-Veranstaltungen](Benutzer-Veranstaltungen)).

**Einrichtung:**
1. **URL** der Quelle eingeben (lässt sich nach dem Anlegen nicht mehr ändern)
2. **Typ** wählen: `iCal`, `RSS` oder `JSON (Auto-Erkennung)` – gängige Varianten wie Gancio- oder Folkdance-JSON werden automatisch erkannt – sowie `Kufer (VHS)`, `jCal` und `Event-Seite (JSON-LD)`
3. **Organisation** auswählen, der importierte Veranstaltungen zugeordnet werden (als Nicht-Administrator nur eigene Organisationen)
4. **Tags** eingeben, die automatisch auf alle aus diesem Feed importierten Veranstaltungen angewendet werden
5. Optional eine **Vorlage** auswählen und den **Vorlage-Modus** festlegen:
   - **Feed dominiert (Vorlage füllt Lücken)**: Vorlage füllt nur fehlende Angaben auf
   - **Vorlage dominiert (überschreibt Feed)**: Vorlage überschreibt z. B. den Zeitplan aus dem Feed
6. Speichern

Beim späteren **Bearbeiten** einer Quelle kommen weitere Angaben hinzu: **Tanzstile** per Checkbox, **Quellenangabe**, **Lizenz**, **Nutzungsbedingungen-Link** und ein **Opt-out**. Mit **Testen** lässt sich der Feed außerdem vorab testweise einlesen.

**Wie der automatische Abruf funktioniert:**
- Es gibt **keine pro Feed einstellbare Abruffrequenz** – alle eingerichteten Feeds werden systemweit **stündlich** automatisch neu abgerufen
- In der Feed-Liste siehst du zu jedem Feed den Zeitpunkt des letzten Abrufs sowie das Ergebnis (Anzahl importierter Termine oder eine Fehlermeldung)
- Über den **„Abrufen“**-Button lässt sich ein Abruf auch sofort manuell anstoßen, ohne auf den nächsten automatischen Lauf zu warten
- **Wichtig**: Über diesen automatischen Weg importierte Veranstaltungen werden **sofort veröffentlicht** – es gibt hier keinen Freigabe-Schritt wie beim manuellen Import. Feeds sollten daher nur eingerichtet werden, wenn ihr Inhalt vertrauenswürdig ist

**Von Besuchern vorgeschlagene Feeds:** Ein Besucher kann ganz ohne Konto einen neuen Feed für eure Organisation vorschlagen (`/feeds/suggest`, siehe Besucher-Guide). Solche Vorschläge erscheinen unter **Feeds → N ausstehende Feed-Vorschläge** (`/admin/fetchurl-suggestions`) und können von jedem aktiven Mitglied der betroffenen Organisation genehmigt oder abgelehnt werden – nicht nur von Administratoren. Bei Genehmigung wird die Quelle wie eine selbst angelegte behandelt und sofort erstmalig abgerufen.

## 📐 Vorlagen (Templates)

Eine **Vorlage** speichert die festen Standardangaben einer Veranstaltung (Ort, Preise, Tags, Tanzstile, Zeitplan usw.), damit diese beim automatischen Feed-Import wiederverwendet werden können – nützlich, wenn ein Veranstaltungsort z. B. ein festes wöchentliches Programm hat, das die externe Feed-Quelle nicht zuverlässig oder vollständig liefert.

**Vorlage erstellen:**
1. Eine bestehende Veranstaltung öffnen, die als Vorlage dienen soll
2. „Als Vorlage speichern“ wählen und einen Namen vergeben
3. Die aktuellen Angaben der Veranstaltung (Zeiten, Ort, Organisation, Preise, Tags, Tanzstile, Speisen/Getränke, Zeitplan usw.) werden als Vorlage übernommen

**Vorlage einer Quelle zuweisen:**
1. Die Quelle **bearbeiten** und die gewünschte Vorlage auswählen
2. **Vorlage-Modus** wählen:
   - **Feed dominiert (Vorlage füllt Lücken)** (`fetch_master`): Die Vorlage füllt nur Felder auf, die der Feed leer lässt. Ein Zeitplan aus der Vorlage wird nur übernommen, wenn die importierte Veranstaltung noch gar keinen eigenen Zeitplan hat
   - **Vorlage dominiert (überschreibt Feed)** (`template_master`): Die Felder der Vorlage werden immer übernommen, unabhängig davon, was im Feed steht – ein vorhandener Zeitplan wird dabei vollständig durch den der Vorlage ersetzt
3. Die Zuordnung von Orten erfolgt dabei automatisch über eine Ähnlichkeitsprüfung der Ortsnamen aus Feed und Vorlage

Bei der Quellen-Zuweisung werden immer **alle** Felder der Vorlage herangezogen. Sollen nur **bestimmte Feldgruppen** übernommen werden (z. B. nur Ort und Zeitplan, oder auch Preise/Tags), lässt sich eine Vorlage stattdessen gezielt auf bereits vorhandene Veranstaltungen anwenden: In der Veranstaltungsliste Veranstaltungen markieren und **Vorlage anwenden** wählen (`/admin/events/template-assign`) – dort werden die Vorlage und die gewünschten Feldgruppen (Zeiten, Organisation, Ort, Veranstaltungstyp, Links, Preis, Tags, Tänze, Essen & Trinken, Buchung, Programm) ausgewählt.

**Hinweis**: Vorlagen werden getrennt von den eigentlichen Veranstaltungsdaten verwaltet, sodass eine einmal erstellte Vorlage für mehrere Quellen wiederverwendet werden kann.

---

**Weiter zu**: [Benutzer-Musiker](Benutzer-Musiker) | [Benutzer](Benutzer)

