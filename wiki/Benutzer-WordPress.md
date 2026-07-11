---
nav_order: 9
---

# WordPress-Plugin „wp-dansal" nutzen

Wer bereits eine WordPress-Seite betreibt, kann Veranstaltungen und Veranstaltungsorte direkt aus dem gewohnten WordPress-Admin heraus pflegen, statt die dansal-Weboberfläche zu verwenden. Das Plugin **wp-dansal** speichert dabei nichts selbst – jede Änderung wird sofort mit einer dansal-Instanz synchronisiert, die als eigentlicher Datenspeicher dient.

## 🧩 Was das Plugin bietet

- Eigene Beitragstypen **Dance Locations** und **Dance Events** im WordPress-Admin, dazu optional **Dance Series** für wiederkehrende Programme
- Beim Anlegen eines Orts wird die Adresse über OpenStreetMap (Nominatim) gesucht und automatisch geprüft, ob in dansal bereits ein passender Ort existiert (über OSM-ID oder Nähe) – bevor ein Duplikat entsteht
- Jedes Speichern eines Termins/Orts synchronisiert sofort zu dansal (Neuanlage beim ersten Speichern, danach Aktualisierung), über einen Publisher-API-Schlüssel, der auf eine Organisation beschränkt ist
- Shortcode `[dansal_events]`: kommende Veranstaltungen als Liste oder Monatskalender
- Shortcode `[dansal_locations]`: Verzeichnis aller Orte mit einer selbst gehosteten Leaflet-Karte
- Einzelseiten-Vorlagen für Veranstaltungen und Orte, die im eigenen Theme überschrieben werden können

## ⚙️ Einrichtung

1. In dansal unter `/admin/users` bei der eigenen Organisation den **Verbindungslink** anklicken (oder zuerst einen Publisher-Zugang für die Organisation anlegen)
2. In WordPress unter **Einstellungen → Dansal** den Link unter „Über Link verbinden" einfügen und auf **Verbinden** klicken – Basis-URL, Organisation und API-Schlüssel werden automatisch übernommen. Mit „Verbindung testen" prüfen
   - Alternativ unter „Manuelle Verbindung (erweitert)" Basis-URL, Organisations-ID und API-Schlüssel von Hand eintragen, falls diese bereits über `dansal_admin` oder `POST /api/v1/publishers` erzeugt wurden
3. Orte unter **Dance Locations** anlegen, danach Veranstaltungen unter **Dance Events**

## 📍 Veranstaltungen und Orte anzeigen

- Automatische Archivseiten für alle Orte/Veranstaltungen entstehen durch das Plugin selbst
- Alternativ lässt sich die Orte-Karte oder der Veranstaltungskalender auf einer beliebigen WordPress-Seite platzieren: **Seiten → Erstellen → Attribute → Vorlage**, dort **„Dansal: Locations Map"** oder **„Dansal: Events Calendar"** auswählen – Titel und Inhalt der Seite bleiben erhalten, Karte/Kalender werden darunter angehängt

## 🎨 Vorlagen im eigenen Theme überschreiben

Jede der Plugin-Vorlagen lässt sich durch eine Kopie im (Child-)Theme unter einem `dansal/`-Unterverzeichnis überschreiben:

- `dansal/single-dansal_event.php`
- `dansal/single-dansal_location.php`
- `dansal/archive-dansal_event.php`
- `dansal/archive-dansal_location.php`
- `dansal/page-dansal-locations.php`
- `dansal/page-dansal-calendar.php`

## 🗺️ Kartenkacheln (Tiles)

Die Kartenkacheln werden standardmäßig live von OpenStreetMap geladen (die Leaflet-Bibliothek selbst ist im Plugin enthalten, Kartenkacheln lassen sich aber praktisch nicht selbst hosten). Jede Kachel-Anfrage sendet `Referrer-Policy: no-referrer`, damit die Seiten-URL nicht an den Kachel-Anbieter weitergegeben wird. Für einen selbst gehosteten oder bezahlten Kachel-Proxy lässt sich der Filter `wpd_tile_url_template` verwenden:

```php
add_filter( 'wpd_tile_url_template', function () {
    return 'https://tiles.example.com/{z}/{x}/{y}.png';
} );
```

## ❓ Häufige Fragen

**Brauche ich einen dansal-Server?**
Ja. Das Plugin ist eine Editier-Oberfläche für [dansal](https://github.com/ademant/dansal) und speichert selbst keine Veranstaltungsdaten.

**Löscht eine Deinstallation meine Veranstaltungen?**
Nein. Beim Deinstallieren werden nur Plugin-Einstellungen und Zwischenspeicher entfernt. Um auch Orte/Veranstaltungen/Serien zu löschen, muss vor der Deinstallation der Filter `wpd_uninstall_delete_content` aktiviert werden.

---

**Weiter zu**: [Benutzer](Benutzer) | [Benutzer-Organisationen](Benutzer-Organisationen)

**Fehler gefunden?** Melde es auf [GitHub](https://github.com/ademant/dansal/issues)
