---
nav_order: 5
---

# Anmeldung – Schritt-für-Schritt-Anleitung

Diese Seite zeigt, wie du dich bei dansal anmeldest. Für die Registrierung eines neuen Kontos siehe [Registrierung](Benutzer-Registrierung).

---

## Anmeldeseite

![Anmeldeseite](images/screenshots/02_Login.png)

Die Anmeldeseite bietet mehrere Möglichkeiten:

| Schaltfläche | Methode |
|---|---|
| **Sofort anmelden** | Passkey — der Browser fragt nach Fingerabdruck, Gesicht oder PIN |
| **E-Mail + Passwort** | Klassische Anmeldung |
| **Mit Passkey anmelden** | Passkey manuell auswählen |
| **Per E-Mail** | Passwortloser Magic Link per E-Mail |
| **Per Telegram** | Magic Link über Telegram |
| **Per Matrix** | Magic Link über Matrix |

---

## Übersicht der Anmeldemethoden

Dansal unterstützt mehrere Anmeldemethoden, die du kombinieren kannst:

### 1. Passkey (empfohlen)
- **Was ist das?** Ein kryptografischer Schlüssel, der sicher auf deinem Gerät gespeichert wird (Fingerabdruck, Gesichtserkennung oder PIN)
- **Vorteile**: Kein Passwort nötig, sehr sicher, schnell
- **Hinweis**: Jedes Gerät braucht seinen eigenen Passkey

### 2. Passwort
- **Klassische Anmeldung** mit Benutzername und Passwort
- Kann mit anderen Methoden kombiniert werden

### 3. Magic Link (Passwortlos)
- **Per E-Mail**: Einmaliger Anmeldelink, 15 Minuten gültig
- **Per Telegram**: Login-Link über Telegram
- **Per Matrix**: Login-Link über Matrix
- **Vorteile**: Kein Passwort nötig, ideal für Geräte ohne Passkey

> 💡 **Tipp**: Nutze Passkey als primäre Methode und Magic Link als Backup. Du kannst mehrere Methoden gleichzeitig aktivieren.

---

## Anmeldung per Magic Link (E-Mail)

Hier die Beschreibung exemplarisch bei Verwendung einer E-Mail-Adresse. Die anderen Methoden (Telegram / Matrix) sind vergleichbar.

### Schritt 1 — E-Mail eingeben und „Per E-Mail" klicken

![E-Mail eingeben](images/screenshots/02_Login_02_email_magic_link.png)

Trage deine E-Mail-Adresse oder deinen Anzeigenamen ein und klicke auf **„Per E-Mail"**.

---

### Schritt 2 — Bestätigung

![Magic Link angefordert](images/screenshots/02_Login_03_magiclinksent.png)

Die Seite bestätigt, dass eine passwortlose Anmeldung per E-Mail unterwegs ist.

---

### Schritt 3 — Link aus der E-Mail öffnen

![Magic-Link-E-Mail](images/screenshots/02_Login_04_magiclinkmail.png)

Klicke auf den Link in der E-Mail. Er ist **15 Minuten** gültig und kann nur **einmal** verwendet werden.

---

### Schritt 4 — Erfolgreich angemeldet

![Dashboard nach Login](images/screenshots/02_Login_05_successedlogin.png)

Du landest auf deinem Dashboard mit der Übersicht deiner Veranstaltungen.

---

## Benutzer-Einstellungen

Nach der Anmeldung kannst du in deinen Profileinstellungen weitere Anmeldemethoden hinzufügen und dein Konto verwalten.

![Benutzer-Einstellungen](images/screenshots/01_Registration_01_JoinOrg_12_userpage.png)

Unter **Einstellungen** (Klick auf dein Avatar-Symbol) kannst du:

- Anzeigenamen und Kurzbeschreibung anpassen
- Telegram, Matrix, Mastodon verknüpfen
- Weitere Passkeys hinzufügen oder löschen
- TOTP (Authenticator-App) einrichten
- Passwort setzen oder ändern
- API-Schlüssel verwalten
- Das Konto dauerhaft löschen

Bis jetzt ist nur Deine E-Mail-Adresse gespeichert. Dies muss keine Adresse sein, welche Du für tägliche Arbeiten verwendest. Es wird davon abgeraten, ein Passwort zu verwenden.

Auf den Profileinstellungen kannst Du einen Nutzernamen festlegen. Nutzername wird aktuell nur verwendet, um anderen angemeldeten Personen zu zeigen, ob Du eine Veranstaltung verändert hast.

---

## Passkey auf einem zweiten Gerät einrichten

Passkey ist eine moderne und sichere Methode, mit der Du nur mit einem ausgewählten Gerät (z. B. Deinem Smartphone) anmelden kannst. Mit einem zweiten Gerät musst Du eine eigene Passkey einrichten.

### Schritt 1: Anmelden mit Magic Link

![Anmeldeseite](images/screenshots/02_Login.png)

Auf der Anmeldeseite trage Deine E-Mail-Adresse ein und gehe auf den Punkt "Per E-Mail". Ein Login-Link wird Dir per E-Mail zugesandt. Damit wirst Du angemeldet.

### Schritt 2: Gehe zu Einstellungen

![Benutzer-Einstellungen](images/screenshots/01_Registration_01_JoinOrg_12_userpage.png)

In den Nutzer-Einstellungen ist der Abschnitt **Passkeys**. Dort ist ein Button **Passkey hinzufügen**. Hiermit wird auf dem aktuellen Gerät ein neuer Passkey erzeugt.

### Schritt 3: zusätzliches Gerät freigeschaltet

Damit ist ein weiteres Gerät freigeschaltet, mit dem Du ohne Passwort Dich anmelden kannst.

> ⏱️ **Wichtig**: Der Passkey funktioniert nur auf dem Gerät, auf dem er erstellt wurde. Für jedes Gerät (Smartphone, Tablet, Laptop) musst du einen separaten Passkey einrichten.

> 💡 **Tipp**: Richte Passkeys auf mindestens zwei Geräten ein, um den Zugang nicht zu verlieren, falls ein Gerät nicht mehr verfügbar ist.

---

**Weiter zu**: [Registrierung](Benutzer-Registrierung) | [Veranstaltungen verwalten](Benutzer-Veranstaltungen)
