---
nav_order: 3
---

# Konto, Anmeldung & Rollen

## 🔑 Konto erstellen
Ein normales Konto zum Verwalten von Veranstaltungen, muss mindestens einer Organisation zugeordnet sein. Es lassen sich dann nur Veranstaltungen für diese Organisation erstellen oder verändern.

Es gibt zwei Wege, ein Konto zu erhalten:
1. **Selbstregistrierung**: Eine Anfrage stellen, die von einem Administrator bestätigt wird. Nach Bestätigung durch einen Administrator, erhältst Du einen Einladungslink mit dem Du Dein Konto fertigstellen kannst.
2. **Einladungslink**: Ein bereits bestehender Benutzer kann dir einen Einladungslink oder QR-Code zur Registrierung geben. Damit kannst Du direkt ein Konto anlegen.

Bei der Selbstregistrierung kann entweder eine existierende Organisation beigetreten werden oder eine neue eingetragen werden. Diese neue Organisation wird erst angelegt, wenn die Anfrage bewilligt wurde.

### Selbst-Onboarding über den Einladungslink

Öffnest du einen Einladungslink, richtest du dein Konto in einem Schritt selbst ein:
1. **Anzeigename** eingeben (frei wählbar, kein Klarname nötig)
2. **E-Mail-Adresse** eingeben – ist sie bereits vom Einladenden vorgegeben, ist das Feld nur lesbar
3. **Bevorzugte Anmeldemethode** wählen:
   - **🔑 Passkey** (voreingestellt, falls dein Gerät/Browser das unterstützt): Erstellung direkt im Browser, kein Passwort nötig
   - **🔒 Passwort**: klassisches Passwort mit Bestätigung
4. Absenden – du bist danach sofort eingeloggt und landest auf der Startseite

Wurde die E-Mail-Adresse bestätigt, ist dies automatisch ein weiteres Anmeldeweg: Ein Magic Link wird Dir zugesandt, mit dem Du angemeldet wirst.

Ein **zweiter oder dritter Anmeldeweg** (z. B. zusätzlicher Passkey, TOTP-App, oder das Verknüpfen von Telegram/Matrix für Magic Links) wird nicht beim Onboarding, sondern **danach in den Profileinstellungen** unter „Konto/Einstellungen“ eingerichtet. So lässt sich z. B. mit Passkey starten und später zusätzlich ein Passwort oder TOTP als Backup hinzufügen.

Jeder über einen Einladungslink erstellte Account erhält automatisch die Rolle **user** (für höhere Rollen wie publisher/admin muss ein Administrator die Rolle nachträglich anpassen).

Sollte doch einmal alle Anmeldewege abhanden gekommen sein (Laptop ist gecrashed), dann kann ein Administrator Dir einen Magic Link zur Verfügung stellen, womit Du Dich wieder anmelden kannst und neue Anmeldedaten hinterlegen kannst.

## 🔐 Anmeldemethoden

Benutzer, die Veranstaltungen verwalten möchten, haben mehrere Möglichkeiten zur Anmeldung:

- **Klassisches Passwort**: Anmeldung mit E-Mail/Anzeigename und Passwort, wie bei vielen anderen Diensten
- **TOTP**: Eine TOTP-Authenticator-App kann als zweiter Faktor verwendet werden
- **Magic Link**: Wenn E-Mail, Telegram oder Matrix hinterlegt und verifiziert ist, wird ein einmaliger Anmeldelink an diese Adresse gesendet
- **Passkey**: Ein moderner Passkey, sicher auf deinem Gerät gespeichert oder auf dem Mobilgerät mit Fingerabdruck gesichert.

Nur mit einem Passkey ist es möglich, Veranstaltungen ohne E-Mail-Adresse oder Anzeigename zu verwalten.

> 📱 **Biometrische Anmeldung (Fingerabdruck/Face ID)**: Auf dem Smartphone nutzt ein Passkey automatisch den biometrischen Sensor deines Geräts. Beim Erstellen oder Verwenden eines Passkeys fragt dein Betriebssystem nach Fingerabdruck, Gesichtserkennung oder Geräte-PIN – dansal selbst sieht oder speichert dabei niemals biometrische Daten, sondern nur den daraus erzeugten kryptografischen Schlüssel.
>
> ⚠️ **Duckduckgo auf Android** (eventuell auch andere Browser auf Android oder IPhone): Passkeys funktionieren dort aktuell nicht zuverlässig, da Duckduckgo für Android (im Gegensatz zu Chrome, Samsung Internet u. a.) die Android-Credential-Manager-Schnittstelle noch nicht unterstützt. Falls beim Anlegen oder Nutzen eines Passkeys kein Fingerabdruck-/Face-ID-Dialog erscheint, bitte stattdessen die Anmeldemethode **🔒 Passwort** wählen.

## 👤 Kontotypen & Rollen

| Rolle | Berechtigungen |
|---|---|
| **admin** | Voller Zugriff auf alle Funktionen und Einstellungen |
| **publisher** | Veranstaltungen erstellen/bearbeiten, Orte und Musiker verwalten |
| **user** | Veranstaltungen nur für die eigene Organisation erstellen |

## ⚙️ Profileinstellungen

- **Persönliche Daten**: Anzeigename, E-Mail
- **Benachrichtigungseinstellungen**: E-Mail-, Telegram-, Matrix-Benachrichtigungen
- **Spracheinstellungen**: Standardsprache festlegen
- **Kontosicherheit**: Passwort ändern, Passkeys verwalten
- **Verknüpfte Konten**: Telegram, Matrix, Mastodon usw. verbinden

## Geteiltes Konto
Es ist nicht notwendig, ein Gemeinschaftskonto für einen Verein anzulegen, über den verschiedene Personen zugreifen können. Die Sicherheit eines Konto wird erhöht, wenn jeder die höchstmögliche Sicherheit wählt und, wenn ein Passwort verwendet wird, einen Passwortmanager mit möglichst komplexem Passwort verwendet wird.

---

**Weiter zu**: [Benutzer-Veranstaltungen](Benutzer-Veranstaltungen) | [Benutzer](Benutzer)

