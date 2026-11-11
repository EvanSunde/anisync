// Package media turns file paths / player titles into (title, season, episode)
// and provides title normalization and fuzzy matching.
package media

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/nssteinbrenner/anitogo"
)

type Parsed struct {
	Title   string
	Season  int
	Episode int  // 0 = no episode number (movie, single OVA...)
	Skip    bool // recap/fractional episodes like 12.5, previews, NCOP...
}

var (
	reDigits      = regexp.MustCompile(`^\d{1,4}$`)
	reSeasonDir   = regexp.MustCompile(`(?i)^(?:season|series|s)\s*0*(\d{1,2})$`)
	reSeasonInStr = regexp.MustCompile(`(?i)\b(?:season\s*0*(\d{1,2})|(\d{1,2})(?:st|nd|rd|th)\s+season|s0*(\d{1,2}))\s*$`)
	reEpisodeOnly = regexp.MustCompile(`(?i)^(?:episode|ep|e|#)?\s*0*(\d{1,4})(?:v\d)?$`)
	reStreamEp    = regexp.MustCompile(`(?i)^(.*?)[\s\-:]*\b(?:episode|ep\.?)\s*0*(\d{1,4})\b`)
)

// FromURL converts an xesam:url into a local path ("" if not a file URL).
func FromURL(u string) string {
	if !strings.HasPrefix(u, "file://") {
		if strings.HasPrefix(u, "/") {
			return u
		}
		return ""
	}
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Path
}

// ParsePath parses a local file path, falling back to folder names when the
// file name alone has no usable title ("05.mkv", "Episode 05.mkv").
func ParsePath(path string) Parsed {
	base := filepath.Base(path)
	p := parseName(base)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if p.Episode == 0 {
		if m := reEpisodeOnly.FindStringSubmatch(strings.TrimSpace(stem)); m != nil {
			p.Episode, _ = strconv.Atoi(m[1])
		}
	}
	if !genericTitle(p.Title) {
		return p
	}
	dir := filepath.Dir(path)
	for i := 0; i < 2 && dir != "/" && dir != "."; i++ {
		name := filepath.Base(dir)
		if m := reSeasonDir.FindStringSubmatch(name); m != nil {
			if p.Season == 0 {
				p.Season, _ = strconv.Atoi(m[1])
			}
			dir = filepath.Dir(dir)
			continue
		}
		d := parseName(name)
		t := d.Title
		if t == "" {
			t = name
		}
		if p.Season == 0 {
			p.Season = d.Season
		}
		p.Title = t
		break
	}
	p.Title, p.Season = splitSeason(p.Title, p.Season)
	return p
}

// ParseTitle parses a player-provided media title (streams).
func ParseTitle(title string) Parsed {
	p := parseName(title)
	if p.Episode == 0 || genericTitle(p.Title) {
		if m := reStreamEp.FindStringSubmatch(title); m != nil && strings.TrimSpace(m[1]) != "" {
			p.Title = strings.TrimSpace(m[1])
			p.Episode, _ = strconv.Atoi(m[2])
		}
	}
	p.Title, p.Season = splitSeason(p.Title, p.Season)
	return p
}

func parseName(name string) Parsed {
	e := anitogo.Parse(name, anitogo.DefaultOptions)
	var p Parsed
	p.Title = strings.TrimSpace(e.AnimeTitle)
	if n := len(e.EpisodeNumber); n > 0 {
		ep := e.EpisodeNumber[n-1]
		if strings.ContainsAny(ep, ".,") {
			p.Skip = true
		}
		p.Episode, _ = strconv.Atoi(strings.TrimLeft(ep, "0"))
		if p.Episode == 0 && strings.Trim(ep, "0") == "" && ep != "" {
			p.Episode = 0 // episode 00 → treat as special
			p.Skip = true
		}
	}
	if n := len(e.AnimeSeason); n > 0 {
		p.Season, _ = strconv.Atoi(e.AnimeSeason[n-1])
	}
	for _, t := range e.AnimeType {
		switch strings.ToUpper(t) {
		case "NCOP", "NCED", "OP", "ED", "OPENING", "ENDING", "PV", "PREVIEW", "CM", "MENU", "TRAILER", "TEASER":
			p.Skip = true
		}
	}
	return p
}

// splitSeason moves a trailing "Season 2" / "2nd Season" / "S2" from the
// title into the season number.
func splitSeason(title string, season int) (string, int) {
	m := reSeasonInStr.FindStringSubmatchIndex(title)
	if m == nil {
		return title, season
	}
	for g := 1; g <= 3; g++ {
		if m[2*g] >= 0 {
			n, _ := strconv.Atoi(title[m[2*g]:m[2*g+1]])
			t := strings.TrimRight(strings.TrimSpace(title[:m[0]]), " -:")
			if t == "" {
				return title, season
			}
			if season == 0 {
				season = n
			}
			return t, season
		}
	}
	return title, season
}

func genericTitle(t string) bool {
	n := Normalize(t)
	if n == "" || reDigits.MatchString(n) {
		return true
	}
	switch n {
	case "episode", "ep", "e", "video", "movie", "untitled":
		return true
	}
	return reEpisodeOnly.MatchString(n)
}

// Normalize lowercases and strips punctuation so "Re:Zero" == "re zero".
func Normalize(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(r)
			space = false
		case r == '\'' || r == '’' || r == '`':
			// "Frieren's" -> "frierens"
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// Key is the mapping key for an anime title + season.
func Key(title string, season int) string {
	k := "a:" + Normalize(title)
	if season > 1 {
		k += "#s" + strconv.Itoa(season)
	}
	return k
}

// VNKey is the mapping key for a VN folder.
func VNKey(folder string) string { return "v:" + Normalize(folder) }

// Similarity is the Sørensen–Dice coefficient over character bigrams of the
// normalized strings (1 = identical).
func Similarity(a, b string) float64 {
	a, b = strings.ReplaceAll(Normalize(a), " ", ""), strings.ReplaceAll(Normalize(b), " ", "")
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) < 2 || len(rb) < 2 {
		return 0
	}
	grams := map[[2]rune]int{}
	for i := 0; i+1 < len(ra); i++ {
		grams[[2]rune{ra[i], ra[i+1]}]++
	}
	inter := 0
	for i := 0; i+1 < len(rb); i++ {
		g := [2]rune{rb[i], rb[i+1]}
		if grams[g] > 0 {
			grams[g]--
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(ra)-1+len(rb)-1)
}

var reVNNoise = regexp.MustCompile(`(?i)(\[[^\]]*\]|\([^)]*\)|\bv?\d+(\.\d+)+[a-z]?\b|\b(english|eng|jp|jap|patched|steam|repack|portable|full|voice[ds]?|uncensored|censored|edition|ver(sion)?)\b)`)

// CleanVNFolder turns "[Studio] Some_Visual-Novel v1.02 (ENG)" into "Some Visual Novel".
func CleanVNFolder(name string) string {
	s := reVNNoise.ReplaceAllString(name, " ")
	s = strings.NewReplacer("_", " ", ".", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

var reTrailingQualifier = regexp.MustCompile(`\s*[(\[](?:\d{4}|tv|tv series)[)\]]\s*$`)

// MatchForm normalizes a title for exact comparison, ignoring a trailing
// "(2011)" / "(TV)" so remakes like "HUNTER×HUNTER (2011)" compare equal to
// the plain title and the match is treated as ambiguous.
func MatchForm(s string) string {
	s = reTrailingQualifier.ReplaceAllString(strings.TrimSpace(strings.ToLower(s)), "")
	s = strings.NewReplacer("×", "x", "☆", " ", "★", " ", "♪", " ").Replace(s)
	return Normalize(s)
}
