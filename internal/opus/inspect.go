package opus

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

// FileInfo beschreibt eine Opus-Datei, ohne sie komplett zu laden.
type FileInfo struct {
	Info
	Duration float64 // Sekunden (aus der letzten Granule Position), 0 wenn unbekannt
}

// InspectFile liest Tags und Laufzeit seitenweise (konstanter Speicher, Audio wird übersprungen).
func InspectFile(path string) (FileInfo, error) { return inspectFile(path, true) }

// InspectTags liest nur die Tags (kein Durchlesen der Audioseiten, Duration bleibt 0). Für Dateien, die schon vollständig sind.
func InspectTags(path string) (FileInfo, error) { return inspectFile(path, false) }

func inspectFile(path string, withDuration bool) (FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileInfo{}, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)

	raw0, err := readRawPage(br)
	if err != nil {
		return FileInfo{}, err
	}
	p0 := pageOf(raw0)
	if p0.headerType&0x02 == 0 || !bytes.HasPrefix(p0.payload, []byte("OpusHead")) || len(p0.payload) < 12 {
		return FileInfo{}, errors.New("keine Opus-Datei")
	}
	preSkip := uint64(binary.LittleEndian.Uint16(p0.payload[10:12]))

	var packet []byte
	for done := false; !done; {
		raw, err := readRawPage(br)
		if err != nil {
			return FileInfo{}, errLayout
		}
		p := pageOf(raw)
		packet = append(packet, p.payload...)
		if len(packet) > maxHeader {
			return FileInfo{}, errLayout
		}
		for j, s := range p.segs {
			if s < 255 {
				if j != len(p.segs)-1 {
					return FileInfo{}, errLayout
				}
				done = true
				break
			}
		}
	}
	t, err := parseTags(packet)
	if err != nil {
		return FileInfo{}, err
	}
	fi := FileInfo{Info: Info{
		Album: t.get("ALBUM"), Date: t.get("DATE"), Title: t.get("TITLE"), Artist: t.get("ARTIST"),
		HasCover: t.has("METADATA_BLOCK_PICTURE"),
	}}

	if !withDuration {
		return fi, nil
	}
	// Audioseiten nur überfliegen: Header lesen, Payload verwerfen, letzte Granule merken.
	var last uint64
	for {
		var hdr [27]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			break
		}
		if string(hdr[:4]) != "OggS" {
			break
		}
		segs := make([]byte, hdr[26])
		if _, err := io.ReadFull(br, segs); err != nil {
			break
		}
		size := 0
		for _, s := range segs {
			size += int(s)
		}
		if _, err := br.Discard(size); err != nil {
			break
		}
		if g := binary.LittleEndian.Uint64(hdr[6:]); g != ^uint64(0) {
			last = g
		}
	}
	if last > preSkip {
		fi.Duration = float64(last-preSkip) / 48000
	}
	return fi, nil
}
