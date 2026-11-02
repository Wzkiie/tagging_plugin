// Package opus ergänzt in Ogg-Opus-Dateien fehlende Tags (ALBUM, DATE) und ein eingebettetes Cover.
//
// Reine Go-Standardbibliothek, keine Abhängigkeiten, damit es auch als WASM-Plugin läuft.
// Es wird nur das Kommentar-Paket (OpusTags) ersetzt; die Audiodaten bleiben byte-identisch.
// Dabei werden die Ogg-Seiten neu verpackt: Seitennummern der Audioseiten und die CRC
// werden angepasst, alles andere (Granule Position, Serial) bleibt unverändert.
package opus

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

// Edit beschreibt, was ergänzt werden soll. Vorhandene Werte werden nie überschrieben.
type Edit struct {
	Album string // wird nur gesetzt, wenn noch kein ALBUM-Tag existiert
	Date  string // wird nur gesetzt, wenn noch kein DATE-Tag existiert
	Cover []byte // JPEG/PNG; wird nur eingebettet, wenn noch kein Cover existiert
}

// Info zeigt den aktuellen Zustand einer Datei.
type Info struct {
	Album, Date, Title, Artist string
	HasCover                   bool
}

var errLayout = errors.New("unerwartetes Ogg-Layout")

// ---------------------------------------------------------------- Ogg-Seiten
var crcTable [256]uint32

func init() {
	for i := 0; i < 256; i++ {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		crcTable[i] = r
	}
}

func oggCRC(b []byte) uint32 {
	var crc uint32
	for _, v := range b {
		crc = (crc << 8) ^ crcTable[byte(crc>>24)^v]
	}
	return crc
}

type page struct {
	headerType byte
	granule    uint64
	serial     uint32
	seq        uint32
	segs       []byte
	payload    []byte
}

func (p *page) bytes() []byte {
	out := make([]byte, 0, 27+len(p.segs)+len(p.payload))
	out = append(out, 'O', 'g', 'g', 'S', 0, p.headerType)
	out = binary.LittleEndian.AppendUint64(out, p.granule)
	out = binary.LittleEndian.AppendUint32(out, p.serial)
	out = binary.LittleEndian.AppendUint32(out, p.seq)
	out = append(out, 0, 0, 0, 0, byte(len(p.segs)))
	out = append(out, p.segs...)
	out = append(out, p.payload...)
	binary.LittleEndian.PutUint32(out[22:26], oggCRC(out))
	return out
}

func parsePages(b []byte) ([]page, error) {
	var pages []page
	for off := 0; off < len(b); {
		if len(b)-off < 27 || string(b[off:off+4]) != "OggS" || b[off+4] != 0 {
			return nil, errors.New("kein gültiger Ogg-Strom")
		}
		nseg := int(b[off+26])
		if len(b)-off < 27+nseg {
			return nil, errors.New("abgeschnittene Ogg-Seite")
		}
		segs := b[off+27 : off+27+nseg]
		size := 0
		for _, s := range segs {
			size += int(s)
		}
		end := off + 27 + nseg + size
		if end > len(b) {
			return nil, errors.New("abgeschnittene Ogg-Seite")
		}
		pages = append(pages, page{
			headerType: b[off+5],
			granule:    binary.LittleEndian.Uint64(b[off+6:]),
			serial:     binary.LittleEndian.Uint32(b[off+14:]),
			seq:        binary.LittleEndian.Uint32(b[off+18:]),
			segs:       segs,
			payload:    b[off+27+nseg : end],
		})
		off = end
	}
	return pages, nil
}

// ---------------------------------------------------------------- OpusTags
type tags struct {
	vendor   string
	comments []string
	tail     []byte
}

func parseTags(p []byte) (*tags, error) {
	if len(p) < 16 || string(p[:8]) != "OpusTags" {
		return nil, errors.New("kein OpusTags-Paket")
	}
	pos := 8
	u32 := func() (int, error) {
		if pos+4 > len(p) {
			return 0, errors.New("OpusTags abgeschnitten")
		}
		v := int(binary.LittleEndian.Uint32(p[pos:]))
		pos += 4
		return v, nil
	}
	vl, err := u32()
	if err != nil || vl < 0 || pos+vl > len(p) {
		return nil, errors.New("OpusTags: ungültiger Vendor")
	}
	t := &tags{vendor: string(p[pos : pos+vl])}
	pos += vl
	n, err := u32()
	if err != nil {
		return nil, err
	}
	for i := 0; i < n; i++ {
		l, err := u32()
		if err != nil || l < 0 || pos+l > len(p) {
			return nil, errors.New("OpusTags: ungültiger Kommentar")
		}
		t.comments = append(t.comments, string(p[pos:pos+l]))
		pos += l
	}
	t.tail = p[pos:]
	return t, nil
}

func (t *tags) bytes() []byte {
	out := []byte("OpusTags")
	out = binary.LittleEndian.AppendUint32(out, uint32(len(t.vendor)))
	out = append(out, t.vendor...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(t.comments)))
	for _, c := range t.comments {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(c)))
		out = append(out, c...)
	}
	return append(out, t.tail...)
}

func (t *tags) get(key string) string {
	for _, c := range t.comments {
		if k, v, ok := strings.Cut(c, "="); ok && strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func (t *tags) has(key string) bool { return t.get(key) != "" }

func pictureBlock(img []byte) (string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(img))
	if err != nil {
		return "", fmt.Errorf("Cover nicht lesbar: %w", err)
	}
	mime := "image/" + format
	var b []byte
	be := func(v uint32) { b = binary.BigEndian.AppendUint32(b, v) }
	be(3) // Front Cover
	be(uint32(len(mime)))
	b = append(b, mime...)
	be(0) // Beschreibung leer
	be(uint32(cfg.Width))
	be(uint32(cfg.Height))
	be(24)
	be(0)
	be(uint32(len(img)))
	b = append(b, img...)
	return "METADATA_BLOCK_PICTURE=" + base64.StdEncoding.EncodeToString(b), nil
}

// ---------------------------------------------------------------- Kopfpakete finden
// headerPages liefert das Kommentar-Paket und den Index der letzten Seite, die dazugehört.
func headerPages(pages []page) (packet []byte, last int, err error) {
	if len(pages) < 2 || pages[0].headerType&0x02 == 0 || !bytes.HasPrefix(pages[0].payload, []byte("OpusHead")) {
		return nil, 0, errors.New("keine Opus-Datei")
	}
	for i := 1; i < len(pages); i++ {
		packet = append(packet, pages[i].payload...)
		for j, s := range pages[i].segs {
			if s < 255 { // Paket endet hier
				if j != len(pages[i].segs)-1 {
					return nil, 0, errLayout // Audio auf derselben Seite wie der Kommentar-Header
				}
				return packet, i, nil
			}
		}
	}
	return nil, 0, errLayout
}

// Inspect liest die wichtigsten Tags und ob ein Cover vorhanden ist.
func Inspect(data []byte) (Info, error) {
	pages, err := parsePages(data)
	if err != nil {
		return Info{}, err
	}
	pkt, _, err := headerPages(pages)
	if err != nil {
		return Info{}, err
	}
	t, err := parseTags(pkt)
	if err != nil {
		return Info{}, err
	}
	return Info{
		Album: t.get("ALBUM"), Date: t.get("DATE"), Title: t.get("TITLE"), Artist: t.get("ARTIST"),
		HasCover: t.has("METADATA_BLOCK_PICTURE"),
	}, nil
}

// Apply ergänzt fehlende Felder. Liefert (neue Daten, true) oder (nil, false), wenn nichts zu tun war.
func Apply(data []byte, e Edit) ([]byte, bool, error) {
	pages, err := parsePages(data)
	if err != nil {
		return nil, false, err
	}
	pkt, last, err := headerPages(pages)
	if err != nil {
		return nil, false, err
	}
	t, err := parseTags(pkt)
	if err != nil {
		return nil, false, err
	}

	if !addMissing(t, e) {
		return nil, false, nil
	}

	// Kommentar-Paket neu in Seiten verpacken
	newPkt := t.bytes()
	out := pages[0].bytes()
	hp := buildHeaderPages(pages[0].serial, newPkt)
	for _, h := range hp {
		out = append(out, h...)
	}
	seq := uint32(1 + len(hp))
	for _, p := range pages[last+1:] {
		p.seq = seq
		out = append(out, p.bytes()...)
		seq++
	}
	return out, true, nil
}

// addMissing ergänzt nur fehlende Felder; true, wenn etwas geändert wurde.
func addMissing(t *tags, e Edit) bool {
	changed := false
	if e.Album != "" && !t.has("ALBUM") {
		t.comments = append(t.comments, "ALBUM="+e.Album)
		changed = true
	}
	if e.Date != "" && !t.has("DATE") {
		t.comments = append(t.comments, "DATE="+e.Date)
		changed = true
	}
	if len(e.Cover) > 0 && !t.has("METADATA_BLOCK_PICTURE") {
		if c, err := pictureBlock(e.Cover); err == nil {
			t.comments = append(t.comments, c)
			changed = true
		}
	}
	return changed
}

// buildHeaderPages verpackt das Kommentar-Paket in Ogg-Seiten ab Seitennummer 1.
func buildHeaderPages(serial uint32, newPkt []byte) [][]byte {
	table := bytes.Repeat([]byte{255}, len(newPkt)/255)
	table = append(table, byte(len(newPkt)%255))
	var res [][]byte
	seq, off := uint32(1), 0
	for len(table) > 0 {
		n := len(table)
		if n > 255 {
			n = 255
		}
		chunk := table[:n]
		table = table[n:]
		size := 0
		for _, s := range chunk {
			size += int(s)
		}
		p := page{serial: serial, seq: seq, segs: chunk, payload: newPkt[off : off+size]}
		off += size
		if seq > 1 {
			p.headerType = 0x01
		}
		if len(table) == 0 {
			p.granule = 0
		} else {
			p.granule = ^uint64(0)
		}
		res = append(res, p.bytes())
		seq++
	}
	return res
}
