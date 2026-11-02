package opus

import (
	"albumfix/internal/fsutil"
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// readRawPage liest genau eine Ogg-Seite (Header + Segmenttabelle + Payload) als Rohbytes.
// maxHeader begrenzt das Kommentar-Paket (ein korrupter Header ohne Ende würde sonst die ganze Datei einlesen).
const maxHeader = 32 << 20

func readRawPage(r *bufio.Reader) ([]byte, error) {
	var hdr [27]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, errors.New("abgeschnittene Ogg-Seite")
	}
	if string(hdr[:4]) != "OggS" || hdr[4] != 0 {
		return nil, errors.New("kein gültiger Ogg-Strom")
	}
	nseg := int(hdr[26])
	segs := make([]byte, nseg)
	if _, err := io.ReadFull(r, segs); err != nil {
		return nil, errors.New("abgeschnittene Ogg-Seite")
	}
	size := 0
	for _, s := range segs {
		size += int(s)
	}
	raw := make([]byte, 27+nseg+size)
	copy(raw, hdr[:])
	copy(raw[27:], segs)
	if _, err := io.ReadFull(r, raw[27+nseg:]); err != nil {
		return nil, errors.New("abgeschnittene Ogg-Seite")
	}
	return raw, nil
}

func pageOf(raw []byte) page {
	nseg := int(raw[26])
	return page{
		headerType: raw[5],
		granule:    binary.LittleEndian.Uint64(raw[6:]),
		serial:     binary.LittleEndian.Uint32(raw[14:]),
		seq:        binary.LittleEndian.Uint32(raw[18:]),
		segs:       raw[27 : 27+nseg],
		payload:    raw[27+nseg:],
	}
}

// Rewrite ergänzt fehlende Felder direkt in der Datei (Streaming, konstanter Speicher).
// Es wird nur der Kopfbereich im Speicher gehalten; Audioseiten werden seitenweise durchgereicht.
// Geschrieben wird in path+".albumfix-tmp" und danach atomar umbenannt.
func Rewrite(path string, e Edit) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)

	raw0, err := readRawPage(br)
	if err != nil {
		return false, err
	}
	p0 := pageOf(raw0)
	if p0.headerType&0x02 == 0 || !bytes.HasPrefix(p0.payload, []byte("OpusHead")) {
		return false, errors.New("keine Opus-Datei")
	}

	// Kommentar-Paket einsammeln
	var packet []byte
	var seqHdr uint32 // Anzahl Seiten des Kommentar-Headers
	done := false
	for !done {
		raw, err := readRawPage(br)
		if err != nil {
			return false, errLayout
		}
		p := pageOf(raw)
		packet = append(packet, p.payload...)
		if len(packet) > maxHeader {
			return false, errLayout
		}
		seqHdr++
		for j, s := range p.segs {
			if s < 255 {
				if j != len(p.segs)-1 {
					return false, errLayout
				}
				done = true
				break
			}
		}
	}
	t, err := parseTags(packet)
	if err != nil {
		return false, err
	}
	if !addMissing(t, e) {
		return false, nil
	}
	newPkt := t.bytes()
	hdrPages := buildHeaderPages(p0.serial, newPkt)

	if err := fsutil.CheckWritable(path); err != nil {
		return false, err
	}
	tmp := path + fsutil.TmpSuffix
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return false, err
	}
	fail := func(err error) (bool, error) {
		out.Close()
		os.Remove(tmp)
		return false, err
	}
	bw := bufio.NewWriterSize(out, 64<<10)
	if _, err := bw.Write(raw0); err != nil {
		return fail(err)
	}
	for _, p := range hdrPages {
		if _, err := bw.Write(p); err != nil {
			return fail(err)
		}
	}
	// Seitennummern nur im ersten Strom anpassen (bei verketteten Ogg-Dateien bleiben die übrigen Ströme unberührt)
	seq := uint32(1 + len(hdrPages))
	for {
		raw, err := readRawPage(br)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(err)
		}
		if binary.LittleEndian.Uint32(raw[14:]) == p0.serial {
			if binary.LittleEndian.Uint32(raw[18:]) != seq {
				// die alte Prüfsumme vorher prüfen, sonst würde der Neuberechnen einen Bitfehler "heilen"
				stored := binary.LittleEndian.Uint32(raw[22:])
				copy(raw[22:26], []byte{0, 0, 0, 0})
				if oggCRC(raw) != stored {
					return fail(fmt.Errorf("CRC-Fehler in Audioseite %d: Datei ist beschädigt, bleibt unverändert", seq))
				}
				binary.LittleEndian.PutUint32(raw[18:], seq)
				binary.LittleEndian.PutUint32(raw[22:], 0)
				binary.LittleEndian.PutUint32(raw[22:], oggCRC(raw))
			}
			seq++
		}
		if _, err := bw.Write(raw); err != nil {
			return fail(err)
		}
	}
	if err := bw.Flush(); err != nil {
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
