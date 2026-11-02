//go:build !wasip1

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"albumfix/internal/id3"
	"albumfix/internal/opus"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/stretchr/testify/mock"
)

// Integrationstest mit gemocktem Host: echte ffmpeg-Dateien, gefälschte Deezer-Antworten.
func TestRun(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg fehlt")
	}
	lib := t.TempDir()
	os.MkdirAll(filepath.Join(lib, "explo"), 0o755)
	gen := func(name, codec string, meta ...string) string {
		p := filepath.Join(lib, "explo", name)
		args := []string{"-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=d=30", "-c:a", codec}
		for _, m := range meta {
			args = append(args, "-metadata", m)
		}
		if out, err := exec.Command("ffmpeg", append(args, p)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		past := time.Now().Add(-time.Hour)
		os.Chtimes(p, past, past)
		return p
	}
	good := gen("a.opus", "libopus", "title=Song", "artist=Band")
	mp3 := gen("b.mp3", "libmp3lame", "title=Song", "artist=Band")
	bad := gen("c.opus", "libopus", "title=Nope", "artist=Nobody")
	have := gen("d.opus", "libopus", "title=Song", "artist=Band", "album=Schon da")
	_ = have

	cover, _ := os.ReadFile("cover.jpg")
	search := map[string]any{"data": []map[string]any{
		{"id": 1, "title": "Song", "title_short": "Song", "duration": 30, "artist": map[string]any{"name": "Band"},
			"album": map[string]any{"id": 11, "title": "Single X", "cover_xl": "https://cdn.dzcdn.net/11.jpg"}},
		{"id": 2, "title": "Song", "title_short": "Song", "duration": 30, "artist": map[string]any{"name": "Band"},
			"album": map[string]any{"id": 12, "title": "Das Album", "cover_xl": "https://cdn.dzcdn.net/12.jpg"}},
	}}
	empty := map[string]any{"data": []any{}}
	js := func(v any) []byte { b, _ := json.Marshal(v); return b }
	host.HTTPMock.On("Send", mock.MatchedBy(func(r host.HTTPRequest) bool {
		return strings.Contains(r.URL, "/search") && strings.Contains(r.URL, "Band")
	})).
		Return(&host.HTTPResponse{StatusCode: 200, Body: js(search)}, nil)
	host.HTTPMock.On("Send", mock.MatchedBy(func(r host.HTTPRequest) bool {
		return strings.Contains(r.URL, "/search") && (strings.Contains(r.URL, "Nobody") || strings.Contains(r.URL, "Nope"))
	})).
		Return(&host.HTTPResponse{StatusCode: 200, Body: js(empty)}, nil)
	host.HTTPMock.On("Send", mock.MatchedBy(func(r host.HTTPRequest) bool { return strings.HasSuffix(r.URL, "/album/11") })).
		Return(&host.HTTPResponse{StatusCode: 200, Body: js(map[string]any{"title": "Single X", "record_type": "single", "release_date": "2018-05-01", "cover_xl": "https://cdn.dzcdn.net/11.jpg"})}, nil)
	host.HTTPMock.On("Send", mock.MatchedBy(func(r host.HTTPRequest) bool { return strings.HasSuffix(r.URL, "/album/12") })).
		Return(&host.HTTPResponse{StatusCode: 200, Body: js(map[string]any{"title": "Das Album", "record_type": "album", "release_date": "2019-03-08", "cover_xl": "https://cdn.dzcdn.net/12.jpg"})}, nil)
	host.HTTPMock.On("Send", mock.MatchedBy(func(r host.HTTPRequest) bool { return strings.HasSuffix(r.URL, ".jpg") })).
		Return(&host.HTTPResponse{StatusCode: 200, Body: cover}, nil)

	host.LibraryMock.On("GetAllLibraries").Return([]host.Library{{ID: 1, Name: "Musik", MountPoint: lib}}, nil)
	host.KVStoreMock.On("Get", mock.Anything).Return([]byte(nil), false, nil)
	host.KVStoreMock.On("Set", mock.Anything, mock.Anything).Return(nil)
	host.SchedulerMock.On("CancelSchedule", mock.Anything).Return(nil)
	host.SchedulerMock.On("ScheduleOneTime", mock.Anything, mock.Anything, mock.Anything).Return("id", nil)
	host.KVStoreMock.On("SetWithTTL", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	host.ConfigMock.On("Get", "dry_run").Return("false", true)
	host.ConfigMock.On("Get", "path_filter").Return("explo/", true)
	host.ConfigMock.On("Get", mock.Anything).Return("", false)
	pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Run(func(a mock.Arguments) { t.Log(a.String(1)) })

	if err := (&albumfix{}).OnCallback(scheduler.SchedulerCallbackRequest{Payload: "cron"}); err != nil {
		t.Fatal(err)
	}

	oi, err := opus.InspectFile(good)
	if err != nil || oi.Album != "Das Album" || oi.Date != "2018" || !oi.HasCover {
		t.Errorf("Opus: %+v %v", oi, err)
	}
	mi, err := id3.InspectFile(mp3)
	if err != nil || mi.Album != "Das Album" || mi.Date != "2018" || !mi.HasCover {
		t.Errorf("MP3: %+v %v", mi, err)
	}
	if bi, _ := opus.InspectFile(bad); bi.Album != "" || bi.HasCover {
		t.Errorf("abgelehnte Datei wurde verändert: %+v", bi)
	}
}
