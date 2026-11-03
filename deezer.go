package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"albumfix/internal/match"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

const deezerAPI = "https://api.deezer.com"

var (
	errQuota  = errors.New("Deezer-Rate-Limit erreicht")
	errBudget = errors.New("Zeitbudget für diesen Aufruf aufgebraucht")
)

// hardDeadline: nach diesem Zeitpunkt werden keine Anfragen mehr gestartet (Navidrome bricht bei 30 s ab).
var hardDeadline time.Time

func httpGet(rawURL string, timeoutMs int32) ([]byte, error) {
	remaining := hardDeadline.Sub(time.Now().Round(0))
	if remaining < 2*time.Second {
		return nil, errBudget
	}
	if ms := remaining.Milliseconds() - 1000; int64(timeoutMs) > ms { // nie über das 30-s-Limit von Navidrome hinaus warten
		timeoutMs = int32(ms)
	}
	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:    "GET",
		URL:       rawURL,
		Headers:   map[string]string{"User-Agent": "navidrome-albumfix/0.2"},
		TimeoutMs: timeoutMs,
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 429 {
		return nil, errQuota
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func deezerGet(path string, q url.Values, out any) error {
	u := deezerAPI + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, err := httpGet(u, 8000)
	if err != nil {
		return err
	}
	var e struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != nil {
		if e.Error.Code == 4 || e.Error.Code == 100 {
			return errQuota
		}
		return fmt.Errorf("Deezer-Fehler %d", e.Error.Code)
	}
	return json.Unmarshal(body, out)
}

// deezerClient cached Albumdaten und das zuletzt geladene Cover innerhalb eines Aufrufs.
type deezerClient struct {
	albums     map[int64]match.Album
	coverID    int64
	coverBytes []byte
}

func newDeezer() *deezerClient { return &deezerClient{albums: map[int64]match.Album{}} }

func (d *deezerClient) album(id int64) (match.Album, error) {
	if a, ok := d.albums[id]; ok {
		return a, nil
	}
	var a match.Album
	if err := deezerGet(fmt.Sprintf("/album/%d", id), nil, &a); err != nil {
		return a, err
	}
	a.ID = id
	d.albums[id] = a
	return a, nil
}

// lookup sucht ein Album für Artist/Titel. Liefert (Album, Score, Grund); Album == nil heißt abgelehnt.
func (d *deezerClient) lookup(artist, title string, length float64, s settings) (*match.Album, float64, float64, string, error) {
	var ok []match.Candidate
	var best *match.Candidate
	for _, q := range match.Queries(artist, title) {
		var res struct {
			Data []match.Track `json:"data"`
		}
		if err := deezerGet("/search", url.Values{"q": {q.Q}, "limit": {strconv.Itoa(q.Limit)}}, &res); err != nil {
			return nil, 0, 0, "", err
		}
		var b *match.Candidate
		ok, b = match.Evaluate(artist, title, length, res.Data, s.MinScore, s.DurTol, s.DurTolMax)
		if best == nil || (b != nil && b.Score > best.Score) {
			best = b
		}
		if len(ok) > 0 {
			break
		}
	}
	if len(ok) == 0 {
		switch {
		case best == nil:
			return nil, 0, 0, "kein Treffer", nil
		case best.Score < s.MinScore:
			return nil, best.Score, 0, "Titel/Artist zu unähnlich", nil
		}
		return nil, best.Score, best.DurDiff, fmt.Sprintf("Laufzeit weicht %.0fs ab", best.DurDiff), nil
	}

	// nur Treffer mit exakt passendem Artist für die Albumwahl ("Johnny Cash", nicht "Johnny Crash")
	var exact []match.Candidate
	for _, c := range ok {
		if match.ExactArtist(artist, c.Track) {
			exact = append(exact, c)
		}
	}
	if len(exact) > 0 {
		ok = exact
	}

	// gleiche Aufnahme auf mehreren Alben: Albumtyp usw. entscheidet (siehe match.PickAlbum)
	var albums []match.Album
	seen := map[int64]bool{}
	for _, c := range ok {
		if c.Score < ok[0].Score-0.05 || seen[c.Track.Album.ID] || len(albums) == 3 {
			continue
		}
		seen[c.Track.Album.ID] = true
		a, err := d.album(c.Track.Album.ID)
		if err != nil {
			if errors.Is(err, errQuota) || errors.Is(err, errBudget) {
				return nil, 0, 0, "", err
			}
			a = c.Track.Album
		}
		if a.Title == "" {
			a.Title = c.Track.Album.Title
		}
		if a.CoverXL == "" {
			a.CoverXL, a.CoverBig = c.Track.Album.CoverXL, c.Track.Album.CoverBig
		}
		albums = append(albums, a)
	}
	pick := match.PickAlbum(albums, artist)
	if match.IsSampler(pick, artist) {
		return nil, ok[0].Score, ok[0].DurDiff, "nur Sampler/fremdes Album gefunden", nil
	}
	// Jahr: frühestes Datum unter den Alben des Künstlers (Neuauflagen tragen oft ein späteres Datum)
	for _, a := range albums {
		if !match.IsSampler(a, artist) && a.ReleaseDate != "" && (pick.ReleaseDate == "" || a.ReleaseDate < pick.ReleaseDate) {
			pick.ReleaseDate = a.ReleaseDate
		}
	}
	return &pick, ok[0].Score, ok[0].DurDiff, "ok", nil
}

// cover lädt das Cover eines Albums (XL, sonst Big).
func (d *deezerClient) cover(a match.Album) ([]byte, error) {
	if d.coverID == a.ID && d.coverBytes != nil {
		return d.coverBytes, nil
	}
	var lastErr error
	for _, u := range []string{a.CoverXL, a.CoverBig} {
		if u == "" {
			continue
		}
		b, err := httpGet(u, 15000)
		if err != nil {
			lastErr = err
			if errors.Is(err, errBudget) {
				break
			}
			continue
		}
		if len(b) < 100 {
			lastErr = errors.New("Cover zu klein")
			continue
		}
		d.coverID, d.coverBytes = a.ID, b
		return b, nil
	}
	if lastErr == nil {
		lastErr = errors.New("kein Cover-Link")
	}
	return nil, lastErr
}
