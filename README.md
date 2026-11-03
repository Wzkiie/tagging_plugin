# Album Fix – a Navidrome plugin

Fills in missing **album, year and cover art** for music files whose tags are incomplete (typical for YouTube rips and
downloads from tools such as [Explo](https://github.com/LumePart/Explo)). It looks the track up through the public
**Deezer API** and writes the result directly into the **Opus and MP3** files. Only **empty** fields are filled;
existing tags are never overwritten. The audio data stays byte-identical.

- Runs inside Navidrome (WASM plugin, Navidrome ≥ 0.64), no extra container or script needed
- Daily schedule (cron) plus an optional run right after loading
- **Dry run by default**: it only logs what it would change
- Careful matching: title and artist similarity, duration check, version guard (live / remix / acoustic / instrumental
  are not mixed up), prefers real albums over compilations and samplers
- Selectable scope: path filter, playlists, per-run limit
- Safe writes: write test per library, write-ahead copy with crash recovery, automatic abort on repeated errors

## Install
1. Download `albumfix.ndp` (or build it, see below) and put it into Navidrome's plugin folder (`<DataFolder>/plugins`).
   Set `ND_PLUGINS_ENABLED=true` and start Navidrome.
2. In the web UI (Admin → Plugins) enable the plugin, grant access to your library/libraries **with write access**,
   and set the options. The same via CLI:
   ```bash
   navidrome plugin edit albumfix --all-libraries --write-access \
     --config '{"dry_run":"true","path_filter":"downloads/"}'
   navidrome plugin enable albumfix
   ```
   Installing a new `.ndp` disables the plugin (its hash changed): run `navidrome plugin rescan` and `enable` again.
3. **The default is a dry run** (`dry_run=true`). The log shows lines like
   `albumfix: PROBELAUF, würde ergänzen: ...` ("dry run, would add"). When the results look right, set `dry_run` to
   `false`. **Back up your music and `navidrome.db` first** – there is no undo.
4. Navidrome picks up the changed files by itself through its file watcher.

> Log messages and option descriptions in the UI are currently in German.

## Recommended first run
1. Keep `dry_run=true`, set `path_filter` to one folder, read the log.
2. Set `dry_run=false` with `limit_per_run=20` and check the 20 files in your player.
3. Remove the limit and widen `path_filter` folder by folder (or clear it for the whole library).

## Options (plugin config, all values are strings)
| Key | Default | Meaning |
|---|---|---|
| `dry_run` | `true` | only log, do not write |
| `min_score` | `0.85` | minimum similarity of title **and** artist |
| `dur_tol` | `8` | allowed duration difference in seconds |
| `dur_tol_max` | `45` | larger difference, accepted only for an almost exact title/artist match (YouTube files often have intro/outro); `0` = off |
| `path_filter` | empty | only paths containing one of these substrings, comma separated, relative to the library root, case-insensitive (e.g. `downloads/`) |
| `limit_per_run` | `0` | max. lookups per run, counted across all chunks (`0` = unlimited); the next run continues with the rest |
| `playlists` | empty | only tracks from these playlists (names, comma separated) |
| `playlist_user` | empty | Navidrome user who owns the playlists (required with `playlists`; the plugin needs access to that user, `--users` / `--all-users`) |
| `retry_days` | `7` | re-check rejected files only after this many days |
| `min_age_min` | `10` | skip files younger than this (a downloader may still be writing them) |
| `cron` | `30 4 * * *` | schedule |
| `run_on_start` | `true` | start a run shortly after loading and after every config change. **Note:** with `dry_run=false` this writes immediately; combine with `limit_per_run` for the first real run |
| `budget_ms` | `18000` | time budget per call (Navidrome's hard limit is 30 s; work is split into chunks) |

## How it works
Per file: read tags → album or cover missing? → Deezer search (several query variants) → score = min(title, artist
similarity), duration check → if the recording is on several albums, prefer `album > ep > single > compilation`
→ write album, year (earliest release among the artist's albums) and cover (XL). The cover is only embedded when the
existing album tag matches the Deezer album. Because WASI has no `chmod`, content is copied into the existing file
(permissions and inode stay the same); the full copy `<file>.albumfix-tmp` is kept only if something fails.
State (rejected files, progress) lives in Navidrome's KV store.

Supported formats: Opus, MP3 (ID3v2.3/2.4). Not supported: FLAC, M4A.

## Build & test
Requires Go ≥ 1.24.
```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
zip albumfix.ndp manifest.json plugin.wasm
go test ./...        # needs ffmpeg + ffprobe (with libopus, libmp3lame)
```

## Notes
- Uses the public Deezer API without a key; it is rate limited and may change. The plugin pauses on quota errors.
- Metadata comes from Deezer; quality depends on the match. Always start with a dry run and a small scope.
- `deezer_tagcheck.py` and `docker-compose.yml` are the older standalone variant (a separate container) and are not needed for the plugin.
