---
nav_order: 9
---

# Administration – Systemverwaltung

Diese Seite richtet sich an Systemadministratoren, die eine dansal-Instanz installieren, konfigurieren und betreiben.

## 💻 Systemanforderungen

- **Betriebssystem**: Linux mit systemd (Debian/Ubuntu empfohlen)
- **Go**: 1.26+ (für den Build aus dem Quellcode)
- **nginx**: Reverse-Proxy für TLS-Terminierung
- **certbot**: Let's-Encrypt-TLS-Zertifikate
- **openssl**: Wird vom Installer für Secret- und mTLS-Zertifikatserstellung benötigt

## 🚀 Installation

### Ersteinrichtung

Den interaktiven Installer als root aus dem Quellverzeichnis ausführen:

```bash
sudo scripts/install-instance
```

Der Installer fragt ab:
- **Instanzname** (z. B. `dev`, `prod`) – wird in allen Pfad- und Unit-Namen verwendet
- **Ports** für API (Standard 8000), Web (Standard 8080) und Webmin (Standard 8090); sucht automatisch freie Ports
- **Domains** für Web-Frontend, API und optional Webmin
- **Mail**: lokaler MTA (Postfix/Sendmail) oder externer SMTP-Server
- **Instanz-Identität**: Beschreibung, Betreibername/-E-Mail, Sicherheitskontakt für Federation-Metadaten
- **mTLS**: optional Erstellung einer CA und eines Client-Zertifikats für den Webmin-Zugriff
- **certbot**: optional Let's-Encrypt-Zertifikate beziehen und nginx konfigurieren

Nach Abschluss des Installers:

```bash
# Konfigurationsdateien vor dem Start prüfen
sudo nano /etc/dansal/<instanz>/config.yaml
sudo nano /etc/dansal/<instanz>/web.yaml
sudo nano /etc/dansal/<instanz>/webmin.yaml

# Timer starten, wenn bereit
sudo systemctl start dansal-fetch@<instanz>.timer dansal-backup@<instanz>.timer
```

### Manuelle Einrichtung (ohne Skript)

```bash
# 1. Verzeichnisse anlegen, Vorlagenkonfigurationen installieren, systemd-Units aktivieren
sudo make setup-instance INSTANCE=prod

# 2. Die drei Konfigurationsdateien bearbeiten
sudo nano /etc/dansal/prod/config.yaml
sudo nano /etc/dansal/prod/web.yaml
sudo nano /etc/dansal/prod/webmin.yaml

# 3. Binaries bauen und installieren
make build
sudo make deploy INSTANCE=prod

# 4. nginx und TLS einrichten
certbot certonly --nginx -d events.example.com
certbot certonly --nginx -d api.example.com
sudo make deploy-nginx INSTANCE=prod

# 5. Timer starten
sudo systemctl start dansal-fetch@prod.timer dansal-backup@prod.timer
```

### Ersten Administrator anlegen

Es gibt kein automatisch erzeugtes Admin-Konto. Den ersten Administrator über `dansal_admin` anlegen:

```bash
/usr/lib/dansal/<instanz>/dansal_admin \
  --config /etc/dansal/<instanz>/config.yaml \
  create-user --email admin@example.com --role admin
```

Ein Passwort mit `set-password` vergeben, oder der Benutzer kann sich per Magic-Link anmelden und selbst eines setzen.

### Branding

Vor dem Live-Gang eigene `logo`-, `banner`- und `favicon`-Dateien (`.svg`, `.avif`, `.jpg` oder `.gif`) in das Bilderverzeichnis der Instanz (`/var/lib/dansal-web/<instanz>/`) legen. Diese werden sofort ohne Neustart ausgeliefert.

## ⚙️ Konfiguration

Jede Instanz hat drei Konfigurationsdateien unter `/etc/dansal/<instanz>/`:

| Datei | Zweck |
|---|---|
| `config.yaml` | API-Server: Port, Datenbankpfad, Bilder, Backups, Rate-Limits, SMTP usw. |
| `web.yaml` | Web-Frontend: Domain, ActivityPub, Übersetzungen, Branding, Telegram/Captcha |
| `webmin.yaml` | Webmin: Bind-Adresse, Verbindung zu API und web.db |

Die wichtigsten Einstellungen in `config.yaml`:

| Schlüssel | Beschreibung |
|---|---|
| `server.port` | TCP-Port der API (Standard 8000) |
| `server.listen` | Bind-Adresse (Standard `127.0.0.1:<port>`) |
| `server.db_path` | Pfad zur SQLite-Datenbank |
| `server.images_dir` | Verzeichnis für hochgeladene Bilder |
| `server.admin_socket` | Unix-Socket für `dansal_admin` und `dansal-webmin` |
| `server.backup_dir` | Verzeichnis für Datenbank-Backups |
| `server.base_url` | Öffentliche API-URL, für E-Mails und iCal-Feeds (erforderlich) |
| `server.token_expiration_hours` | Sitzungsdauer in Stunden (Standard 24) |
| `server.invite_expiry_hours` | Gültigkeitsdauer von Einladungslinks (Standard 48) |
| `server.rate_limit` | Anfragen pro Minute pro IP (Standard 100) |
| `server.login_rate_limit` | Login-Versuche pro Minute pro IP (Standard 5) |
| `server.admin_allowed_ips` | IPs mit Zugriff auf `/api/v1/admin/*` (Standard: nur localhost) |
| `server.allowed_origins` | Für die API erlaubte CORS-Origins (Standard: alle) |

Web-Frontend (`web.yaml`) und Webmin (`webmin.yaml`) haben jeweils eigene Schlüssel für Domain, ActivityPub, Branding und Verbindungseinstellungen – siehe die Beispiel-Konfigurationsdateien im Quellverzeichnis für die vollständige Liste.

## 🔄 Bestehende Instanz aktualisieren

Immer alle vier Binaries gemeinsam bauen und als Einheit deployen:

```bash
# Bauen (als normaler Benutzer – sudo hat kein go im PATH)
make build

# Auf eine bestimmte Instanz deployen (installiert Binaries, startet Dienste neu)
sudo make deploy INSTANCE=prod
```

## 👥 Benutzerverwaltung

### Benutzer anlegen

```bash
# Benutzer ohne Passwort anlegen (Anmeldung per Magic-Link)
dansal_admin --config /etc/dansal/prod/config.yaml \
  create-user --email user@example.com --role publisher

# Mit Passwort anlegen
dansal_admin --config /etc/dansal/prod/config.yaml \
  create-user --email user@example.com --role admin --password <passwort>
```

### Rollen

| Rolle | Beschreibung |
|---|---|
| `admin` | Voller Systemzugriff, kann alles verwalten |
| `publisher` | Kann Veranstaltungen erstellen/bearbeiten, Orte und Musiker verwalten |
| `user` | Kann Veranstaltungen nur für die eigene Organisation erstellen |

### Weitere Benutzerbefehle

```bash
dansal_admin list-users
dansal_admin set-role     --email E --role R
dansal_admin set-password --email E --password P
dansal_admin set-email    --email E --new-email NEU
dansal_admin disable-user --email E
dansal_admin enable-user  --email E
dansal_admin delete-user  --email E
```

### Einladungslinks

Über die Webmin-Oberfläche: **Benutzer → Einladung erstellen**. Rolle und optionale Organisation festlegen; der Link läuft nach 48 Stunden ab (konfigurierbar über `server.invite_expiry_hours`).

Per CLI:
```bash
dansal_admin list-invites
dansal_admin revoke-invite --token TOKEN
```

### Sitzungsverwaltung

```bash
dansal_admin list-sessions  --email E
dansal_admin revoke-session --id SESSION_ID
```

### mTLS-Zertifikate für Webmin

```bash
dansal_admin --config /etc/dansal/prod/config.yaml \
  mtls-issue --email admin@example.com --days 1095
dansal_admin mtls-list
dansal_admin mtls-revoke --email user@example.com
```

### SMTP-Konfiguration

```bash
dansal_admin smtp-show
dansal_admin smtp-set --host smtp.example.com --port 587 --username u@example.com
dansal_admin smtp-set-password
dansal_admin smtp-test --to test@example.com
```

## 💾 Backup & Wiederherstellung

### Automatische Backups

Der systemd-Timer `dansal-backup@<instanz>.timer` führt `dansal_admin backup` nach Zeitplan aus:

```bash
sudo systemctl enable --now dansal-backup@prod.timer
sudo systemctl status dansal-backup@prod.timer
```

Backups werden in `server.backup_dir` abgelegt (Standard `/var/lib/dansal/<instanz>/backups/`).

### Manuelles Backup

```bash
# Vollständiges Backup: Konfiguration + Datenbank + Bilder (tar.gz)
dansal_admin --config /etc/dansal/prod/config.yaml backup

# Verschlüsseltes Backup (AES-256-GCM)
dansal_admin --config /etc/dansal/prod/config.yaml password-backup

# Inkrementelles Backup seit einem bestimmten Zeitpunkt
dansal_admin --config /etc/dansal/prod/config.yaml \
  incremental-backup --since 2025-01-01T00:00:00Z
```

### Wiederherstellung

```bash
# Aus Backup-Archiv wiederherstellen (Datenbank live, kein Neustart nötig)
dansal_admin --config /etc/dansal/prod/config.yaml restore --input /pfad/zum/backup.tar.gz

# Verschlüsseltes Backup wiederherstellen
dansal_admin --config /etc/dansal/prod/config.yaml password-restore --input /pfad/zum/backup.enc
```

### Datenbankintegrität prüfen

```bash
sqlite3 /var/lib/dansal/<instanz>/calendar.db "PRAGMA integrity_check;"
```

## 🛠️ Systemwartung

```bash
# Datenbank komprimieren
dansal_admin --config /etc/dansal/prod/config.yaml vacuum

# Feed-Import manuell anstoßen
dansal_admin --config /etc/dansal/prod/config.yaml fetch-all

# Verwaiste Bilder bereinigen
dansal_admin --config /etc/dansal/prod/config.yaml prune-images
```

### Logs

```bash
journalctl -u dansal@prod -f
journalctl -u dansal-web@prod -f
journalctl -u dansal-webmin@prod -f
```

## 🩺 Fehlerbehebung

### Dienst startet nicht

```bash
sudo systemctl status dansal@prod
journalctl -u dansal@prod --since "5 min ago"
```

Häufige Ursachen:
- **Port bereits belegt**: Ein anderer Prozess nutzt den konfigurierten Port (prüfen mit `ss -tlnp`)
- **Konfigurationsdatei fehlt oder ist fehlerhaft**: `/etc/dansal/prod/config.yaml` muss existieren und gültiges YAML enthalten; `server.base_url` ist erforderlich
- **Binary nicht installiert**: `make build && sudo make deploy INSTANCE=prod` ausführen

### Datenbankprobleme

```bash
df -h /var/lib/dansal/prod/
sqlite3 /var/lib/dansal/prod/calendar.db "PRAGMA integrity_check;"
```

### Anmeldeprobleme

- **„Konto deaktiviert"**: Mit `dansal_admin enable-user --email E` wieder aktivieren
- **Konto nach Fehlversuchen gesperrt**: Auf Ablauf des `login_failure_window_secs`-Fensters warten, oder Sitzungen mit `dansal_admin revoke-session` widerrufen
- **Passkey-Fehler**: Prüfen, ob `server.base_url` mit dem tatsächlichen Origin übereinstimmt (WebAuthn ist origin-gebunden)

### Probleme beim Feed-Import

```bash
dansal_admin --config /etc/dansal/prod/config.yaml fetch-all
journalctl -u dansal-fetch@prod -n 50
```

---

**Hilfe benötigt?** Ein Issue auf [GitHub](https://github.com/ademant/dansal/issues) erstellen.

**Sicherheitsprobleme?** Den in `security_contact` (`web.yaml`) hinterlegten Kontakt nutzen, oder eine private GitHub-Security-Advisory erstellen.

