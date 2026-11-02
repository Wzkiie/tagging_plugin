package id3

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mkMP3(t *testing.T, args ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.mp3")
	a := append([]string{"-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=d=3"}, args...)
	a = append(a, "-c:a", "libmp3lame", p)
	if out, err := exec.Command("ffmpeg", a...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg nicht verfügbar: %v %s", err, out)
	}
	return p
}

func audioMD5(t *testing.T, p string) string {
	out, err := exec.Command("ffmpeg", "-loglevel", "error", "-i", p, "-map", "0:a", "-c", "copy", "-f", "md5", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRewrite(t *testing.T) {
	cover, err := os.ReadFile("../../cover.jpg")
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"v23":      {"-metadata", "title=T", "-metadata", "artist=A", "-id3v2_version", "3"},
		"v24":      {"-metadata", "title=T", "-metadata", "artist=A", "-id3v2_version", "4"},
		"ohne-tag": {"-write_id3v2", "0"},
	} {
		t.Run(name, func(t *testing.T) {
			p := mkMP3(t, args...)
			before := audioMD5(t, p)
			fi, err := InspectFile(p)
			if err != nil || fi.Album != "" || fi.HasCover {
				t.Fatalf("vorher: %+v %v", fi, err)
			}
			if fi.Duration < 2.8 || fi.Duration > 3.3 {
				t.Errorf("Laufzeit %v", fi.Duration)
			}
			ok, err := Rewrite(p, Edit{Album: "Äther", Date: "2019", Cover: cover})
			if err != nil || !ok {
				t.Fatalf("Rewrite: %v %v", ok, err)
			}
			fi, _ = InspectFile(p)
			if fi.Album != "Äther" || fi.Date != "2019" || !fi.HasCover {
				t.Errorf("nachher: %+v", fi)
			}
			if name != "ohne-tag" && (fi.Title != "T" || fi.Artist != "A") {
				t.Errorf("alte Tags verloren: %+v", fi)
			}
			if after := audioMD5(t, p); after != before {
				t.Errorf("Audio verändert: %s != %s", before, after)
			}
			if out, err := exec.Command("ffprobe", "-loglevel", "error", "-show_entries", "stream=codec_type:format_tags=album,date", "-of", "csv", p).CombinedOutput(); err != nil {
				t.Errorf("ffprobe: %v %s", err, out)
			} else if !strings.Contains(string(out), "Äther") {
				t.Errorf("ffprobe sieht Album nicht: %s", out)
			}
			if ok, _ := Rewrite(p, Edit{Album: "X", Date: "1", Cover: cover}); ok {
				t.Error("zweiter Lauf hat überschrieben")
			}
		})
	}
}

func TestReplaceUnknownAlbumNoDuplicates(t *testing.T) {
	cover, _ := os.ReadFile("../../cover.jpg")
	p := mkMP3(t, "-metadata", "title=T", "-metadata", "artist=A", "-metadata", "album=Unknown Album", "-id3v2_version", "3")
	// ohne ReplaceAlbum bleibt der Platzhalter, mit ReplaceAlbum wird er ersetzt - genau ein TALB
	if _, err := Rewrite(p, Edit{Album: "Echt", Date: "2020"}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := InspectFile(p); fi.Album != "Unknown Album" {
		t.Fatalf("ohne ReplaceAlbum: %q", fi.Album)
	}
	if ok, err := Rewrite(p, Edit{Album: "Echt", ReplaceAlbum: true, Cover: cover}); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	fi, _ := InspectFile(p)
	if fi.Album != "Echt" || !fi.HasCover {
		t.Errorf("%+v", fi)
	}
	f, _ := os.Open(p)
	defer f.Close()
	tg, _ := readTag(f)
	if n := len(tg.all["TALB"]); n != 1 {
		t.Errorf("TALB-Frames: %d", n)
	}
}

func TestBrokenFrameStructureIsRefused(t *testing.T) {
	src := mkMP3(t, "-write_id3v2", "0")
	audio, _ := os.ReadFile(src)
	body := append([]byte("TIT2\x00\x00\x00\x03\x00\x00\x03Hi"), []byte("XXXXXXXXXXXXXXXXXXXX")...) // danach kein gültiger Frame, kein Padding
	hdr := append([]byte("ID3\x04\x00\x00"), putSyncsafe(len(body))...)
	p := filepath.Join(t.TempDir(), "b.mp3")
	orig := append(append(hdr, body...), audio...)
	os.WriteFile(p, orig, 0o644)
	cover, _ := os.ReadFile("../../cover.jpg")
	if ok, err := Rewrite(p, Edit{Album: "X", Cover: cover}); err == nil || ok {
		t.Fatalf("erwartet Fehler, got %v %v", ok, err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(orig) {
		t.Error("Datei wurde verändert")
	}
}

func TestBackCoverIsNotFront(t *testing.T) {
	p := mkMP3(t, "-write_id3v2", "0")
	audio, _ := os.ReadFile(p)
	apic := append([]byte{0}, "image/jpeg\x00\x04\x00xx"...) // Typ 4 = Back Cover
	fr := append([]byte("APIC"), 0, 0, 0, byte(len(apic)), 0, 0)
	body := append(fr, apic...)
	hdr := append([]byte("ID3\x03\x00\x00"), putSyncsafe(len(body))...)
	os.WriteFile(p, append(append(hdr, body...), audio...), 0o644)
	if fi, _ := InspectFile(p); fi.HasCover {
		t.Error("Back Cover zählt nicht als Cover")
	}
}
