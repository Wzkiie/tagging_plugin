// Package fsutil enthält Hilfen zum sicheren Ersetzen von Dateien.
package fsutil

import (
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	TmpSuffix   = ".albumfix-tmp"   // wird gerade geschrieben (unvollständig)
	ReadySuffix = ".albumfix-ready" // vollständig und geprüft, wartet auf das Übernehmen
)

// CheckWritable prüft vor dem Anlegen großer Temp-Dateien, ob orig überhaupt beschreibbar ist.
func CheckWritable(orig string) error {
	f, err := os.OpenFile(orig, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

// Replace ersetzt orig durch den Inhalt von tmp (= orig+TmpSuffix, vollständig geschrieben und geschlossen).
// Normal atomar per Rename. Unter WASI siehe replace_wasi.go.
func Replace(tmp, orig string) error { return replace(tmp, orig) }

// Recover räumt Reste einer unterbrochenen Änderung auf: eine fertige Kopie (.albumfix-ready) wird in die Datei
// übernommen, eine unvollständige (.albumfix-tmp) verworfen. Liefert true, wenn eine Datei wiederhergestellt wurde.
func Recover(orig string) (bool, error) {
	ready := orig + ReadySuffix
	_ = os.Remove(orig + TmpSuffix)
	if _, err := os.Stat(ready); err != nil {
		return false, nil
	}
	if err := finishReady(ready, orig); err != nil {
		return false, err
	}
	return true, nil
}

func renameReplace(tmp, orig string) error {
	if err := os.Rename(tmp, orig); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// copyReplace übernimmt tmp in die vorhandene Datei orig (Inode, Rechte, Besitzer bleiben erhalten).
// Write-Ahead: tmp wird erst mit Sync abgesichert und atomar zu .albumfix-ready umbenannt. Erst danach wird die
// Originaldatei überschrieben - ohne O_TRUNC und mit anschließendem Kürzen. Bricht das ab (Absturz, 30-s-Limit,
// Platte voll), bleibt .ready liegen und wird beim nächsten Lauf von Recover vollendet; das Original ist bis dahin
// eine Mischung aus alt und neu, aber die vollständige neue Datei existiert.
func copyReplace(tmp, orig string) error {
	if f, err := os.OpenFile(tmp, os.O_RDWR, 0); err == nil {
		_ = f.Sync()
		f.Close()
	}
	ready := orig + ReadySuffix
	if err := os.Rename(tmp, ready); err != nil {
		os.Remove(tmp)
		return err
	}
	return finishReady(ready, orig)
}

func finishReady(ready, orig string) error {
	src, err := os.Open(ready)
	if err != nil {
		return err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(orig, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		os.Remove(ready) // Original unberührt, nichts zu retten
		return err
	}
	_, err = io.Copy(dst, src)
	if err == nil {
		err = dst.Truncate(st.Size())
	}
	if err == nil {
		err = dst.Sync()
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if fi, serr := os.Stat(orig); serr != nil || fi.Size() != st.Size() {
			err = errors.New("Größe nach dem Kopieren stimmt nicht")
		}
	}
	if err != nil {
		return fmt.Errorf("Übernehmen fehlgeschlagen, vollständige neue Datei liegt in %s (wird beim nächsten Lauf erneut übernommen): %w", ready, err)
	}
	return os.Remove(ready)
}
