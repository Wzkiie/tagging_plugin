// albumfix: ergänzt in Opus-/MP3-Dateien der Navidrome-Bibliothek fehlende Album-, Jahr- und
// Cover-Tags über die öffentliche Deezer-API - direkt über den WASI-Mount der Bibliothek.
//
// Navidrome bricht jeden Plugin-Aufruf nach 30 s ab. Ein Lauf arbeitet deshalb in Häppchen
// (Zeitbudget), merkt sich den Fortschritt im KVStore und plant sich selbst neu ein.
//
// Bauen:  GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"albumfix/internal/fsutil"
	"albumfix/internal/id3"
	"albumfix/internal/match"
	"albumfix/internal/opus"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
)

const (
	dailyID     = "albumfix-daily"
	defaultCron = "30 4 * * *"
	hardBudget  = 25 * time.Second // danach keine neuen HTTP-Anfragen mehr
)

type albumfix struct{}

func init() {
	lifecycle.Register(&albumfix{})
	scheduler.Register(&albumfix{})
}

var (
	_ lifecycle.InitProvider     = (*albumfix)(nil)
	_ scheduler.CallbackProvider = (*albumfix)(nil)
)

func logf(level pdk.LogLevel, format string, a ...any) {
	pdk.Log(level, "albumfix: "+fmt.Sprintf(format, a...))
}

// OnInit richtet den täglichen Zeitplan ein und startet optional kurz nach dem Laden einen Lauf.
func (p *albumfix) OnInit() error {
	s := loadSettings()
	_ = host.SchedulerCancelSchedule(dailyID) // sonst "already exists" nach Neustart
	if _, err := host.SchedulerScheduleRecurring(s.Cron, "cron", dailyID); err != nil {
		logf(pdk.LogWarn, "Zeitplan %q ungültig (%v) - nutze Standard %q", s.Cron, err, defaultCron)
		s.Cron = defaultCron
		if _, err := host.SchedulerScheduleRecurring(s.Cron, "cron", dailyID); err != nil {
			return fmt.Errorf("Zeitplan %q: %w", s.Cron, err)
		}
	}
	logf(pdk.LogInfo, "bereit: Zeitplan %q, %s", s.Cron, modeText(s))
	if run := loadRun(); run.Active {
		// Neustart mitten in einem Durchgang: dort weitermachen, wo der Fortschritt zuletzt gesichert wurde
		logf(pdk.LogInfo, "unterbrochener Durchgang gefunden (Cursor %q) - wird fortgesetzt", run.Cursor)
		if _, err := host.SchedulerScheduleOneTime(5, "cont", ""); err != nil {
			return fmt.Errorf("Fortsetzung: %w", err)
		}
	} else if s.RunOnStart {
		if _, err := host.SchedulerScheduleOneTime(5, "start", ""); err != nil {
			return fmt.Errorf("Startlauf: %w", err)
		}
	}
	return nil
}

func modeText(s settings) string {
	if s.DryRun {
		return "PROBELAUF (dry_run=true, es wird nichts geschrieben)"
	}
	return "SCHREIBMODUS"
}

const watchdogID = "albumfix-wd"

// ord macht aus Pfaden einen String, dessen Reihenfolge der von filepath.WalkDir entspricht (Namen je Ordner sortiert):
// "/" muss vor allen anderen Zeichen kommen, sonst werden "a-b/..." gegenüber "a/..." falsch einsortiert.
func ord(rel string) string { return strings.ReplaceAll(rel, "/", "\x00") }

// canWrite prüft, ob das Plugin in der Bibliothek schreiben darf - bevor Deezer-Anfragen verschwendet werden.
func canWrite(mount string) error {
	p := filepath.Join(mount, ".albumfix-write-test")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(p)
}

// OnCallback: ein Häppchen des Durchgangs.
func (p *albumfix) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	s := loadSettings()
	start := time.Now().Round(0) // Round(0) entfernt die Monotonic-Uhr: sie läuft im WASM-Sandbox nicht mit der echten Zeit
	hardDeadline = start.Add(hardBudget)
	run := loadRun()
	cont := req.Payload == "cont"

	if cont && !run.Active {
		return nil // veralteter Watchdog/Fortsetzung eines beendeten Durchgangs
	}
	if !cont { // neuer Durchgang (Zeitplan oder Start)
		if run.Active && time.Now().Unix()-run.Updated < 90 {
			logf(pdk.LogInfo, "Durchgang läuft bereits, überspringe")
			return nil
		}
		run = runState{Active: true}
		logf(pdk.LogInfo, "Durchgang startet - %s, min_score=%.2f, dur_tol=%.0fs (bis %.0fs bei exaktem Treffer), retry=%.0fd, limit=%d, Filter=%v",
			modeText(s), s.MinScore, s.DurTol, s.DurTolMax, s.RetryDays, s.Limit, s.PathFilter)
	} else if run.Current != "" {
		// Das letzte Häppchen ist bei dieser Datei abgestorben (30-s-Limit/Neustart): nicht in einer Schleife wiederholen
		saveFileState(run.CurrentKey, fileState{Status: "rej", Reason: "Abbruch/Zeitüberschreitung bei dieser Datei", Tol: s.DurTol, TolMax: s.DurTolMax, MinScore: s.MinScore, Dry: s.DryRun}, retryTTL(s))
		logf(pdk.LogWarn, "%s: Aufruf ist hier abgebrochen - Datei wird für %.0f Tage ausgelassen", run.Current, s.RetryDays)
		run.Current, run.CurrentKey = "", ""
	}
	if err := saveRun(run); err != nil { // Sperre sofort setzen, nicht erst am Ende des Häppchens
		return fmt.Errorf("Zustand speichern: %w", err)
	}
	// Wächter: stirbt dieser Aufruf (Kill nach 30 s, Neustart), setzt er den Durchgang fort
	_ = host.SchedulerCancelSchedule(watchdogID)
	_, _ = host.SchedulerScheduleOneTime(45, "cont", watchdogID)

	var plTracks []plEntry
	plOn := len(s.Playlists) > 0
	if plOn {
		if s.PlaylistUser == "" {
			return errors.New("playlists ist gesetzt, aber playlist_user fehlt")
		}
		var found []string
		var err error
		if plTracks, found, err = playlistTracks(s.PlaylistUser, s.Playlists); err != nil {
			return fmt.Errorf("Wiedergabelisten lesen: %w", err)
		}
		if !cont {
			logf(pdk.LogInfo, "Nur Wiedergabelisten %v: %d Titel", found, len(plTracks))
		}
		if len(plTracks) == 0 {
			logf(pdk.LogWarn, "keine Titel in den Wiedergabelisten %v gefunden (Name/Benutzer prüfen) - nichts zu tun", s.Playlists)
			_ = host.SchedulerCancelSchedule(watchdogID)
			return saveRun(runState{})
		}
	}

	libs, err := host.LibraryGetAllLibraries()
	if err != nil {
		return fmt.Errorf("Bibliotheken: %w", err)
	}
	if !cont && !s.DryRun { // Schreibtest vor der Arbeit
		for _, lib := range libs {
			if lib.MountPoint == "" {
				continue
			}
			if err := canWrite(lib.MountPoint); err != nil {
				logf(pdk.LogError, "Bibliothek %q ist nicht beschreibbar (%v): Schreibzugriff fehlt (navidrome plugin edit albumfix --write-access) oder Mount ist read-only. Bibliothek wird übersprungen.", lib.Name, err)
				run.Skip = append(run.Skip, lib.ID)
			}
		}
	}
	dz := newDeezer()
	var st passStats
	more, abortErr, fatalAbort, stop := false, error(nil), false, false
	abortDelay := int32(60)
	budget := time.Duration(s.BudgetMs) * time.Millisecond
	lastSave := time.Now().Round(0)
	onLookup = func(key, rel string) { // vor jeder Datei mit Deezer-Anfrage merken, woran wir gerade arbeiten
		run.Current, run.CurrentKey = rel, key
		_ = saveRun(run)
		lastSave = time.Now().Round(0)
	}
	limitReached := func() bool { return s.Limit > 0 && run.Looked+st.looked >= s.Limit }

libs:
	for _, lib := range libs {
		if lib.MountPoint == "" {
			logf(pdk.LogWarn, "Bibliothek %q hat keinen Dateizugriff (Mount fehlt)", lib.Name)
			continue
		}
		skip := false
		for _, id := range run.Skip {
			skip = skip || id == lib.ID
		}
		if skip || (run.Cursor != "" && lib.ID < run.CursorLB) {
			continue
		}
		_ = filepath.WalkDir(lib.MountPoint, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				logf(pdk.LogWarn, "%s: %v", path, err)
				return nil
			}
			if d.IsDir() {
				return nil
			}
			kind := fileKind(path)
			if kind == "" {
				return nil
			}
			rel, _ := filepath.Rel(lib.MountPoint, path)
			rel = filepath.ToSlash(rel)
			if run.Cursor != "" && lib.ID == run.CursorLB && ord(rel) <= run.Cursor {
				return nil
			}
			if !s.allows(rel) {
				return nil
			}
			if limitReached() {
				return fs.SkipAll // Limit erreicht = Durchgang beendet
			}
			if time.Now().Sub(start) > budget {
				more = true
				return fs.SkipAll
			}
			err = processFile(path, kind, fileKey(lib.ID, rel), rel, s, dz, &st, plOn, plTracks)
			run.Current, run.CurrentKey = "", ""
			if err != nil {
				switch {
				case errors.Is(err, errBudget):
					more = true
					return fs.SkipAll
				case errors.Is(err, errWriteFails):
					logf(pdk.LogError, "Durchgang abgebrochen: 3 Schreibfehler hintereinander - Rechte/Platz prüfen")
					fatalAbort = true
					return fs.SkipAll
				case errors.Is(err, errQuota):
					abortErr, more = err, true
					return fs.SkipAll
				case errors.Is(err, errNetwork):
					abortErr, more, abortDelay = err, true, 300
					return fs.SkipAll
				}
			}
			run.CursorLB, run.Cursor = lib.ID, ord(rel)
			if time.Now().Round(0).Sub(lastSave) > 5*time.Second { // Fortschritt zwischendurch sichern
				if err := saveRun(run); err != nil {
					logf(pdk.LogError, "Zustand nicht speicherbar (%v) - Durchgang endet", err)
					stop = true
					return fs.SkipAll
				}
				lastSave = time.Now().Round(0)
			}
			return nil
		})
		if more || abortErr != nil || fatalAbort || stop || limitReached() {
			break libs
		}
	}

	run.Changed += st.changed
	run.Looked += st.looked
	run.Rejected += st.rejected
	run.Failed += st.failed
	logf(pdk.LogInfo, "Häppchen: %d Lookups, %d ergänzt, %d abgelehnt, %d fertig/übersprungen, %d Fehler, %.1fs",
		st.looked, st.changed, st.rejected, st.skipped, st.failed, time.Now().Sub(start).Seconds())

	_ = host.SchedulerCancelSchedule(watchdogID)
	if more && !fatalAbort && !stop {
		delay := int32(2)
		if abortErr != nil {
			run.Aborts++
			delay = abortDelay
			logf(pdk.LogWarn, "%v - weiter in %d s (Abbruch %d von max. 5)", abortErr, delay, run.Aborts)
		} else {
			run.Aborts = 0
		}
		if run.Aborts > 5 {
			logf(pdk.LogError, "Durchgang ABGEBROCHEN (zu viele Fehler/Rate-Limits), nächster Versuch beim nächsten Zeitplan: %d Lookups, %d ergänzt", run.Looked, run.Changed)
			return saveRun(runState{})
		}
		if err := saveRun(run); err != nil {
			logf(pdk.LogError, "Zustand nicht speicherbar (%v) - Durchgang endet", err)
			return saveRun(runState{})
		}
		if _, err := host.SchedulerScheduleOneTime(delay, "cont", ""); err != nil { // leere ID: feste ID scheitert mit "already exists"
			return fmt.Errorf("neu einplanen: %w", err)
		}
		return nil
	}
	logf(pdk.LogInfo, "Durchgang fertig: %d Lookups, %d ergänzt, %d abgelehnt, %d Fehler (%s)", run.Looked, run.Changed, run.Rejected, run.Failed, modeText(s))
	return saveRun(runState{})
}

type passStats struct{ looked, changed, rejected, skipped, failed, writeFails, netFails int }

// onLookup wird vor jeder Datei mit Deezer-Anfrage aufgerufen (siehe OnCallback).
var onLookup func(key, rel string)

var (
	errWriteFails = errors.New("mehrfach nacheinander Schreibfehler")
	errNetwork    = errors.New("mehrfach nacheinander Netzwerkfehler bei Deezer")
)

func fileKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".opus":
		return "opus"
	case ".mp3":
		return "mp3"
	}
	return ""
}

type fileMeta struct {
	Album, Date, Title, Artist string
	HasCover                   bool
	Duration                   float64
}

func inspect(path, kind string, withDuration bool) (fileMeta, error) {
	if kind == "opus" {
		insp := opus.InspectFile
		if !withDuration {
			insp = opus.InspectTags // bei Opus muss man für die Laufzeit alle Seiten lesen: nur wenn nötig
		}
		fi, err := insp(path)
		return fileMeta{fi.Album, fi.Date, fi.Title, fi.Artist, fi.HasCover, fi.Duration}, err
	}
	fi, err := id3.InspectFile(path)
	return fileMeta{fi.Album, fi.Date, fi.Title, fi.Artist, fi.HasCover, fi.Duration}, err
}

func retryTTL(s settings) time.Duration { return time.Duration(s.RetryDays * 24 * float64(time.Hour)) }

func mtimeOf(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().Unix()
	}
	return 0
}

var unknownAlbums = map[string]bool{"": true, "unknown album": true, "unknown": true, "unbekanntes album": true}

// processFile prüft eine Datei und ergänzt sie bei sicherem Treffer. Fehler errBudget/errQuota brechen den Durchgang ab.
func processFile(path, kind, key, rel string, s settings, dz *deezerClient, st *passStats, plOn bool, plTracks []plEntry) error {
	if rec, err := fsutil.Recover(path); err != nil {
		st.failed++
		logf(pdk.LogError, "%s: Wiederherstellung fehlgeschlagen: %v", rel, err)
		return nil
	} else if rec {
		logf(pdk.LogWarn, "%s: Rest einer unterbrochenen Änderung wurde vollständig übernommen", rel)
	}
	mt := mtimeOf(path)
	now := time.Now().Unix()
	if prev, ok := loadFileState(key); ok && prev.MTime == mt {
		switch {
		case prev.Status == "rej" && prev.Dry == s.DryRun && prev.Tol == s.DurTol && prev.TolMax == s.DurTolMax && prev.MinScore == s.MinScore &&
			float64(now-prev.Tried) < s.RetryDays*86400:
			st.skipped++
			return nil
		}
	}
	if s.MinAgeMin > 0 && float64(now-mt) < s.MinAgeMin*60 {
		st.skipped++
		return nil
	}

	m, err := inspect(path, kind, false)
	if err != nil {
		st.failed++
		logf(pdk.LogWarn, "%s: %v", rel, err)
		return nil
	}
	if plOn && !inPlaylists(plTracks, m.Title, m.Artist) {
		st.skipped++
		return nil
	}
	needAlbum := unknownAlbums[match.Norm(m.Album)]
	needCover := !m.HasCover
	if !needAlbum && !needCover {
		st.skipped++
		return nil
	}
	reject := func(reason string) {
		st.rejected++
		logf(pdk.LogInfo, "abgelehnt: %s (%s)", rel, reason)
		saveFileState(key, fileState{Status: "rej", MTime: mt, Reason: reason, Tol: s.DurTol, TolMax: s.DurTolMax, MinScore: s.MinScore, Dry: s.DryRun}, retryTTL(s))
	}
	if m.Title == "" || m.Artist == "" {
		reject("Titel/Artist-Tag fehlt")
		return nil
	}

	if full, err := inspect(path, kind, true); err == nil {
		m.Duration = full.Duration
	}
	if onLookup != nil {
		onLookup(key, rel)
	}
	st.looked++
	album, score, durDiff, reason, err := dz.lookup(match.CleanArtist(m.Artist), match.CleanTitle(m.Artist, m.Title), m.Duration, s)
	if err != nil {
		if errors.Is(err, errBudget) || errors.Is(err, errQuota) {
			st.looked--
			return err
		}
		st.failed++
		st.netFails++
		logf(pdk.LogWarn, "%s: Deezer-Anfrage fehlgeschlagen: %v", rel, err)
		if st.netFails >= 5 {
			return errNetwork
		}
		return nil
	}
	st.netFails = 0
	if album == nil {
		reject(reason)
		return nil
	}

	if !needAlbum && needCover && match.Ratio(m.Album, album.Title) < 0.9 {
		// Das Cover gehört zum Deezer-Album: nur einbetten, wenn es zum Album im Tag passt
		reject(fmt.Sprintf("Album im Tag (%q) weicht vom Deezer-Album (%q) ab", m.Album, album.Title))
		return nil
	}

	var edit = struct {
		album, date string
		cover       []byte
	}{}
	if needAlbum {
		edit.album = album.Title
		if m.Date == "" && len(album.ReleaseDate) >= 4 {
			edit.date = album.ReleaseDate[:4]
		}
	}
	coverNote := "-"
	if needCover {
		coverNote = "ja"
		if !s.DryRun {
			b, err := dz.cover(*album)
			switch {
			case errors.Is(err, errBudget):
				st.looked--
				return err
			case err != nil:
				coverNote = "Download fehlgeschlagen"
				logf(pdk.LogWarn, "%s: Cover: %v", rel, err)
			default:
				edit.cover = b
			}
		}
	}
	desc := fmt.Sprintf("%s | %s - %s -> Album=%q Jahr=%q Cover=%s (Score %.2f, %s, Laufzeit-Δ %.0fs)",
		rel, m.Artist, m.Title, edit.album, edit.date, coverNote, score, album.RecordType, durDiff)

	if s.DryRun {
		logf(pdk.LogInfo, "PROBELAUF, würde ergänzen: %s", desc)
		st.changed++
		saveFileState(key, fileState{Status: "rej", MTime: mt, Reason: "Probelauf", Tol: s.DurTol, TolMax: s.DurTolMax, MinScore: s.MinScore, Dry: true}, retryTTL(s))
		return nil
	}

	var changed bool
	if kind == "opus" {
		changed, err = opus.Rewrite(path, opus.Edit{Album: edit.album, Date: edit.date, Cover: edit.cover, ReplaceAlbum: needAlbum})
	} else {
		changed, err = id3.Rewrite(path, id3.Edit{Album: edit.album, Date: edit.date, Cover: edit.cover, ReplaceAlbum: needAlbum})
	}
	if err != nil {
		st.failed++
		st.writeFails++
		hint := ""
		if e := strings.ToLower(err.Error()); strings.Contains(e, "read-only") || strings.Contains(e, "permission") || strings.Contains(e, "not permitted") {
			hint = " - hat das Plugin Schreibzugriff auf die Bibliothek? (navidrome plugin edit albumfix --write-access)"
		}
		logf(pdk.LogError, "%s: schreiben fehlgeschlagen: %v%s", rel, err, hint)
		if st.writeFails >= 3 {
			return errWriteFails
		}
		return nil
	}
	if !changed {
		// nichts geschrieben (z. B. unbekannter Aufbau): nicht als erledigt merken, aber auch nicht jeden Lauf neu abfragen
		st.failed++
		logf(pdk.LogWarn, "%s: nichts geschrieben, obwohl Treffer vorhanden", rel)
		saveFileState(key, fileState{Status: "rej", MTime: mtimeOf(path), Reason: "nichts geschrieben", Tol: s.DurTol, TolMax: s.DurTolMax, MinScore: s.MinScore}, retryTTL(s))
		return nil
	}
	st.changed++
	st.writeFails = 0
	logf(pdk.LogInfo, "ergänzt: %s", desc)
	if needCover && edit.cover == nil {
		// Album steht, Cover fehlt noch: später erneut versuchen
		saveFileState(key, fileState{Status: "rej", MTime: mtimeOf(path), Reason: "Cover-Download fehlgeschlagen", Tol: s.DurTol, TolMax: s.DurTolMax, MinScore: s.MinScore}, retryTTL(s))
		return nil
	}
	return nil
}

func main() {}
