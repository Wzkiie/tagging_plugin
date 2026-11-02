// albumfix (Machbarkeitstest): ergänzt in .opus-Dateien der Navidrome-Bibliothek fehlende
// Album-/Jahr-Tags und ein eingebettetes Cover - direkt über den WASI-Mount der Bibliothek.
//
// Ablauf: OnInit plant einen einmaligen Scheduler-Aufruf, der Callback läuft durch alle
// freigegebenen Bibliotheken und schreibt (nur wenn die Felder fehlen) per Temp-Datei + Rename.
//
// Bauen:  GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
package main

import (
	_ "embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"albumfix/internal/opus"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
)

//go:embed cover.jpg
var testCover []byte

// mode wird für Messungen per -ldflags gesetzt: read | apply | full
var mode = "full"

const (
	testAlbum  = "Spike Album"
	testDate   = "2024"
	scheduleID = "albumfix-run"
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

// OnInit startet den Lauf ein paar Sekunden nach dem Laden des Plugins.
func (p *albumfix) OnInit() error {
	if _, err := host.SchedulerScheduleOneTime(3, "", scheduleID); err != nil {
		return fmt.Errorf("Scheduler: %w", err)
	}
	logf(pdk.LogInfo, "Lauf in 3 s geplant")
	return nil
}

// budget: ein Plugin-Aufruf wird von Navidrome nach 30 s abgebrochen, daher in Häppchen arbeiten.
var budgetMs = "20000"

func budgetDur() time.Duration {
	n, _ := strconv.Atoi(budgetMs)
	return time.Duration(n) * time.Millisecond
}

// OnCallback läuft durch alle eingehängten Bibliotheken; bei Zeitüberschreitung plant es sich neu ein.
func (p *albumfix) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	libs, err := host.LibraryGetAllLibraries()
	if err != nil {
		return fmt.Errorf("Bibliotheken: %w", err)
	}
	start := time.Now()
	more := false
	var scanned, changed, failed int
	for _, lib := range libs {
		if lib.MountPoint == "" {
			logf(pdk.LogWarn, "Bibliothek %q hat keinen Dateizugriff (Mount fehlt)", lib.Name)
			continue
		}
		_ = filepath.WalkDir(lib.MountPoint, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				logf(pdk.LogWarn, "%s: %v", path, err)
				return nil
			}
			if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".opus") {
				return nil
			}
			if time.Since(start) > budgetDur() {
				more = true
				return fs.SkipAll
			}
			scanned++
			ok, err := fixFile(path)
			switch {
			case err != nil:
				failed++
				logf(pdk.LogWarn, "%s: %v", path, err)
			case ok:
				changed++
			}
			return nil
		})
		if more {
			break
		}
	}
	logf(pdk.LogInfo, "Durchgang: %d geprüft, %d geändert, %d Fehler, %.1fs, weiter=%v", scanned, changed, failed, time.Since(start).Seconds(), more)
	if more {
		if _, err := host.SchedulerScheduleOneTime(2, "", ""); err != nil {
			return fmt.Errorf("neu einplanen: %w", err)
		}
	} else {
		logf(pdk.LogInfo, "fertig")
	}
	return nil
}

// fixFile ergänzt fehlende Felder im Streaming-Verfahren (konstanter Speicher).
func fixFile(path string) (bool, error) {
	if mode == "read" {
		return false, nil
	}
	return opus.Rewrite(path, opus.Edit{Album: testAlbum, Date: testDate, Cover: testCover})
}

func main() {}
