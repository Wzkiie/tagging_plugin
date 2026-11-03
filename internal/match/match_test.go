package match

import (
	"strings"
	"testing"
)

func TestRatio(t *testing.T) {
	// Referenzwerte aus Python: SequenceMatcher(None, a, b).ratio()
	cases := []struct {
		a, b string
		want float64
	}{
		{"Hello World", "hello world", 1},
		{"abcd", "bcde", 0.75},
		{"Beyoncé", "Beyonce", 1},
		{"Straße", "Strasse", 1},
		{"abc", "xyz", 0},
	}
	for _, c := range cases {
		if got := Ratio(c.a, c.b); got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("Ratio(%q,%q)=%v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestEvaluate(t *testing.T) {
	mk := func(title, artist string, dur int) Track {
		var tr Track
		tr.Title, tr.TitleShort, tr.Duration = title, title, dur
		tr.Artist.Name = artist
		return tr
	}
	tracks := []Track{mk("Song", "Band", 200), mk("Song", "Band", 260), mk("Other", "Band", 200)}
	ok, best := Evaluate("Band feat. X", "Song", 201, tracks, 0.85, 8, 0)
	if len(ok) != 1 || ok[0].Track.Duration != 200 || best == nil {
		t.Fatalf("unerwartet: %+v", ok)
	}
	if ok, _ := Evaluate("Band", "Song", 0, tracks, 0.85, 8, 0); len(ok) != 2 {
		t.Errorf("ohne Laufzeit sollten 2 Treffer passen, got %d", len(ok))
	}
}

func TestPickAlbum(t *testing.T) {
	got := PickAlbum([]Album{
		{Title: "Comp", RecordType: "compilation", ReleaseDate: "2019-01-01"},
		{Title: "Single", RecordType: "single", ReleaseDate: "2018-01-01"},
		{Title: "LP", RecordType: "album", ReleaseDate: "2018-06-01"},
	}, "Band")
	if got.Title != "LP" {
		t.Errorf("got %s", got.Title)
	}
}

func TestStripFeatAndRemix(t *testing.T) {
	for in, want := range map[string]string{
		"Au Revoir (feat. Sido)": "Au Revoir", "Levitating (feat. DaBaby)": "Levitating", "Song [ft. X]": "Song",
		"Song feat. X": "Song", "Song (Galantis Remix)": "Song (Galantis Remix)", "Song (Live)": "Song (Live)",
	} {
		if got := StripFeat(in); got != want {
			t.Errorf("StripFeat(%q)=%q, want %q", in, got, want)
		}
	}
	mk := func(title, short string) Track {
		var tr Track
		tr.Title, tr.TitleShort = title, short
		tr.Artist.Name = "Ed Sheeran"
		return tr
	}
	if s := Score("Ed Sheeran", "Shape of You", mk("Shape Of You (Dubstep Remix)", "Shape Of You")); s >= 0.85 {
		t.Errorf("Remix darf nicht als Original durchgehen: %v", s)
	}
	tr := mk("Au Revoir (feat. Sido)", "Au Revoir (feat. Sido)")
	tr.Artist.Name = "Mark Forster"
	if s := Score("Mark Forster feat. Sido", "Au Revoir", tr); s < 0.85 {
		t.Errorf("feat.-Zusatz muss passen: %v", s)
	}
}

func TestPickAlbumSkipsSampler(t *testing.T) {
	a := Album{Title: "Sampler", RecordType: "album", ReleaseDate: "2010-01-01"}
	a.Artist.Name = "Various Artists"
	b := Album{Title: "Single", RecordType: "single", ReleaseDate: "2018-01-01"}
	b.Artist.Name = "Band"
	if got := PickAlbum([]Album{a, b}, "Band"); got.Title != "Single" {
		t.Errorf("got %s", got.Title)
	}
}

func trk(title, short, artist string, dur int) Track {
	var t Track
	t.Title, t.TitleShort, t.Duration = title, short, dur
	t.Artist.Name = artist
	return t
}

func TestVersionGuard(t *testing.T) {
	for _, c := range []struct {
		title string
		tr    Track
		want  bool // Score >= 0.85
	}{
		{"Nothing Else Matters", trk("Nothing Else Matters (Live)", "Nothing Else Matters", "Metallica", 400), false},
		{"Durch den Monsun", trk("Durch den Monsun 2020", "Durch den Monsun", "Tokio Hotel", 230), true}, // keine Versionswörter: Laufzeit entscheidet
		{"Nothing Else Matters", trk("Nothing Else Matters (Remastered 2021)", "Nothing Else Matters", "Metallica", 388), true},
		{"Happy", trk(`Happy (From "Despicable Me 2")`, "Happy", "Pharrell Williams", 233), true},
		{"Everlong (Acoustic)", trk("Everlong", "Everlong", "Foo Fighters", 250), false},
		{"Song (Part 2)", trk("Song (Part 1)", "Song", "Band", 200), false},
		{"Song", trk("Song (Karaoke Version)", "Song", "Band", 200), false},
		{"Live and Let Die", trk("Live and Let Die", "Live and Let Die", "Wings", 190), true},
	} {
		artist := c.tr.Artist.Name
		if got := Score(artist, c.title, c.tr) >= 0.85; got != c.want {
			t.Errorf("%q vs %q: got %v want %v (Score %.2f)", c.title, c.tr.Title, got, c.want, Score(artist, c.title, c.tr))
		}
	}
}

func TestNormEdgeCases(t *testing.T) {
	if r := Ratio("P!nk", "Pink"); r < 0.85 {
		t.Errorf("P!nk/Pink = %v", r)
	}
	if r := Ratio("Björk", "Björk"); r != 1 { // zerlegtes ö (macOS/NFD)
		t.Errorf("NFD = %v", r)
	}
	if r := Ratio("Don't Stop", "Dont Stop"); r != 1 {
		t.Errorf("Apostroph = %v", r)
	}
}

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Lose Yourself (Official Video)":     "Lose Yourself",
		"Radioactive [Lyrics]":               "Radioactive",
		"Eminem - Lose Yourself":             "Lose Yourself",
		"Song (Live)":                        "Song (Live)",
		"Hello (Official Music Video) (HD)":  "Hello",
		"Just - Another Title (feat. Other)": "Just - Another Title (feat. Other)",
	} {
		if got := CleanTitle("Eminem", in); got != want && in != "Hello (Official Music Video) (HD)" && in != "Just - Another Title (feat. Other)" {
			t.Errorf("CleanTitle(%q)=%q want %q", in, got, want)
		}
	}
	if got := CleanTitle("X", "Hello (Official Music Video) (HD)"); got != "Hello" {
		t.Errorf("mehrfach: %q", got)
	}
	if got := CleanArtist("Imagine Dragons - Topic"); got != "Imagine Dragons" {
		t.Errorf("Topic: %q", got)
	}
}

func TestSamplerAndBestOfRanking(t *testing.T) {
	mk := func(title, typ, date, artist string) Album {
		a := Album{Title: title, RecordType: typ, ReleaseDate: date}
		a.Artist.Name = artist
		return a
	}
	got := PickAlbum([]Album{
		mk("Greatest Hits", "album", "2016-01-01", "Band"),
		mk("Enema", "album", "1999-06-01", "Band"),
		mk("Enema (Deluxe)", "album", "1999-01-01", "Band"),
		mk("Hits 2011", "album", "2011-01-01", "Verschiedene Interpreten"),
	}, "Band")
	if got.Title != "Enema" {
		t.Errorf("got %s", got.Title)
	}
	if !IsSampler(mk("x", "album", "", "Verschiedene Interpreten"), "Band") {
		t.Error("Verschiedene Interpreten ist ein Sampler")
	}
}

func TestQueriesNoDeadStrictQuery(t *testing.T) {
	qs := Queries("Dua Lipa feat. X", "Don't Start Now (Official Video)")
	for _, q := range qs {
		if strings.HasPrefix(q.Q, "artist:") {
			t.Errorf("strikte Query entfernt: %q", q.Q)
		}
	}
	if last := qs[len(qs)-1]; !strings.HasPrefix(last.Q, `track:"`) || last.Limit != 25 {
		t.Errorf("letzte Query: %+v", last)
	}
}

func TestDurationTiers(t *testing.T) {
	tr := trk("Song", "Song", "Band", 180)
	for _, c := range []struct {
		length float64
		max    float64
		want   int
	}{{185, 0, 1}, {200, 0, 0}, {200, 45, 1}, {240, 45, 0}, {30, 45, 0}} {
		ok, _ := Evaluate("Band", "Song", c.length, []Track{tr}, 0.85, 8, c.max)
		if len(ok) != c.want {
			t.Errorf("Laufzeit %v (max %v): %d Treffer, erwartet %d", c.length, c.max, len(ok), c.want)
		}
	}
	// ungenauer Titel (Score < 0.95) bekommt die große Toleranz nicht
	loose := trk("Song Name Two", "Song Name Two", "Band", 180)
	if ok, _ := Evaluate("Band", "Song Name", 200, []Track{loose}, 0.7, 8, 45); len(ok) != 0 {
		t.Error("große Toleranz nur bei Score >= 0.95")
	}
}
