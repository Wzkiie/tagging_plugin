# Album Fix – Navidrome-Plugin (Übergabe)

Ziel: Navidrome-Plugin, das in Explo-Downloads (YouTube, viele ohne Album/Cover) fehlende Tags
(Album, Jahr, Cover) per Deezer-Public-API ergänzt – direkt in den Audiodateien, täglich automatisch.
Nutzer: deutschsprachig. Navidrome 0.64.2, Docker, Musik unter `/music`.

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

## Stand v0.2 (umgesetzt und im echten Navidrome 0.64.2 getestet)
- Deezer-Lookup (`deezer.go`, `internal/match`): zwei Suchen, Score = min(Titel, Artist) gegen den VOLLEN Deezer-Titel (nicht `title_short`:
  der verschluckt "(Remix)"), feat.-Zusätze werden ignoriert, Laufzeit-Toleranz, Albumwahl: Künstleralbum vor Sampler, album > ep > single > compilation, früheste Veröffentlichung, kürzester Titel.
- MP3 (`internal/id3`: ID3v2.3/2.4, TALB/TYER|TDRC/APIC). Nicht unterstützt: ID3v2.2, Unsynchronisation, Footer.
- Config per `config.schema` im Manifest (ohne Schema: "plugin has no configurable options"), Cron, Pfadfilter, Dry-Run (Standard AN), KVStore-Zustand + Cursor.
- Test-Setup: `nd-test/` (Navidrome-Binary, `nd.sh`, `reload.sh`, Testmusik aus ffmpeg mit echten Tags/Laufzeiten). `go test ./...` inkl. gemocktem Host-Integrationstest.
- Gefundene und behobene Plattform-Fallen: (1) `time.Since` nutzt die Monotonic-Uhr, die im WASM nicht mit echter Zeit läuft -> `time.Now().Round(0)` + `Sub`;
  (2) WASI hat kein chmod: neue Dateien bekämen 0600 + neuen Inode -> `internal/fsutil` kopiert in die vorhandene Datei; (3) neu eingespieltes .ndp deaktiviert das Plugin.
- Nachweis (Testinstanz, 16 Dateien): 12 ergänzt, 4 korrekt abgelehnt (nicht gefunden, falsche Laufzeit, kein Titel-Tag, Remix statt Original), Audio-MD5 identisch,
  Rechte/Inode erhalten, Navidrome-Watcher liest Album/Jahr/Cover automatisch ein, 2. Lauf = 0 Lookups, Fortsetzung in Häppchen (Budget 2,5 s) und Cron funktionieren.

## Härtung nach Subagenten-Review (Dateien + Erkennung)
- Ersetzen unter WASI (`internal/fsutil`): Write-Ahead. Neue Datei -> `.albumfix-tmp` (unvollständig) -> Sync -> atomar `.albumfix-ready` -> in die Originaldatei kopieren (kein O_TRUNC, danach Truncate+Sync). Bleibt `.ready` liegen (Abbruch), vollendet `fsutil.Recover` es beim nächsten Lauf; unvollständige `.tmp` werden verworfen.
- "Unknown Album"/Whitespace/leeres Album wird ersetzt (`ReplaceAlbum`), keine doppelten ALBUM/TALB. `changed=false` wird nicht mehr als "done" gespeichert.
- ID3: Datei mit unklarer Frame-Struktur wird NICHT angefasst (statt Frames abzuschneiden); Back Cover zählt nicht als Cover; erweiterter Header/Padding werden entfernt; MP3-Dauer bei VBR ohne Xing = unbekannt (0).
- Opus: alte CRC wird vor dem Neuberechnen geprüft (beschädigte Datei bleibt unverändert), verkettete Ogg-Ströme werden nicht umnummeriert, Kommentar-Paket auf 32 MB begrenzt.
- Erkennung: tote Query `artist:"X" track:"Y"` entfernt; Kette: `Artist Titel`, `Hauptartist Titel ohne Klammern`, `track:"Titel"` (limit 25). Versionswächter (live/remix/acoustic/karaoke/demo/part): Merkmale müssen in Tag und Deezer-Titel gleich sein. Titel-Vergleich auch gegen `title_short` und ohne Klammerzusatz. Albumwahl nur unter Treffern mit exakt passendem Artist; Sampler ("Verschiedene Interpreten") werden abgelehnt; Best-of/Live/Soundtrack und Deluxe/Remaster rangieren hinten; Jahr = frühestes Datum der Künstleralben. Tag-Bereinigung: "(Official Video)", "[Lyrics]", "Artist - Titel", " - Topic". Norm entfernt Apostroph/!/* und kombinierende Zeichen (P!nk, NFD-Umlaute).
- Live-Nachweis (Dry-Run in Navidrome, echte Deezer-API): Johnny Cash "Hurt" -> American IV (nicht das Tribut-Album), Metallica -> Metallica (Remastered 2021) statt Live, Pink "So What" -> Funhouse, Dua Lipa "Don't Start Now" gefunden, Sunflower (nur Soundtrack-Sampler) abgelehnt.

## Ablauf-Härtung (3. Review)
- Zustand: Nur abgelehnte Dateien stehen im KVStore (mit Ablaufzeit = `retry_days`, `maxSize` 4MB im Manifest); fertige Dateien erkennt man an den Tags (Opus: Laufzeit wird nur bei Bedarf gelesen). Set-Fehler werden geloggt.
- Fortschritt: Sperre und Cursor werden sofort/alle 5 s gesichert, Wächter-Aufruf nach 45 s, Fortsetzung nach Neustart (OnInit), Datei, bei der ein Aufruf abstirbt (`Current`), wird für retry_days ausgelassen. Cursor-Vergleich in Walk-Ordnung (`ord`).
- Schreibtest je Bibliothek vor der Arbeit (klare Meldung statt "Not implemented on wasip1"), Abbruch nach 3 Schreibfehlern, nach 5 Netzwerkfehlern (300 s Pause), Rate-Limit-Pause; HTTP-Timeout ans Restbudget gekoppelt; ungültiger Cron fällt auf Standard zurück; Konfig-Werte werden begrenzt.
- Cover nur, wenn das Album im Tag zum Deezer-Album passt (sonst "Album weicht ab").
- Nachweis in Navidrome: Fortsetzung in Häppchen, `kill -9` mitten im Durchgang -> Neustart setzt am Cursor fort, Lauf ohne Schreibzugriff -> 0 Lookups + Hinweis.

## Test mit 1.948 echten Explo-Dateien (nd-real)
- Testumgebung: `../nd-real/` (eigene Navidrome-Instanz, Port 4535). Master = lokale Musikordner (Kopie der echten Dateien) (wird NIE verändert), `./reset.sh '<plugin-config-json>'` baut Kopie (APFS-Klon) + Plugin + KV neu auf; Standard Dry-Run.
- Befund: Explo = YouTube-Rips, Laufzeit weicht oft 10-40 s von Deezer ab (Intro/Outro), einige Dateien sind 30-s-Clips. Feste Toleranz 8 s lehnte 330 richtige Songs ab -> zweistufig: `dur_tol` 8 s immer, `dur_tol_max` 45 s nur bei Score >= 0.95. Log zeigt jetzt `Laufzeit-Δ`.
- Ergebnis Dry-Run: 1738 von 1947 (89 %) ergänzt, 209 abgelehnt (116 Laufzeit, 25 Sampler, 25 Titel/Artist, 12 kein Treffer), 1 beschädigte Datei (`17_für_immer-Provinz.opus`, schon im Original kaputt) unangefasst.
- Schreiblauf auf der Kopie: Audio bei allen 1948 Dateien byte-identisch (MD5), alle Original-Tags erhalten, Rechte gleich, 1738 Cover + 1576 Alben ergänzt, keine Reste, Navidrome liest alles ein (1528 Alben mit Cover).

## Offen
1. Lauf auf dem NAS mit echten Explo-Dateien (andere Tags/Dateistruktur als die ffmpeg-Testdateien), erst `dry_run=true`.
2. Undo: nur Logzeilen ("ergänzt: ..."); Backup von DB und Musik vorher ist Pflicht. Ideen aus dem Review: Journal `.albumfix/journal.jsonl`, `max_writes_per_day`, Plausibilitätsregeln (Jahr), Abbruch bei auffälliger Trefferquote.
2b. Playlist-Filter: Abgleich ist linear (ca. 5 ms/Datei bei 1000 Titeln) - bei großen Listen vorab normalisieren/indexieren. Ablehnungsgrund-Statistik im Log fehlt noch. Mindestabstand zwischen Deezer-Anfragen (Limit ca. 50/5 s) nicht umgesetzt.
3. Nicht getestet: sehr große Bibliotheken (>10k Dateien) im Plugin; m4a/flac nicht umgesetzt.

## Nicht tun
- Kein Torrenting, keine Deezer-ARL verwenden. Nur öffentliche Deezer-API.
- Keine Schreibläufe auf die echte Bibliothek ohne Backup von `navidrome.db` und Musik.
