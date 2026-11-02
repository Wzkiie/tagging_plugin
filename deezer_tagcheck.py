#!/usr/bin/env python3
"""
deezer_tagcheck.py (v3, Dauerbetrieb) - fehlende Alben und Cover per öffentlicher Deezer-Suche ergänzen.

- Nutzt NUR die öffentliche Deezer-API (kein Login, kein Key, keine ARL).
- Standard ist ein TROCKENLAUF: es wird nichts an den Dateien geändert.
- Nur mit --apply werden Dateien geändert (vorher Backup, außer --no-backup).
- Es werden nur LEERE Felder gefüllt (Album, Jahr, Cover). Vorhandene Tags bleiben.
- Ein Treffer gilt nur, wenn Titel UND Artist ähnlich genug sind UND die Laufzeit passt.

DAUERBETRIEB (--watch --apply):
- Prüft alle --interval Minuten ODER einmal täglich um --at HH:MM (lokale Zeit) den Ordner
  und taggt neue Dateien automatisch. Beim Start läuft sofort ein erster Durchlauf.
- Dateien jünger als --min-age Minuten werden übersprungen (damit nichts getaggt wird,
  während Explo die Datei noch schreibt).
- Eine Zustandsdatei (--state-file) merkt sich fertige und abgelehnte Dateien.
  Fertige werden nicht mehr geöffnet, Abgelehnte erst nach --retry-days Tagen oder bei
  geänderter Datei / geänderten Einstellungen erneut versucht.
- Strg+C bzw. "docker stop" beendet sauber nach der aktuellen Datei.

Installation: pip install mutagen requests
Beispiele:
  python deezer_tagcheck.py "Y:\\Musik\\explo" --limit 20
  python deezer_tagcheck.py /music/explo --watch --apply --limit 0 --dur-tol 8 --interval 15 --min-age 10
"""
import argparse
import base64
import csv
import json
import os
import re
import shutil
import signal
import sys
import threading
import time
import unicodedata
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta
from difflib import SequenceMatcher
from pathlib import Path

import mutagen
import requests
from mutagen.flac import FLAC, Picture
from mutagen.id3 import APIC, ID3, ID3NoHeaderError
from mutagen.mp4 import MP4, MP4Cover
from mutagen.oggopus import OggOpus

EXTS = {".mp3", ".opus", ".flac", ".m4a"}
API = "https://api.deezer.com"
HEADERS = {"User-Agent": "deezer-tagcheck/3.0"}
UNKNOWN_ALBUMS = {"", "unknown album", "unknown", "unbekanntes album"}
FIELDS = ["zeit", "datei", "tag_artist", "tag_titel", "tag_album", "fehlt_album", "fehlt_cover",
          "deezer_artist", "deezer_titel", "deezer_album", "score", "entscheidung", "ergebnis"]

STOP = threading.Event()
SESSION = requests.Session()  # wiederverwendete Verbindungen = viel schneller
SESSION.headers.update(HEADERS)


class RateLimiter:
    """Gemeinsames Limit für alle Threads (Anfragen pro Sekunde)."""

    def __init__(self, per_second):
        self.interval = 1.0 / per_second
        self.lock = threading.Lock()
        self.next_slot = 0.0

    def wait(self):
        with self.lock:
            now = time.monotonic()
            slot = max(now, self.next_slot)
            self.next_slot = slot + self.interval
        if slot > now:
            time.sleep(slot - now)


LIMITER = RateLimiter(8)


# ----------------------------------------------------------------- helfer
def norm(s):
    s = unicodedata.normalize("NFKD", s or "").lower()
    s = "".join(c for c in s if not unicodedata.combining(c))
    s = re.sub(r"[^\w\s]", " ", s)
    return re.sub(r"\s+", " ", s).strip()


def ratio(a, b):
    return SequenceMatcher(None, norm(a), norm(b)).ratio()


def split_artists(s):
    parts = re.split(r"[,;/&]|\sfeat\.?\s|\sft\.?\s|\sx\s", s or "", flags=re.I)
    return [p.strip() for p in parts if p.strip()]


def safe_mtime(p):
    try:
        return int(p.stat().st_mtime)
    except Exception:
        return 0


def read_info(path):
    audio = mutagen.File(path, easy=True)
    if audio is None:
        return None

    def first(key):
        v = audio.get(key)
        return str(v[0]).strip() if v else ""

    return {
        "title": first("title"),
        "artist": first("artist"),
        "album": first("album"),
        "date": first("date"),
        "length": float(getattr(audio.info, "length", 0) or 0),
    }


def has_cover(path):
    ext = path.suffix.lower()
    try:
        if ext == ".mp3":
            try:
                return bool(ID3(path).getall("APIC"))
            except ID3NoHeaderError:
                return False
        if ext == ".flac":
            return bool(FLAC(path).pictures)
        if ext == ".opus":
            tags = OggOpus(path).tags
            return bool(tags and tags.get("metadata_block_picture"))
        if ext == ".m4a":
            tags = MP4(path).tags
            return bool(tags and tags.get("covr"))
    except Exception:
        return False
    return False


def embed_cover(path, img):
    ext = path.suffix.lower()
    if ext == ".mp3":
        try:
            tags = ID3(path)
        except ID3NoHeaderError:
            tags = ID3()
        tags.add(APIC(encoding=3, mime="image/jpeg", type=3, desc="Cover", data=img))
        tags.save(path, v2_version=3)
    elif ext == ".flac":
        audio = FLAC(path)
        pic = Picture()
        pic.type, pic.mime, pic.data = 3, "image/jpeg", img
        audio.add_picture(pic)
        audio.save()
    elif ext == ".opus":
        audio = OggOpus(path)
        pic = Picture()
        pic.type, pic.mime, pic.data = 3, "image/jpeg", img
        audio["metadata_block_picture"] = [base64.b64encode(pic.write()).decode("ascii")]
        audio.save()
    elif ext == ".m4a":
        audio = MP4(path)
        audio["covr"] = [MP4Cover(img, imageformat=MP4Cover.FORMAT_JPEG)]
        audio.save()


# ----------------------------------------------------------------- zustand
def load_state(path):
    if path and Path(path).exists():
        try:
            data = json.loads(Path(path).read_text(encoding="utf-8"))
            data.setdefault("done", {})
            data.setdefault("rejected", {})
            return data
        except Exception:
            print("Warnung: Zustandsdatei unlesbar, starte mit leerem Zustand.", flush=True)
    return {"done": {}, "rejected": {}}


def save_state(path, state):
    if not path:
        return
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    tmp = p.with_suffix(p.suffix + ".tmp")
    tmp.write_text(json.dumps(state, ensure_ascii=False), encoding="utf-8")
    os.replace(tmp, p)


# ----------------------------------------------------------------- deezer
def deezer_get(path, params=None, retries=4):
    for i in range(retries):
        LIMITER.wait()
        try:
            r = SESSION.get(API + path, params=params, timeout=15)
            data = r.json()
        except Exception:
            time.sleep(1.5 * (i + 1))
            continue
        err = data.get("error") if isinstance(data, dict) else None
        if err:
            if err.get("code") == 4:  # Rate-Limit: kurz warten, dann nochmal
                time.sleep(5)
                continue
            return None
        return data
    return None


def find_match(info, min_score, dur_tol):
    """Liefert (bester Treffer oder None, akzeptiert: bool, Grund)."""
    queries = [
        f'artist:"{info["artist"]}" track:"{info["title"]}"',
        f'{info["artist"]} {info["title"]}',
    ]
    tag_artists = split_artists(info["artist"]) + [info["artist"]]
    best, best_key = None, (-1, -1.0)

    for q in queries:
        data = deezer_get("/search", {"q": q, "limit": 10})
        for t in (data or {}).get("data", []):
            t_title = max(
                ratio(info["title"], t.get("title", "")),
                ratio(info["title"], t.get("title_short", "")),
            )
            t_art = max(ratio(a, t.get("artist", {}).get("name", "")) for a in tag_artists)
            score = min(t_title, t_art)
            dur_diff = abs(t.get("duration", 0) - info["length"]) if info["length"] else 0
            dur_ok = dur_diff <= dur_tol
            ok = score >= min_score and dur_ok
            key = (1 if ok else 0, score)
            if key > best_key:
                best_key = key
                best = {"track": t, "score": score, "dur_diff": dur_diff, "ok": ok}
        if best and best["ok"]:
            break

    if not best:
        return None, False, "kein Treffer"
    if best["ok"]:
        return best, True, "ok"
    if best["score"] < min_score:
        return best, False, "Titel/Artist zu unähnlich"
    return best, False, f"Laufzeit weicht {best['dur_diff']:.0f}s ab"


# ----------------------------------------------------------------- ändern
def apply_changes(path, root, backup_dir, info, match, need_album, need_cover, cover_cache, date_cache):
    album = match["track"]["album"]

    if backup_dir is not None:
        dest = backup_dir / path.relative_to(root)
        dest.parent.mkdir(parents=True, exist_ok=True)
        if not dest.exists():
            shutil.copy2(path, dest)

    if need_album:
        audio = mutagen.File(path, easy=True)
        audio["album"] = [album["title"]]
        if not info["date"]:
            aid = album["id"]
            if aid not in date_cache:
                data = deezer_get(f"/album/{aid}")
                date_cache[aid] = (data or {}).get("release_date", "")
            rd = date_cache[aid]
            if rd[:4].isdigit():
                audio["date"] = [rd[:4]]
        audio.save()

    if need_cover:
        aid = album["id"]
        if aid not in cover_cache:
            url = album.get("cover_xl") or album.get("cover_big")
            img = None
            if url:
                try:
                    img = SESSION.get(url, timeout=20).content
                except Exception:
                    img = None
            cover_cache[aid] = img
        img = cover_cache[aid]
        if img:
            embed_cover(path, img)
        else:
            return "Album gesetzt, Cover-Download fehlgeschlagen"
    return "geändert"


# ----------------------------------------------------------------- ein Durchlauf
def run_pass(args, root, backup_dir, state):
    now = time.time()
    use_state = bool(args.apply and args.state_file)
    done, rejected = state["done"], state["rejected"]
    retry_s = args.retry_days * 86400
    stamp = lambda: datetime.now().strftime("%d.%m. %H:%M:%S")  # noqa: E731

    files = sorted(p for p in root.rglob("*") if p.suffix.lower() in EXTS and p.is_file())
    if not args.watch:
        print(f"{len(files)} Audiodateien gefunden. Modus: "
              f"{'ÄNDERN' if args.apply else 'TROCKENLAUF (nichts wird geändert)'}", flush=True)

    def check(p):
        if STOP.is_set():
            return ("skip", p, 0)
        try:
            mt = int(p.stat().st_mtime)
            rel = str(p.relative_to(root))
            if use_state:
                if done.get(rel) == mt:
                    return ("skip", p, mt)
                r = rejected.get(rel)
                if (r and r.get("mtime") == mt and now - r.get("tried", 0) < retry_s
                        and r.get("tol") == args.dur_tol and r.get("min_score") == args.min_score):
                    return ("skip", p, mt)
            if args.min_age and now - mt < args.min_age * 60:
                return ("young", p, mt)
            info = read_info(p)
            if info is None:
                return ("skip", p, mt)
            need_album = norm(info["album"]) in UNKNOWN_ALBUMS
            need_cover = not has_cover(p)
            if need_album or need_cover:
                return ("cand", (p, info, need_album, need_cover), mt)
            return ("complete", p, mt)
        except Exception:
            return ("skip", p, 0)

    candidates, young, complete_n = [], 0, 0
    scan_start = last_print = time.monotonic()
    with ThreadPoolExecutor(max_workers=args.scan_workers) as ex:
        for n, (kind, payload, mt) in enumerate(ex.map(check, files), 1):
            if kind == "cand":
                candidates.append(payload)
            elif kind == "young":
                young += 1
            elif kind == "complete":
                complete_n += 1
                if use_state:
                    done[str(payload.relative_to(root))] = mt
            if time.monotonic() - last_print >= 15 and n < len(files):
                last_print = time.monotonic()
                el = last_print - scan_start
                print(f"  geprüft: {n}/{len(files)}  (noch ca. {el / n * (len(files) - n) / 60:.1f} min)", flush=True)

    if args.limit:
        candidates = candidates[: args.limit]
    if args.watch and not candidates:
        print(f"[{stamp()}] nichts zu tun ({len(files)} Dateien, {young} noch zu frisch).", flush=True)
        if use_state:
            save_state(args.state_file, state)
        return
    print(f"[{stamp()}] {len(candidates)} Dateien zu bearbeiten"
          f"{f', {young} noch zu frisch' if young else ''}.", flush=True)

    def lookup(item):
        if STOP.is_set():
            return None, False, "abgebrochen"
        _p, info, _na, _nc = item
        if not info["title"] or not info["artist"]:
            return None, False, "übersprungen: Titel/Artist-Tag fehlt"
        try:
            return find_match(info, args.min_score, args.dur_tol)
        except Exception as e:
            return None, False, f"Fehler: {e}"

    rows, cover_cache, date_cache = [], {}, {}
    stats = {"ok": 0, "unsicher": 0, "keiner": 0, "geändert": 0}
    start = time.monotonic()
    total = len(candidates)

    ex = ThreadPoolExecutor(max_workers=args.workers)
    futures = [ex.submit(lookup, c) for c in candidates]
    try:
        for i, (item, fut) in enumerate(zip(candidates, futures), 1):
            if STOP.is_set():
                break
            p, info, need_album, need_cover = item
            match, ok, reason = fut.result()
            rel = str(p.relative_to(root))
            row = {
                "zeit": datetime.now().strftime("%Y-%m-%d %H:%M:%S"), "datei": rel,
                "tag_artist": info["artist"], "tag_titel": info["title"], "tag_album": info["album"],
                "fehlt_album": need_album, "fehlt_cover": need_cover,
                "deezer_artist": "", "deezer_titel": "", "deezer_album": "",
                "score": "", "entscheidung": reason, "ergebnis": "",
            }
            if match:
                t = match["track"]
                row.update(
                    deezer_artist=t["artist"]["name"], deezer_titel=t["title"],
                    deezer_album=t["album"]["title"], score=f"{match['score']:.2f}",
                )

            reject_entry = {"tried": time.time(), "reason": reason,
                            "tol": args.dur_tol, "min_score": args.min_score}
            if ok:
                stats["ok"] += 1
                if args.apply:
                    try:
                        res = apply_changes(p, root, backup_dir, info, match, need_album,
                                            need_cover, cover_cache, date_cache)
                        row["ergebnis"] = res
                        if res == "geändert":
                            stats["geändert"] += 1
                            if use_state:
                                done[rel] = safe_mtime(p)
                                rejected.pop(rel, None)
                        elif use_state:
                            rejected[rel] = dict(reject_entry, mtime=safe_mtime(p), reason=res)
                    except Exception as e:
                        row["ergebnis"] = f"Fehler: {e}"
                        if use_state:
                            rejected[rel] = dict(reject_entry, mtime=safe_mtime(p), reason=f"Fehler: {e}")
            else:
                stats["unsicher" if match else "keiner"] += 1
                if use_state and reason != "abgebrochen":
                    rejected[rel] = dict(reject_entry, mtime=safe_mtime(p))

            if total <= 60 or i % 25 == 0 or i == total:
                rest = (time.monotonic() - start) / i * (total - i)
                print(f"[{i}/{total}] {info['artist']} - {info['title']} -> {reason}"
                      + (f"  [{row['deezer_album']}]" if row["deezer_album"] else "")
                      + (f"   (noch ca. {rest / 60:.1f} min)" if total > 60 else ""), flush=True)
            rows.append(row)
            if use_state and i % 100 == 0:
                save_state(args.state_file, state)
    finally:
        ex.shutdown(wait=False, cancel_futures=True)
        if use_state:
            save_state(args.state_file, state)

    if rows:
        append = args.watch and Path(args.report).exists()
        with open(args.report, "a" if append else "w", newline="", encoding="utf-8-sig") as f:
            w = csv.DictWriter(f, fieldnames=FIELDS, delimiter=";")
            if not append:
                w.writeheader()
            w.writerows(rows)

    print(f"[{stamp()}] fertig: sicher {stats['ok']}, unsicher/abgelehnt {stats['unsicher']}, "
          f"kein Treffer {stats['keiner']}"
          + (f", geändert {stats['geändert']}" if args.apply else " (Trockenlauf, nichts geändert)")
          + f", Dauer {(time.monotonic() - start) / 60:.1f} min", flush=True)
    if not args.watch:
        print(f"Report: {Path(args.report).resolve()}", flush=True)
        if args.apply and backup_dir is not None:
            print(f"Backup: {backup_dir.resolve()}", flush=True)


# ----------------------------------------------------------------- zeitplan
def parse_at(value):
    try:
        h, m = value.split(":")
        h, m = int(h), int(m)
        if not (0 <= h < 24 and 0 <= m < 60):
            raise ValueError
        return h, m
    except ValueError:
        raise argparse.ArgumentTypeError("--at erwartet HH:MM, z. B. 04:30")


def seconds_until(at, now=None):
    """Sekunden bis zur nächsten Uhrzeit HH:MM (lokale Zeit); genau jetzt zählt als morgen."""
    now = now or datetime.now()
    target = now.replace(hour=at[0], minute=at[1], second=0, microsecond=0)
    if target <= now:
        target += timedelta(days=1)
    return (target - now).total_seconds()


# ----------------------------------------------------------------- main
def _on_signal(signum, frame):
    if STOP.is_set():
        os._exit(130)  # zweites Strg+C: sofort beenden
    STOP.set()
    print("\nStopp angefordert: beende nach der aktuellen Datei ... (nochmal Strg+C erzwingt Abbruch)", flush=True)


def main():
    global LIMITER
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawTextHelpFormatter)
    ap.add_argument("folder", help="Musikordner")
    ap.add_argument("--limit", type=int, default=20, help="max. Anzahl Dateien pro Durchlauf (0 = alle), Standard 20")
    ap.add_argument("--min-score", type=float, default=0.85, help="Mindest-Ähnlichkeit 0-1, Standard 0.85")
    ap.add_argument("--dur-tol", type=float, default=3.0, help="erlaubte Laufzeit-Abweichung in s, Standard 3")
    ap.add_argument("--workers", type=int, default=6, help="parallele Deezer-Abfragen, Standard 6")
    ap.add_argument("--rate", type=float, default=8.0, help="max. Anfragen/Sekunde an Deezer, Standard 8")
    ap.add_argument("--scan-workers", type=int, default=16, help="parallele Dateizugriffe beim Prüfen, Standard 16")
    ap.add_argument("--report", default="deezer_report.csv", help="CSV-Report (im Dauerbetrieb wird angehängt)")
    ap.add_argument("--apply", action="store_true", help="Änderungen wirklich schreiben (sonst Trockenlauf)")
    ap.add_argument("--backup-dir", default="tag-backup", help="Backup-Ordner für --apply")
    ap.add_argument("--no-backup", action="store_true", help="kein Backup anlegen (nur wenn du dem Ergebnis traust)")
    ap.add_argument("--state-file", default="deezer_state.json", help="Zustandsdatei (nur mit --apply)")
    ap.add_argument("--retry-days", type=float, default=7.0, help="abgelehnte Dateien nach X Tagen erneut versuchen, Standard 7")
    ap.add_argument("--min-age", type=float, default=0.0, help="Dateien jünger als X Minuten überspringen, Standard 0")
    ap.add_argument("--watch", action="store_true", help="Dauerbetrieb: Ordner regelmäßig prüfen (braucht --apply)")
    ap.add_argument("--interval", type=float, default=15.0, help="Minuten zwischen den Durchläufen im Dauerbetrieb, Standard 15")
    ap.add_argument("--at", type=parse_at, default=None, metavar="HH:MM",
                    help="Dauerbetrieb: täglich zu dieser Uhrzeit laufen (lokale Zeit, z. B. 04:30) statt alle --interval Minuten")
    args = ap.parse_args()

    if args.watch and not args.apply:
        sys.exit("--watch braucht --apply (ein Dauer-Trockenlauf ergibt keinen Sinn).")

    LIMITER = RateLimiter(args.rate)
    root = Path(args.folder)
    if not root.is_dir():
        sys.exit(f"Ordner nicht gefunden: {root}")
    backup_dir = None if args.no_backup else Path(args.backup_dir)

    signal.signal(signal.SIGINT, _on_signal)
    try:
        signal.signal(signal.SIGTERM, _on_signal)
    except (ValueError, OSError):
        pass

    state = load_state(args.state_file) if (args.apply and args.state_file) else {"done": {}, "rejected": {}}

    if not args.watch:
        run_pass(args, root, backup_dir, state)
        sys.exit(130 if STOP.is_set() else 0)

    when = f"täglich um {args.at[0]:02d}:{args.at[1]:02d}" if args.at else f"alle {args.interval:g} min"
    print(f"Dauerbetrieb gestartet: {root}  ({when}, Mindestalter {args.min_age:g} min, "
          f"Toleranz {args.dur_tol:g} s, Backup {'aus' if backup_dir is None else backup_dir})", flush=True)
    while not STOP.is_set():
        try:
            if not root.is_dir():
                print(f"Ordner {root} nicht erreichbar, versuche es später erneut.", flush=True)
            else:
                run_pass(args, root, backup_dir, state)
        except Exception as e:  # ein fehlgeschlagener Durchlauf darf den Dienst nicht beenden
            print(f"Fehler im Durchlauf: {e}", flush=True)
        wait = seconds_until(args.at) if args.at else args.interval * 60
        nxt = datetime.now() + timedelta(seconds=wait)
        print(f"Nächster Durchlauf: {nxt.strftime('%d.%m. %H:%M')}", flush=True)
        STOP.wait(wait)
    print("Beendet.", flush=True)


if __name__ == "__main__":
    main()
