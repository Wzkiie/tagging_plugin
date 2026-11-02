# Album Fix – Navidrome-Plugin

Ergänzt in Explo-Downloads (und anderen Dateien) fehlende **Album, Jahr und Cover** über die öffentliche Deezer-API –
direkt in den Opus- und MP3-Dateien. Es werden nur **leere** Felder gefüllt, vorhandene Tags bleiben.

## Installieren
1. `albumfix.ndp` in den Plugin-Ordner von Navidrome legen (`<DataFolder>/plugins`), `ND_PLUGINS_ENABLED=true` setzen, Navidrome starten.
2. Plugin in der Weboberfläche (Admin, Plugins) aktivieren, Bibliothek(en) und **Schreibzugriff** freigeben und die Einstellungen setzen.
   Per CLI geht dasselbe:
   ```bash
   navidrome plugin edit albumfix --all-libraries --write-access \
     --config '{"dry_run":"true","path_filter":"explo/"}'
   navidrome plugin enable albumfix
   ```
   (Neues .ndp einspielen setzt das Plugin auf "deaktiviert": danach `navidrome plugin rescan` und erneut `enable`.)
3. **Standard ist Probelauf** (`dry_run=true`): im Log steht `albumfix: PROBELAUF, würde ergänzen: ...`. Erst wenn das passt,
   `dry_run` auf `false` setzen. Vorher Backup von `navidrome.db` und Musik.
4. Navidrome scannt geänderte Dateien über seinen Watcher selbst neu (im Test automatisch).

## Einstellungen (Plugin-Config, alle Werte als Strings)
| Schlüssel | Standard | Bedeutung |
|---|---|---|
| `dry_run` | `true` | nur protokollieren, nichts schreiben |
| `min_score` | `0.85` | Mindest-Ähnlichkeit von Titel **und** Artist |
| `dur_tol` | `8` | erlaubte Laufzeit-Abweichung (s) |
| `path_filter` | leer | nur Pfade mit diesem Teilstring, kommagetrennt (z. B. `explo/`) |
| `limit_per_run` | `0` | max. Lookups pro Durchgang, über alle Häppchen gezählt (0 = alle), z. B. `20` für "erstmal nur 20 Dateien"; der nächste Lauf macht mit den übrigen weiter |
| `playlists` | leer | nur Titel aus diesen Wiedergabelisten (Namen, kommagetrennt) |
| `playlist_user` | leer | Navidrome-Benutzer, dem die Wiedergabelisten gehören (Pflicht bei `playlists`; Plugin braucht Zugriff auf diesen Benutzer, `--users` bzw. `--all-users`) |
| `retry_days` | `7` | abgelehnte Dateien erst nach X Tagen erneut prüfen |
| `min_age_min` | `10` | zu frische Dateien überspringen (Explo schreibt evtl. noch) |
| `cron` | `30 4 * * *` | Zeitplan |
| `run_on_start` | `true` | kurz nach dem Laden (und nach jeder Konfig-Änderung) einen Durchgang starten. **Achtung:** mit `dry_run=false` schreibt das sofort. Für den ersten Schreiblauf zusammen mit `limit_per_run` (z. B. `20`) verwenden |
| `budget_ms` | `18000` | Zeitbudget pro Aufruf (Navidrome-Limit: 30 s) |

## Ablauf
Pro Datei: Tags lesen → fehlt Album/Cover? → zwei Deezer-Suchen → Score = min(Titel-, Artist-Ähnlichkeit),
Laufzeit-Toleranz → bei mehreren Alben `album > ep > single > compilation` → Album, Jahr, Cover (XL) schreiben
(Audio byte-identisch; Dateirechte und Inode bleiben erhalten, weil WASI kein chmod kennt - der Inhalt wird in die vorhandene Datei kopiert, die vollständige Kopie `<datei>.albumfix-tmp` bleibt nur bei einem Fehler liegen). Zustand (fertig/abgelehnt, Fortschritt) liegt im KVStore.

## Bauen & Testen
```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
zip albumfix.ndp manifest.json plugin.wasm
go test ./...        # braucht ffmpeg + ffprobe (mit libopus, libmp3lame)
```
