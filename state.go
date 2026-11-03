package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

// fileState merkt sich pro Datei, ob sie fertig ist oder abgelehnt wurde (wie die Zustandsdatei des Python-Taggers).
type fileState struct {
	Status   string  `json:"s"`           // rej (fertige Dateien werden nicht gespeichert)
	MTime    int64   `json:"m"`           // Änderungszeit der Datei beim Eintrag
	Tried    int64   `json:"t"`           // Unix-Zeit des Versuchs
	Reason   string  `json:"r,omitempty"` // Grund bei rej
	Tol      float64 `json:"d,omitempty"`
	TolMax   float64 `json:"x,omitempty"`
	MinScore float64 `json:"c,omitempty"`
	Dry      bool    `json:"y,omitempty"` // Eintrag stammt aus einem Probelauf
}

// runState ist der Fortschritt des laufenden Durchgangs über mehrere Plugin-Aufrufe.
type runState struct {
	Active     bool    `json:"active"`
	Updated    int64   `json:"updated"`
	CursorLB   int32   `json:"lib"`
	Cursor     string  `json:"cursor"`
	Aborts     int     `json:"aborts"`
	Failed     int     `json:"failed"`
	Skip       []int32 `json:"skip,omitempty"` // Bibliotheken ohne Schreibzugriff
	Current    string  `json:"cur,omitempty"`
	CurrentKey string  `json:"curk,omitempty"`
	Changed    int     `json:"changed"`
	Looked     int     `json:"looked"`
	Rejected   int     `json:"rejected"`
}

func fileKey(libID int32, rel string) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d/%s", libID, rel)
	return fmt.Sprintf("f:%016x", h.Sum64())
}

func loadFileState(key string) (fileState, bool) {
	b, ok, err := host.KVStoreGet(key)
	if err != nil || !ok {
		return fileState{}, false
	}
	var st fileState
	if json.Unmarshal(b, &st) != nil {
		return fileState{}, false
	}
	return st, true
}

var kvErrLogged bool

// saveFileState speichert abgelehnte Dateien mit Ablaufzeit (retry_days): der KVStore ist auf ca. 1 MB begrenzt,
// Einträge für fertige Dateien gibt es deshalb gar nicht - sie erkennt man an den Tags.
func saveFileState(key string, st fileState, ttl time.Duration) {
	st.Tried = time.Now().Unix()
	b, err := json.Marshal(st)
	if err == nil {
		if ttl < time.Hour {
			ttl = time.Hour
		}
		err = host.KVStoreSetWithTTL(key, b, int64(ttl.Seconds()))
	}
	if err != nil && !kvErrLogged {
		kvErrLogged = true
		logf(pdk.LogWarn, "Zustand konnte nicht gespeichert werden (Speicher voll?): %v - abgelehnte Dateien werden dann erneut abgefragt", err)
	}
}

func loadRun() runState {
	var r runState
	if b, ok, err := host.KVStoreGet("run"); err == nil && ok {
		_ = json.Unmarshal(b, &r)
	}
	return r
}

func saveRun(r runState) error {
	r.Updated = time.Now().Unix()
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return host.KVStoreSet("run", b)
}
