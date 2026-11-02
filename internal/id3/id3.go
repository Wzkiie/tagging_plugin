// Package id3 ergänzt in MP3-Dateien fehlende ID3v2-Tags (Album, Jahr, Cover).
//
// Reine Standardbibliothek (läuft als WASM-Plugin). Der Tag wird vorn neu geschrieben, das Audio
// wird unverändert per Streaming durchgereicht. Vorhandene Frames bleiben unangetastet.
package id3

import (
	"albumfix/internal/fsutil"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"strings"
	"unicode/utf16"
)

// Edit beschreibt, was ergänzt werden soll. Vorhandene Werte werden nie überschrieben.
type Edit struct {
	Album string
	Date  string // Jahr, z. B. "2019"
	Cover []byte // JPEG/PNG

	// ReplaceAlbum: ein vorhandenes (Platzhalter-)Album wie "Unknown Album" ersetzen statt nur leere zu füllen.
	ReplaceAlbum bool
}

// FileInfo zeigt den aktuellen Zustand einer Datei.
type FileInfo struct {
	Album, Date, Title, Artist string
	HasCover                   bool
	Duration                   float64 // Sekunden, 0 wenn unbekannt
}

type tag struct {
	major   byte
	flags   byte
	size    int // Größe des Tag-Körpers ohne 10-Byte-Header
	body    []byte
	frames  map[string][]byte   // erste Instanz je Frame-ID (Inhalt ohne Header)
	all     map[string][][]byte // alle Instanzen je Frame-ID
	extLen  int                 // Länge des erweiterten Headers am Anfang des Körpers
	usedEnd int                 // Ende des letzten gültigen Frames im Körper (ohne Padding)
	bad     bool                // nach usedEnd steht etwas, das kein Padding ist (Frame-Struktur unklar)
}

func validID(b []byte) bool {
	for _, c := range b {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func syncsafe(b []byte) int {
	return int(b[0]&0x7f)<<21 | int(b[1]&0x7f)<<14 | int(b[2]&0x7f)<<7 | int(b[3]&0x7f)
}

func putSyncsafe(n int) []byte {
	return []byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
}

// readTag liest den ID3v2-Tag am Dateianfang; nil, wenn keiner vorhanden ist.
func readTag(r io.Reader) (*tag, error) {
	var h [10]byte
	n, err := io.ReadFull(r, h[:])
	if err != nil || n < 10 || string(h[:3]) != "ID3" {
		return nil, nil
	}
	if h[3] < 2 || h[3] > 4 {
		return nil, fmt.Errorf("ID3v2.%d nicht unterstützt", h[3])
	}
	t := &tag{major: h[3], flags: h[5], size: syncsafe(h[6:10])}
	if t.size > 32<<20 {
		return nil, errors.New("ID3-Tag unplausibel groß")
	}
	t.body = make([]byte, t.size)
	if _, err := io.ReadFull(r, t.body); err != nil {
		return nil, errors.New("ID3-Tag abgeschnitten")
	}
	t.parseFrames()
	return t, nil
}

func (t *tag) parseFrames() {
	t.frames = map[string][]byte{}
	t.all = map[string][][]byte{}
	pos := 0
	if t.flags&0x40 != 0 && t.major >= 3 && len(t.body) >= 4 { // erweiterter Header
		if t.major == 4 {
			pos = syncsafe(t.body)
		} else {
			pos = 4 + int(binary.BigEndian.Uint32(t.body))
		}
		if pos < 0 || pos > len(t.body) {
			pos = 0
			t.bad = true
		}
	}
	t.extLen = pos
	if t.major == 2 {
		t.usedEnd = len(t.body)
		return
	}
	t.usedEnd = pos
	for pos+10 <= len(t.body) {
		if t.body[pos] == 0 {
			break // Padding
		}
		var sz int
		if t.major == 4 {
			sz = syncsafe(t.body[pos+4:])
		} else {
			sz = int(binary.BigEndian.Uint32(t.body[pos+4:]))
		}
		if !validID(t.body[pos:pos+4]) || sz < 0 || pos+10+sz > len(t.body) {
			break
		}
		id := string(t.body[pos : pos+4])
		content := t.body[pos+10 : pos+10+sz]
		if _, ok := t.frames[id]; !ok {
			t.frames[id] = content
		}
		t.all[id] = append(t.all[id], content)
		pos += 10 + sz
		t.usedEnd = pos
	}
	if len(bytes.TrimRight(t.body[t.usedEnd:], "\x00")) > 0 {
		t.bad = true // Rest ist kein Padding: nicht anfassen, sonst gehen Frames verloren
	}
}

// frameType liefert den Bildtyp eines APIC-Frames (3 = Front Cover), -1 bei kaputtem Frame.
func apicType(b []byte) int {
	if len(b) < 4 {
		return -1
	}
	i := bytes.IndexByte(b[1:], 0)
	if i < 0 || 1+i+1 >= len(b) {
		return -1
	}
	return int(b[1+i+1])
}

func (t *tag) hasFrontCover() bool {
	for _, f := range t.all["APIC"] {
		if ty := apicType(f); ty == 3 || ty == 0 {
			return true
		}
	}
	return false
}

// withoutFrames entfernt alle Frames mit den IDs aus body (body beginnt direkt mit dem ersten Frame).
func withoutFrames(body []byte, major byte, ids ...string) []byte {
	drop := map[string]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	var out []byte
	for pos := 0; pos+10 <= len(body); {
		var sz int
		if major == 4 {
			sz = syncsafe(body[pos+4:])
		} else {
			sz = int(binary.BigEndian.Uint32(body[pos+4:]))
		}
		end := pos + 10 + sz
		if end > len(body) {
			break
		}
		if !drop[string(body[pos:pos+4])] {
			out = append(out, body[pos:end]...)
		}
		pos = end
	}
	return out
}

func decodeText(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	enc, b := b[0], b[1:]
	switch enc {
	case 1, 2:
		be := enc == 2
		if len(b) >= 2 && b[0] == 0xff && b[1] == 0xfe {
			b, be = b[2:], false
		} else if len(b) >= 2 && b[0] == 0xfe && b[1] == 0xff {
			b, be = b[2:], true
		}
		var u []uint16
		for i := 0; i+1 < len(b); i += 2 {
			v := binary.LittleEndian.Uint16(b[i:])
			if be {
				v = binary.BigEndian.Uint16(b[i:])
			}
			if v == 0 {
				break
			}
			u = append(u, v)
		}
		return strings.TrimSpace(string(utf16.Decode(u)))
	case 0:
		rs := make([]rune, 0, len(b))
		for _, c := range b {
			if c == 0 {
				break
			}
			rs = append(rs, rune(c))
		}
		return strings.TrimSpace(string(rs))
	default:
		if i := bytes.IndexByte(b, 0); i >= 0 {
			b = b[:i]
		}
		return strings.TrimSpace(string(b))
	}
}

func (t *tag) text(id string) string {
	if f, ok := t.frames[id]; ok {
		return decodeText(f)
	}
	return ""
}

func (t *tag) info() FileInfo {
	fi := FileInfo{Album: t.text("TALB"), Title: t.text("TIT2"), Artist: t.text("TPE1")}
	fi.Date = t.text("TDRC")
	if fi.Date == "" {
		fi.Date = t.text("TYER")
	}
	fi.HasCover = t.hasFrontCover()
	return fi
}

// InspectFile liest Tags und Laufzeit, ohne das Audio zu laden.
func InspectFile(path string) (FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileInfo{}, err
	}
	defer f.Close()
	t, err := readTag(f)
	if err != nil {
		return FileInfo{}, err
	}
	var fi FileInfo
	audioStart := int64(0)
	if t != nil {
		fi = t.info()
		audioStart = int64(10 + t.size)
	}
	st, err := f.Stat()
	if err != nil {
		return fi, err
	}
	fi.Duration = duration(f, audioStart, st.Size())
	return fi, nil
}

var (
	brV1 = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	brV2 = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	srV1 = [3]int{44100, 48000, 32000}
)

// duration schätzt die Laufzeit einer Layer-III-Datei: Xing/Info-Frameanzahl, sonst CBR aus Dateigröße.
// Ohne Xing-Header und mit wechselnder Bitrate (VBR) ist die Laufzeit nicht bestimmbar: 0 = unbekannt.
func duration(f *os.File, start, size int64) float64 {
	buf := make([]byte, 64<<10)
	n, _ := f.ReadAt(buf, start)
	buf = buf[:n]
	for i := 0; i+4 <= len(buf); i++ {
		if buf[i] != 0xff || buf[i+1]&0xe0 != 0xe0 {
			continue
		}
		ver := buf[i+1] >> 3 & 3 // 3 = MPEG1, 2 = MPEG2, 0 = MPEG2.5
		layer := buf[i+1] >> 1 & 3
		bri, sri := int(buf[i+2]>>4), int(buf[i+2]>>2&3)
		if ver == 1 || layer != 1 || bri == 0 || bri == 15 || sri == 3 {
			continue
		}
		br := brV2[bri]
		sr := srV1[sri]
		spf := 576
		if ver == 3 {
			br, spf = brV1[bri], 1152
		} else if ver == 2 {
			sr /= 2
		} else {
			sr /= 4
		}
		mono := buf[i+3]>>6 == 3
		side := 32
		switch {
		case ver == 3 && mono:
			side = 17
		case ver != 3 && !mono:
			side = 17
		case ver != 3 && mono:
			side = 9
		}
		if x := i + 4 + side; x+12 <= len(buf) {
			if id := string(buf[x : x+4]); (id == "Xing" || id == "Info") && buf[x+7]&1 != 0 {
				frames := binary.BigEndian.Uint32(buf[x+8:])
				return float64(frames) * float64(spf) / float64(sr)
			}
		}
		// kein Xing: nur bei konstanter Bitrate rechnen; die nächsten Frames müssen dieselbe haben
		pos := i
		for k := 0; k < 40; k++ {
			if pos+4 > len(buf) {
				break
			}
			if buf[pos] != 0xff || buf[pos+1]&0xe0 != 0xe0 || int(buf[pos+2]>>4) != bri {
				return 0
			}
			pad := int(buf[pos+2] >> 1 & 1)
			pos += spf/8*br*1000/sr + pad
		}
		return float64(size-start-int64(i)) * 8 / float64(br*1000)
	}
	return 0
}

func textFrame(major byte, id, val string) []byte {
	var body []byte
	if major == 4 {
		body = append([]byte{3}, val...)
	} else {
		body = []byte{1, 0xff, 0xfe}
		for _, u := range utf16.Encode([]rune(val)) {
			body = binary.LittleEndian.AppendUint16(body, u)
		}
	}
	return frame(major, id, body)
}

func frame(major byte, id string, body []byte) []byte {
	out := []byte(id)
	if major == 4 {
		out = append(out, putSyncsafe(len(body))...)
	} else {
		out = binary.BigEndian.AppendUint32(out, uint32(len(body)))
	}
	out = append(out, 0, 0)
	return append(out, body...)
}

func pictureFrame(major byte, img []byte) ([]byte, error) {
	_, format, err := image.DecodeConfig(bytes.NewReader(img))
	if err != nil {
		return nil, fmt.Errorf("Cover nicht lesbar: %w", err)
	}
	body := []byte{0}
	body = append(body, "image/"+format...)
	body = append(body, 0, 3, 0) // Mime-Ende, Typ Front Cover, leere Beschreibung
	body = append(body, img...)
	return frame(major, "APIC", body), nil
}

// newFrames baut die Frames, die laut e fehlen, und nennt die vorhandenen (leeren/Platzhalter-)Frames, die dafür weichen.
func newFrames(t *tag, e Edit) (add []byte, drop []string) {
	if e.Album != "" && (e.ReplaceAlbum || t.text("TALB") == "") {
		add = append(add, textFrame(t.major, "TALB", e.Album)...)
		drop = append(drop, "TALB")
	}
	if e.Date != "" && t.text("TDRC") == "" && t.text("TYER") == "" {
		id := "TYER"
		if t.major == 4 {
			id = "TDRC"
		}
		add = append(add, textFrame(t.major, id, e.Date)...)
		drop = append(drop, "TDRC", "TYER")
	}
	if len(e.Cover) > 0 && !t.hasFrontCover() {
		if f, err := pictureFrame(t.major, e.Cover); err == nil {
			add = append(add, f...)
		}
	}
	return add, drop
}

// Rewrite ergänzt fehlende Felder direkt in der Datei (Temp-Datei + atomares Umbenennen).
func Rewrite(path string, e Edit) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	t, err := readTag(f)
	if err != nil {
		return false, err
	}
	audioStart := int64(0)
	if t == nil {
		t = &tag{major: 3, frames: map[string][]byte{}}
	} else {
		if t.major == 2 {
			return false, errors.New("ID3v2.2 nicht unterstützt")
		}
		if t.flags&0x80 != 0 || t.flags&0x10 != 0 {
			return false, errors.New("ID3-Tag mit Unsynchronisation/Footer nicht unterstützt")
		}
		if t.bad {
			return false, errors.New("ID3-Tag: Frame-Struktur unklar, Datei bleibt unverändert")
		}
		audioStart = int64(10 + t.size)
	}
	add, drop := newFrames(t, e)
	if len(add) == 0 {
		return false, nil
	}
	if err := fsutil.CheckWritable(path); err != nil {
		return false, err
	}
	// erweiterter Header (Padding-Größe/CRC) und Padding entfallen; die Frames bleiben unverändert erhalten
	kept := append([]byte{}, t.body[t.extLen:t.usedEnd]...)
	if len(t.body) > 0 {
		kept = withoutFrames(kept, t.major, drop...)
	}
	body := append(kept, add...)

	tmp := path + ".albumfix-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return false, err
	}
	fail := func(err error) (bool, error) {
		out.Close()
		os.Remove(tmp)
		return false, err
	}
	hdr := append([]byte("ID3"), t.major, 0, t.flags&^0xC0)
	hdr = append(hdr, putSyncsafe(len(body))...)
	if _, err := out.Write(hdr); err != nil {
		return fail(err)
	}
	if _, err := out.Write(body); err != nil {
		return fail(err)
	}
	if _, err := f.Seek(audioStart, io.SeekStart); err != nil {
		return fail(err)
	}
	if _, err := io.Copy(out, f); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return false, err
	}
	if err := fsutil.Replace(tmp, path); err != nil {
		return false, err
	}
	return true, nil
}
