# Album Fix – Navidrome-Plugin (Übergabe)

Ziel: Navidrome-Plugin, das in Explo-Downloads (YouTube, viele ohne Album/Cover) fehlende Tags
(Album, Jahr, Cover) per Deezer-Public-API ergänzt – direkt in den Audiodateien, täglich automatisch.
Nutzer: Jacob (Deutsch, direkt, knapp, verifiziert, produktionsreif). Navidrome 0.64.2, Docker auf UGREEN NAS, Musik `/music`.

## Stand
- `internal/opus`: Ogg-Opus-Tag-Rewriter (nur stdlib). `Apply` (in-memory, für Tests) und `Rewrite(path, Edit)`
  (Streaming, konstanter Speicher, Temp-Datei `.albumfix-tmp` + Rename). Ergänzt nur fehlende Felder, überschreibt nie.
  Audiodaten byte-identisch; Seitennummern/CRC werden nur bei Bedarf neu berechnet.
- `main.go`: Spike. OnInit plant Lauf, OnCallback läuft über alle Bibliotheken (`lib.MountPoint`), nur `.opus`,
  schreibt noch FESTE Testwerte (`testAlbum`, `testDate`, `cover.jpg`). Zeitbudget (`budgetMs`, Standard 20000) →
  danach Selbst-Neueinplanung mit `SchedulerScheduleOneTime(2, "", "")` (LEERE ID! Feste ID scheitert: "already exists").
- Messung (454 Opus, 150 à 2,5 MB): 2,7 s, Spitzen-RSS 713 MB (vorher OOM bei 6 GB), 0 Fehler, Audio-MD5 identisch,
  Idempotenz ok, Neu-Einplanen bei 0,8 s Budget: 6 Durchgänge, 134 MB.
- Tests: `go test ./internal/...` (braucht ffmpeg, libopus, libvorbis).

## Plattform-Fakten (Navidrome-Plugins)
- `.ndp` = zip aus `manifest.json` + `plugin.wasm`. Bauen mit Go ≥1.24:
  `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .` (kein TinyGo nötig). PDK liegt in `pdk/` (go 1.24).
- Library-Permission `filesystem:true` → Mount unter `/libraries/<id>`, schreibbar nur, wenn Admin `--write-access` vergibt
  (immer ganze Bibliothek). Kein Tag-Schreib-API, keine Datei-/Scan-Events, kein Scan-Trigger; Navidrome-Watcher scannt Änderungen selbst.
- Harte 30 s pro Plugin-Aufruf (nicht konfigurierbar) → immer in Häppchen arbeiten.
- Zeitplan täglich: `SchedulerScheduleRecurring` (Cron). HTTP nur für in `requiredHosts` deklarierte Hosts.

## Offen
1. Deezer-Lookup im Plugin (HTTP-Permission, Hosts `api.deezer.com` + Cover-CDN `*.dzcdn.net`/`e-cdns-images.dzcdn.net`).
   Logik aus Python-Tagger übernehmen: zwei Suchanfragen, Score = min(Titel-, Artist-Ähnlichkeit),
   Dauer-Toleranz (8 s), Album über record_type bevorzugen (album > ep > single > compilation), Rate ~50/5 s.
   Referenz: `deezer_tagcheck.py` (liegt beim Nutzer; Optionen --min-score --dur-tol).
2. MP3 (ID3v2, APIC) – bisher nur Opus. Optional m4a/flac.
3. Plugin-Config (Mindest-Score, Dauer-Toleranz, Limit pro Lauf, Dry-Run), täglicher Zeitplan, Bereich einschränken
   (z. B. nur Pfade unter `explo/`).
4. Zustand merken (KVStore/Cache): bereits erfolglos geprüfte Dateien nicht jeden Tag neu abfragen (retry-days).
5. Backup/Undo-Konzept (Original-Tags vorher sichern), Dry-Run-Report.
6. Test auf dem NAS: zweite Navidrome-Instanz mit ~20–30 kopierten Explo-Dateien (Deezer ist in der Cloud-Sandbox gesperrt).

## Nicht tun
- Kein Torrenting, keine Deezer-ARL verwenden. Nur öffentliche Deezer-API.
- Keine Schreibläufe auf die echte Bibliothek ohne Backup von `navidrome.db` und Musik.
