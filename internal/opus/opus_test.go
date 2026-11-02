package opus

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func mkOpus(t *testing.T, name string, secs string, meta ...string) []byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	args := []string{"-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=d=" + secs, "-c:a", "libopus"}
	for _, m := range meta {
		args = append(args, "-metadata", m)
	}
	args = append(args, p)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg nicht verfügbar: %v %s", err, out)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func cover(t *testing.T) []byte {
	b, err := os.ReadFile("../../cover.jpg")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Struktur prüfen: gültige CRC, lückenlose Seitennummern, Audiopakete unverändert.
func checkStructure(t *testing.T, orig, got []byte) {
	t.Helper()
	pg, err := parsePages(got)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pg {
		if p.seq != uint32(i) {
			t.Fatalf("Seite %d hat Nummer %d", i, p.seq)
		}
	}
	// CRC jeder Seite gegen die gespeicherte prüfen
	for off, i := 0, 0; off < len(got); i++ {
		n := 27 + len(pg[i].segs) + len(pg[i].payload)
		stored := got[off+22 : off+26]
		cp := append([]byte(nil), got[off:off+n]...)
		copy(cp[22:26], []byte{0, 0, 0, 0})
		calc := oggCRC(cp)
		if uint32(stored[0])|uint32(stored[1])<<8|uint32(stored[2])<<16|uint32(stored[3])<<24 != calc {
			t.Fatalf("CRC falsch auf Seite %d", i)
		}
		off += n
	}
	// Audiodaten: alle Seiten nach dem Kommentar-Header müssen byte-gleich im Payload sein
	op, _ := parsePages(orig)
	_, lastO, _ := headerPages(op)
	_, lastN, _ := headerPages(pg)
	var a, b []byte
	for _, p := range op[lastO+1:] {
		a = append(a, p.payload...)
	}
	for _, p := range pg[lastN+1:] {
		b = append(b, p.payload...)
	}
	if !bytes.Equal(a, b) || len(op[lastO+1:]) != len(pg[lastN+1:]) {
		t.Fatal("Audiodaten wurden verändert")
	}
}

func TestAddAlbumDateCover(t *testing.T) {
	orig := mkOpus(t, "a.opus", "20", "title=Song", "artist=Band")
	got, changed, err := Apply(orig, Edit{Album: "Mein Album", Date: "2024", Cover: cover(t)})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	checkStructure(t, orig, got)
	info, err := Inspect(got)
	if err != nil {
		t.Fatal(err)
	}
	if info.Album != "Mein Album" || info.Date != "2024" || !info.HasCover || info.Title != "Song" || info.Artist != "Band" {
		t.Fatalf("%+v", info)
	}
}

func TestIdempotentAndNoOverwrite(t *testing.T) {
	orig := mkOpus(t, "a.opus", "5", "album=Original", "date=1999")
	if _, changed, _ := Apply(orig, Edit{Album: "Neu", Date: "2024"}); changed {
		t.Fatal("vorhandene Tags dürfen nicht überschrieben werden")
	}
	got, changed, _ := Apply(orig, Edit{Album: "Neu", Cover: cover(t)})
	if !changed {
		t.Fatal("Cover fehlte, hätte ergänzt werden müssen")
	}
	if info, _ := Inspect(got); info.Album != "Original" || !info.HasCover {
		t.Fatalf("%+v", info)
	}
	if _, changed, _ := Apply(got, Edit{Album: "Neu", Cover: cover(t)}); changed {
		t.Fatal("zweiter Lauf darf nichts mehr ändern")
	}
}

func TestMultiPageHeaderRoundTrip(t *testing.T) {
	// Großes Cover (mehrere Seiten im Kommentar-Header), danach nochmals Album ergänzen
	big := bytes.Repeat([]byte{0}, 0)
	_ = big
	orig := mkOpus(t, "a.opus", "10", "title=Song")
	first, _, err := Apply(orig, Edit{Cover: padJPEG(cover(t), 200_000)})
	if err != nil {
		t.Fatal(err)
	}
	pg, _ := parsePages(first)
	if _, last, _ := headerPages(pg); last < 3 {
		t.Fatalf("Test braucht mehrseitigen Header, hat %d", last)
	}
	checkStructure(t, orig, first)
	second, changed, err := Apply(first, Edit{Album: "Zusatz"})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	checkStructure(t, orig, second)
	if info, _ := Inspect(second); info.Album != "Zusatz" || !info.HasCover {
		t.Fatalf("%+v", info)
	}
}

// JPEG bleibt gültig, wenn man nach dem EOI-Marker Füllbytes anhängt (genug für Größentests).
func padJPEG(j []byte, n int) []byte {
	return append(append([]byte(nil), j...), bytes.Repeat([]byte{0}, n)...)
}

func TestRejectsGarbage(t *testing.T) {
	if _, _, err := Apply([]byte("kein ogg"), Edit{Album: "x"}); err == nil {
		t.Fatal("Fehler erwartet")
	}
	// Vorbis-Ogg (kein Opus) muss abgelehnt werden
	p := filepath.Join(t.TempDir(), "v.ogg")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=d=2", "-c:a", "libvorbis", p).CombinedOutput(); err != nil {
		t.Skipf("kein libvorbis: %s", out)
	}
	b, _ := os.ReadFile(p)
	if _, _, err := Apply(b, Edit{Album: "x"}); err == nil {
		t.Fatal("Vorbis darf nicht angefasst werden")
	}
}

func TestRewriteMatchesApply(t *testing.T) {
	for _, secs := range []string{"3", "30"} {
		orig := mkOpus(t, "a.opus", secs, "title=Song")
		want, _, err := Apply(orig, Edit{Album: "A", Date: "2024", Cover: padJPEG(cover(t), 150_000)})
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "x.opus")
		os.WriteFile(p, orig, 0o644)
		ch, err := Rewrite(p, Edit{Album: "A", Date: "2024", Cover: padJPEG(cover(t), 150_000)})
		if err != nil || !ch {
			t.Fatalf("ch=%v err=%v", ch, err)
		}
		got, _ := os.ReadFile(p)
		if !bytes.Equal(got, want) {
			t.Fatal("Rewrite weicht von Apply ab")
		}
		checkStructure(t, orig, got)
		if ch, _ := Rewrite(p, Edit{Album: "A", Date: "2024"}); ch {
			t.Fatal("zweiter Lauf darf nichts ändern")
		}
	}
}
