# Benutzerregistrierung in dansal-web

## Übersicht
Diese Dokumentation beschreibt den Registrierungsprozess für neue Benutzer in der dansal-web Anwendung.

## Voraussetzungen
- Zugriff auf die dansal-web Instanz (z.B. https://dansal.example.com)
- Gültige E-Mail-Adresse
- Einladungslink (falls Registrierung nur per Einladung erlaubt ist)

## Registrierungsprozess

### 1. Registrierungsseite aufrufen

**Option A: Direkte Registrierung (falls aktiviert)**
- Navigieren Sie zu `/register`
- Beispiel: `https://dansal.example.com/register`

**Option B: Über Einladungslink**
- Klicken Sie auf den Einladungslink, den Sie per E-Mail erhalten haben
- Der Link führt Sie direkt zur Registrierungsseite mit vorgefülltem Einladungscode

### 2. Registrierungsformular ausfüllen

Füllen Sie das Formular mit folgenden Informationen aus:

**Pflichtfelder:**
- **Benutzername**: Ihr gewünschter Anmeldename (mindestens 3 Zeichen)
- **E-Mail-Adresse**: Ihre gültige E-Mail-Adresse
- **Passwort**: Ein sicheres Passwort (mindestens 8 Zeichen)
- **Passwort bestätigen**: Wiederholung des Passworts

**Optionale Felder:**
- **Einladungscode**: Falls Sie über einen Einladungslink gekommen sind, ist dieses Feld bereits ausgefüllt
- **Telegram-Benutzername**: Falls Sie Telegram-Benachrichtigungen wünschen

### 3. Registrierung abschließen

- Klicken Sie auf "Registrieren" oder "Konto erstellen"
- Das System prüft:
  - Gültigkeit der E-Mail-Adresse
  - Passwortstärke
  - Verfügbarkeit des Benutzernamens
  - Gültigkeit des Einladungscodes (falls erforderlich)

### 4. E-Mail-Bestätigung

- Sie erhalten eine Bestätigungs-E-Mail an die angegebene Adresse
- Klicken Sie auf den Bestätigungslink in der E-Mail
- Ihr Konto wird aktiviert

### 5. Erste Anmeldung

- Navigieren Sie zur Anmeldeseite (`/login`)
- Geben Sie Ihren Benutzernamen und Ihr Passwort ein
- Klicken Sie auf "Anmelden"
- Sie werden zu Ihrem Benutzerprofil weitergeleitet

## Fehlerbehebung

### Häufige Probleme und Lösungen

**Problem: "Benutzername bereits vergeben"**
- Wählen Sie einen anderen Benutzernamen
- Versuchen Sie eine Kombination aus Vorname und Nachname

**Problem: "E-Mail-Adresse bereits registriert"**
- Verwenden Sie eine andere E-Mail-Adresse
- Falls Sie Ihr Passwort vergessen haben, nutzen Sie die "Passwort vergessen"-Funktion

**Problem: "Ungültiger Einladungscode"**
- Überprüfen Sie, ob Sie den Code korrekt eingegeben haben
- Falls der Code abgelaufen ist, bitten Sie den Administrator um einen neuen

**Problem: Keine Bestätigungs-E-Mail erhalten**
- Überprüfen Sie Ihren Spam-Ordner
- Warten Sie 5-10 Minuten und prüfen Sie erneut
- Falls das Problem besteht, kontaktieren Sie den Administrator

## Administratorkonto

Falls Sie der erste Benutzer sind oder Administratorrechte benötigen:

1. Der erste registrierte Benutzer erhält automatisch Administratorrechte
2. Administratoren können weitere Benutzer über die Admin-Oberfläche einladen
3. Administratoren können Benutzerrollen verwalten unter `/admin/users`

## Datenschutzhinweise

- Ihre E-Mail-Adresse wird nur für Systembenachrichtigungen verwendet
- Ihr Passwort wird verschlüsselt gespeichert
- Persönliche Daten werden gemäß unserer Datenschutzrichtlinie behandelt

## Support

Bei weiteren Fragen oder Problemen:
- Konsultieren Sie die ausführliche Dokumentation
- Wenden Sie sich an den Systemadministrator
- Nutzen Sie das Kontaktformular auf der Plattform