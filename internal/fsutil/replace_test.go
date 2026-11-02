package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T, old, new string) (orig, tmp string) {
	d := t.TempDir()
	orig = filepath.Join(d, "a")
	tmp = orig + TmpSuffix
	os.WriteFile(orig, []byte(old), 0o664)
	os.Chmod(orig, 0o664)
	os.WriteFile(tmp, []byte(new), 0o600)
	return
}

func TestReplaceBoth(t *testing.T) {
	for name, fn := range map[string]func(string, string) error{"rename": renameReplace, "copy": copyReplace} {
		t.Run(name, func(t *testing.T) {
			orig, tmp := setup(t, "alt-alt-alt", "neu")
			if err := fn(tmp, orig); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(orig); string(b) != "neu" {
				t.Errorf("Inhalt %q", b)
			}
			for _, s := range []string{TmpSuffix, ReadySuffix} {
				if _, err := os.Stat(orig + s); err == nil {
					t.Errorf("%s übrig", s)
				}
			}
		})
	}
	orig, tmp := setup(t, "x", "yy")
	copyReplace(tmp, orig)
	if st, _ := os.Stat(orig); st.Mode().Perm() != 0o664 {
		t.Errorf("copy: Rechte %v", st.Mode().Perm())
	}
}

func TestRecoverFinishesReady(t *testing.T) {
	// simuliert: Absturz nach dem Umbenennen in .ready, Original halb überschrieben/abgeschnitten
	orig, tmp := setup(t, "ABCDEFGHIJ", "neue-datei-inhalt")
	os.Rename(tmp, orig+ReadySuffix)
	os.WriteFile(orig, []byte("neue-"), 0o664)
	ok, err := Recover(orig)
	if err != nil || !ok {
		t.Fatalf("Recover: %v %v", ok, err)
	}
	if b, _ := os.ReadFile(orig); string(b) != "neue-datei-inhalt" {
		t.Errorf("Inhalt %q", b)
	}
	if _, err := os.Stat(orig + ReadySuffix); err == nil {
		t.Error(".ready übrig")
	}
}

func TestRecoverDiscardsIncompleteTmp(t *testing.T) {
	orig, tmp := setup(t, "original", "halb")
	if ok, err := Recover(orig); ok || err != nil {
		t.Fatalf("Recover: %v %v", ok, err)
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Error("tmp nicht entfernt")
	}
	if b, _ := os.ReadFile(orig); string(b) != "original" {
		t.Errorf("Original verändert: %q", b)
	}
}
