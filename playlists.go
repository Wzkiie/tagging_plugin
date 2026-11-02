package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"albumfix/internal/match"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// subsonic ruft die Subsonic-API von Navidrome als der konfigurierten Benutzer auf.
func subsonic(user, endpoint string, q url.Values) ([]byte, error) {
	q.Set("u", user)
	q.Set("f", "json")
	raw, err := host.SubsonicAPICall(endpoint + "?" + q.Encode())
	if err != nil {
		return nil, err
	}
	var env struct {
		R struct {
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"subsonic-response"`
	}
	if json.Unmarshal([]byte(raw), &env) == nil && env.R.Status == "failed" && env.R.Error != nil {
		return nil, fmt.Errorf("%s: %s", endpoint, env.R.Error.Message)
	}
	return []byte(raw), nil
}

// plEntry ist ein Titel einer Wiedergabeliste. Der Subsonic-Pfad taugt nicht zum Abgleich: Navidrome baut ihn aus
// Artist/Album/Titel und er entspricht nicht dem Dateipfad. Verglichen wird deshalb über Titel + Artist.
type plEntry struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
}

// playlistTracks liefert die Titel aller Wiedergabelisten mit einem der Namen.
func playlistTracks(user string, names []string) ([]plEntry, []string, error) {
	b, err := subsonic(user, "getPlaylists", url.Values{})
	if err != nil {
		return nil, nil, err
	}
	var pl struct {
		R struct {
			Playlists struct {
				Playlist []struct{ ID, Name string } `json:"playlist"`
			} `json:"playlists"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal(b, &pl); err != nil {
		return nil, nil, err
	}
	want := map[string]bool{}
	for _, n := range names {
		want[strings.ToLower(strings.TrimSpace(n))] = true
	}
	var tracks []plEntry
	var found []string
	for _, p := range pl.R.Playlists.Playlist {
		if !want[strings.ToLower(strings.TrimSpace(p.Name))] {
			continue
		}
		b, err := subsonic(user, "getPlaylist", url.Values{"id": {p.ID}})
		if err != nil {
			return nil, nil, err
		}
		var one struct {
			R struct {
				Playlist struct {
					Entry []plEntry `json:"entry"`
				} `json:"playlist"`
			} `json:"subsonic-response"`
		}
		if err := json.Unmarshal(b, &one); err != nil {
			return nil, nil, err
		}
		tracks = append(tracks, one.R.Playlist.Entry...)
		found = append(found, fmt.Sprintf("%s (%d)", p.Name, len(one.R.Playlist.Entry)))
	}
	return tracks, found, nil
}

// inPlaylists: gleicher Titel und ähnlicher Artist wie ein Eintrag der Wiedergabelisten.
func inPlaylists(tracks []plEntry, title, artist string) bool {
	for _, t := range tracks {
		if match.Ratio(t.Title, title) < 0.95 {
			continue
		}
		for _, a := range append(match.SplitArtists(artist), artist) {
			if match.Ratio(t.Artist, a) >= 0.8 {
				return true
			}
		}
		for _, a := range match.SplitArtists(t.Artist) {
			if match.Ratio(a, artist) >= 0.8 {
				return true
			}
		}
	}
	return false
}
