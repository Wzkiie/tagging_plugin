// Package match bewertet Deezer-Suchtreffer gegen die Tags einer Datei (Port von deezer_tagcheck.py).
package match

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Track ist ein Deezer-Suchtreffer (Auszug).
type Track struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	TitleShort string `json:"title_short"`
	Duration   int    `json:"duration"`
	Artist     struct {
		Name string `json:"name"`
	} `json:"artist"`
	Album Album `json:"album"`
}

// Album ist das Album eines Treffers bzw. das Ergebnis von /album/{id}.
type Album struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	CoverXL     string `json:"cover_xl"`
	CoverBig    string `json:"cover_big"`
	RecordType  string `json:"record_type"`
	ReleaseDate string `json:"release_date"`
	Artist      struct {
		Name string `json:"name"`
	} `json:"artist"`
}

// Candidate ist ein Treffer, der Titel, Artist und Laufzeit besteht.
type Candidate struct {
	Track   Track
	Score   float64
	DurDiff float64
}

var fold = strings.NewReplacer(
	"ä", "a", "ö", "o", "ü", "u", "ß", "ss", "æ", "ae", "œ", "oe", "ø", "o", "đ", "d", "ł", "l",
	"á", "a", "à", "a", "â", "a", "ã", "a", "å", "a", "ā", "a", "ă", "a", "ą", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e", "ē", "e", "ę", "e", "ě", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i", "ī", "i", "ı", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ō", "o", "ő", "o",
	"ú", "u", "ù", "u", "û", "u", "ū", "u", "ů", "u", "ű", "u",
	"ý", "y", "ÿ", "y", "ç", "c", "ć", "c", "č", "c", "ñ", "n", "ń", "n", "ň", "n",
	"š", "s", "ś", "s", "ş", "s", "ž", "z", "ź", "z", "ż", "z", "ř", "r", "ť", "t", "ď", "d",
)

// Norm entspricht norm() aus dem Python-Tagger: klein, ohne Akzente/Satzzeichen, Leerraum normalisiert.
func Norm(s string) string {
	s = fold.Replace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r), r == '\'', r == '’', r == '!', r == '*': // weglassen statt Leerzeichen: "P!nk" = "Pnk"
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Ratio entspricht difflib.SequenceMatcher(None, norm(a), norm(b)).ratio().
func Ratio(a, b string) float64 {
	x, y := []rune(Norm(a)), []rune(Norm(b))
	if len(x)+len(y) == 0 {
		return 1
	}
	return 2 * float64(matching(x, y)) / float64(len(x)+len(y))
}

func matching(a, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// längster gemeinsamer Block, bei Gleichstand der früheste in a, dann in b (wie difflib)
	bi, bj, bk := 0, 0, 0
	prev := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
				if cur[j] > bk {
					bi, bj, bk = i-cur[j], j-cur[j], cur[j]
				}
			}
		}
		prev = cur
	}
	if bk == 0 {
		return 0
	}
	return bk + matching(a[:bi], b[:bj]) + matching(a[bi+bk:], b[bj+bk:])
}

var artistSplit = regexp.MustCompile(`(?i)[,;/&]|\sfeat\.?\s|\sft\.?\s|\sx\s`)

// SplitArtists zerlegt "A feat. B & C" in Einzelkünstler.
func SplitArtists(s string) []string {
	var out []string
	for _, p := range artistSplit.Split(s, -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Query ist eine Deezer-Suchanfrage mit Trefferlimit.
type Query struct {
	Q     string
	Limit int
}

var (
	parenRx = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]`)
	junkRx  = regexp.MustCompile(`(?i)\s*[\(\[\{]\s*(official\s*(music\s*)?(video|audio|lyric\s*video|visuali[sz]er)|music video|lyrics?(\s*video)?|audio|visuali[sz]er|hd|hq|4k|explicit|video)\s*[\)\]\}]\s*$`)
	topicRx = regexp.MustCompile(`(?i)\s+-\s+topic$`)
)

// bare entfernt alle Klammerzusätze: "Happy (From Despicable Me 2)" -> "Happy".
func bare(title string) string {
	if b := strings.TrimSpace(parenRx.ReplaceAllString(title, "")); b != "" {
		return b
	}
	return title
}

// CleanTitle entfernt YouTube-Zusätze ("(Official Video)", "[Lyrics]") und ein vorangestelltes "Artist - ".
func CleanTitle(artist, title string) string {
	for i := 0; i < 3; i++ {
		title = junkRx.ReplaceAllString(title, "")
	}
	if i := strings.Index(title, " - "); i > 0 {
		head := title[:i]
		for _, a := range append(SplitArtists(artist), artist) {
			if Ratio(head, a) >= 0.8 {
				title = title[i+3:]
				break
			}
		}
	}
	return strings.TrimSpace(title)
}

// CleanArtist entfernt das YouTube-Anhängsel " - Topic".
func CleanArtist(artist string) string {
	return strings.TrimSpace(topicRx.ReplaceAllString(artist, ""))
}

// Queries liefert die Suchkette. Die frühere Anfrage artist:"X" track:"Y" liefert bei Deezer derzeit nie Treffer.
// Der Artist wird deshalb nur über den Score geprüft; track:"..." fängt Titel mit Apostroph ab.
func Queries(artist, title string) []Query {
	mainArtist := artist
	if p := SplitArtists(artist); len(p) > 0 {
		mainArtist = p[0]
	}
	b := bare(title)
	qs := []Query{{artist + " " + title, 10}}
	if q := mainArtist + " " + b; q != qs[0].Q {
		qs = append(qs, Query{q, 10})
	}
	return append(qs, Query{`track:"` + strings.NewReplacer(`"`, " ").Replace(b) + `"`, 25})
}

// Versionsmerkmale: Live, Remix, Akustik, Karaoke/Cover, Demo, Teil-Nummern. Sie müssen im Tag-Titel und im
// Deezer-Titel übereinstimmen, sonst ist es eine andere Aufnahme (ratio() allein lässt "(Live)" bei langen Titeln durch).
var versionRx = map[string]*regexp.Regexp{
	"live":  regexp.MustCompile(`\b(live|concert|unplugged|sessions?)\b`),
	"remix": regexp.MustCompile(`\b(remix(ed)?|rmx|bootleg|mash ?up|rework|flip|vip|sped up|slowed|reverb|nightcore|8d)\b|\b(radio|club|extended|dub) (mix|edit)\b`),
	"acou":  regexp.MustCompile(`\b(acoustic|piano version|stripped|orchestral|symphonic|lullaby)\b`),
	"instr": regexp.MustCompile(`\b(instrumental|karaoke|a ?cap+ella|in the style|originally|made famous|tribute|cover|8 ?bit|16 ?bit)\b`),
	"demo":  regexp.MustCompile(`\b(demo|take \d+|alternate|unreleased|medley|re ?recorded)\b`),
	"part":  regexp.MustCompile(`\b(part|pt) ?\d+\b|\b(reprise|intro|outro|interlude|skit)\b`),
}

// versionFlags liefert pro gefundenem Merkmal den Treffertext ("part 2" != "part 1").
func versionFlags(s string) map[string]string {
	n, m := Norm(s), map[string]string{}
	for k, rx := range versionRx {
		if f := rx.FindString(n); f != "" {
			if k == "part" {
				m[k] = f
			} else {
				m[k] = "1"
			}
		}
	}
	return m
}

func sameFlags(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

var featSuffix = regexp.MustCompile(`(?i)\s*([\(\[]\s*(feat\.?|ft\.?|featuring|with)\s[^\)\]]*[\)\]]|\s(feat\.?|ft\.?|featuring)\s.*)$`)

// StripFeat entfernt "(feat. X)", "[ft. X]" und " feat. X" am Titelende. Remix-/Live-Zusätze bleiben bewusst stehen.
func StripFeat(title string) string { return strings.TrimSpace(featSuffix.ReplaceAllString(title, "")) }

func artistRatio(artist string, t Track) float64 {
	var r float64
	for _, a := range append(SplitArtists(artist), artist) {
		r = max(r, Ratio(a, t.Artist.Name))
	}
	return r
}

// Score bewertet einen Treffer: min(Titel-, Artist-Ähnlichkeit). Ein Treffer mit anderem Versionsmerkmal
// (Live, Remix, Karaoke ...) bekommt 0. Der Titel wird in mehreren Varianten verglichen: voll, ohne feat.-Zusatz,
// title_short und ohne Klammerzusatz ("(Remastered 2021)", "(From ...)").
func Score(artist, title string, t Track) float64 {
	if !sameFlags(versionFlags(title), versionFlags(t.Title)) {
		return 0
	}
	mine := []string{title, StripFeat(title), bare(title)}
	theirs := []string{t.Title, StripFeat(t.Title), t.TitleShort, bare(t.Title)}
	var tTitle float64
	for _, a := range mine {
		for _, b := range theirs {
			if b != "" {
				tTitle = max(tTitle, Ratio(a, b))
			}
		}
	}
	return min(tTitle, artistRatio(artist, t))
}

// Evaluate filtert Treffer nach Mindest-Score und Laufzeit (length == 0: Laufzeit unbekannt, wird nicht geprüft; durTolMax 0 = aus).
// Sortierung: Score, dann Treffer ohne Versionszusatz, dann kleinste Laufzeitabweichung.
// best ist der beste Treffer auch bei Ablehnung (für den Grund).
func Evaluate(artist, title string, length float64, tracks []Track, minScore, durTol, durTolMax float64) (ok []Candidate, best *Candidate) {
	for _, t := range tracks {
		c := Candidate{Track: t, Score: Score(artist, title, t)}
		if length > 0 {
			d := float64(t.Duration) - length
			if d < 0 {
				d = -d
			}
			c.DurDiff = d
		}
		if best == nil || c.Score > best.Score {
			cc := c
			best = &cc
		}
		// Laufzeit: bis durTol immer ok. Explo-Dateien sind YouTube-Rips (Intro/Outro, Clips): bis durTolMax nur bei
		// praktisch exaktem Titel/Artist (Score >= 0.95).
		if c.Score >= minScore && (length == 0 || c.DurDiff <= durTol ||
			(durTolMax > durTol && c.Score >= max(minScore, 0.95) && c.DurDiff <= durTolMax)) {
			ok = append(ok, c)
		}
	}
	suffix := func(t Track) int {
		if t.TitleShort != "" && Norm(t.TitleShort) != Norm(t.Title) {
			return 1
		}
		return 0
	}
	sort.SliceStable(ok, func(i, j int) bool {
		a, b := ok[i], ok[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if sa, sb := suffix(a.Track), suffix(b.Track); sa != sb {
			return sa < sb
		}
		return a.DurDiff < b.DurDiff
	})
	return ok, best
}

// ExactArtist: Artist des Treffers entspricht dem Tag praktisch exakt ("Johnny Cash", nicht "Johnny Crash").
func ExactArtist(artist string, t Track) bool { return artistRatio(artist, t) >= 0.98 }

// typeRank: album > ep > single > compilation > sonstiges.
func typeRank(t string) int {
	switch strings.ToLower(t) {
	case "album":
		return 0
	case "ep":
		return 1
	case "single":
		return 2
	case "compilation":
		return 3
	}
	return 4
}

var (
	compRx    = regexp.MustCompile(`(?i)greatest hits|best of|essential|collection|sampler|soundtrack|\bost\b|anthology|das beste|the very best|\blive\b|unplugged|sessions|\bnow\b|party|karaoke|tribute`)
	editionRx = regexp.MustCompile(`(?i)deluxe|bonus|expanded|anniversary|remaster|re-?edition|special edition|super`)
)

// IsSampler: Album verschiedener Interpreten ("Various Artists", "Verschiedene Interpreten") oder fremder Künstler.
func IsSampler(a Album, trackArtist string) bool {
	n := Norm(a.Artist.Name)
	if n == "" {
		return false
	}
	if strings.Contains(n, "various") || strings.Contains(n, "verschiedene") {
		return true
	}
	m := 0.0
	for _, x := range append(SplitArtists(trackArtist), trackArtist) {
		m = max(m, Ratio(x, a.Artist.Name))
	}
	return m < 0.5
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// better: a ist ein besseres Album als b - Künstleralbum vor Sampler, normales Album vor Best-of/Live/Soundtrack,
// album > ep > single > compilation, Standardausgabe vor Deluxe/Remaster, früheres Datum, kürzerer Titel.
func better(a, b Album, trackArtist string) bool {
	type key struct{ sampler, comp, typ, edition int }
	k := func(x Album) key {
		return key{b2i(IsSampler(x, trackArtist)), b2i(compRx.MatchString(x.Title)), typeRank(x.RecordType), b2i(editionRx.MatchString(x.Title))}
	}
	ka, kb := k(a), k(b)
	switch {
	case ka.sampler != kb.sampler:
		return ka.sampler < kb.sampler
	case ka.comp != kb.comp:
		return ka.comp < kb.comp
	case ka.typ != kb.typ:
		return ka.typ < kb.typ
	case ka.edition != kb.edition:
		return ka.edition < kb.edition
	case a.ReleaseDate != b.ReleaseDate && a.ReleaseDate != "" && (b.ReleaseDate == "" || a.ReleaseDate < b.ReleaseDate):
		return true
	case a.ReleaseDate == b.ReleaseDate:
		return len(a.Title) < len(b.Title)
	}
	return false
}

// PickAlbum wählt unter mehreren Alben derselben Aufnahme das beste (siehe better).
func PickAlbum(albums []Album, trackArtist string) Album {
	best := albums[0]
	for _, a := range albums[1:] {
		if better(a, best, trackArtist) {
			best = a
		}
	}
	return best
}
