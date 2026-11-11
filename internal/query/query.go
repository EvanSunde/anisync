// Package query parses the search syntax into an AniList filter and back
// (Format), so typed queries and the TUI's filter editor stay in sync.
//
//	frieren                    title search
//	g:action  -g:ecchi         genres (must have all / exclude); repeatable
//	t:"time skip"  -t:harem    tags (must have all / exclude)
//	f:tv,movie,ova,ona,special,short,music
//	y:2023  y:2015-2020  y:>2018  y:<2010
//	s:winter|spring|summer|fall
//	st:airing|finished|upcoming|hiatus|cancelled
//	score>75  score<60  score:70-85
//	eps<13  eps>24  eps:12  eps:10-13
//	pop>50000                  popularity (users with it on their list)
//	src:manga,ln,original      source material
//	c:jp|cn|kr|tw              country of origin
//	sort:pop|score|trending|fav|new|old|title|eps|worst|obscure
//	list:yes|no  adult:yes
package query

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/evansunde/anisync/internal/anilist"
)

const Help = `free text           title search (romaji, english, native, synonyms)
g:action  -g:ecchi  genres: must have all / exclude (repeat g: for more)
t:"time skip"       tags: must have all             -t:harem   exclude tag
f:tv,movie,ova,ona,special,short,music              format
y:2023  y:2015-2020  y:>2018  y:<2010               year
s:winter|spring|summer|fall                         season (with y:)
st:airing|finished|upcoming|hiatus|cancelled        status
score>75  score:70-85                               average score (0-100)
eps<13  eps:10-13                                   episode count
pop>50000                                           popularity (users)
src:manga,ln,original,game,vn,novel,web             source material
c:jp|cn|kr|tw                                       country of origin
sort:pop|score|trending|fav|new|old|title|eps|worst|obscure
list:yes|no   adult:yes                             on your list / adult titles`

// Vocab is the set of known genres and tags (case-insensitive lookup).
type Vocab struct {
	Genres map[string]string // lower -> canonical
	Tags   map[string]string
}

func NewVocab(genres, tags []string) *Vocab {
	v := &Vocab{Genres: map[string]string{}, Tags: map[string]string{}}
	for _, g := range genres {
		v.Genres[strings.ToLower(g)] = g
	}
	for _, t := range tags {
		v.Tags[strings.ToLower(t)] = t
	}
	return v
}

// GenreList / TagList return sorted canonical names.
func (v *Vocab) GenreList() []string { return sortedValues(v.Genres) }
func (v *Vocab) TagList() []string   { return sortedValues(v.Tags) }

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, x := range m {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

// Choice is one option of an enumerated filter: query token, API value, label.
type Choice struct{ Token, Value, Label string }

var Formats = []Choice{{"tv", "TV", "TV"}, {"short", "TV_SHORT", "TV Short"}, {"movie", "MOVIE", "Movie"},
	{"special", "SPECIAL", "Special"}, {"ova", "OVA", "OVA"}, {"ona", "ONA", "ONA"}, {"music", "MUSIC", "Music"}}

var Statuses = []Choice{{"airing", "RELEASING", "Airing"}, {"finished", "FINISHED", "Finished"},
	{"upcoming", "NOT_YET_RELEASED", "Upcoming"}, {"hiatus", "HIATUS", "Hiatus"}, {"cancelled", "CANCELLED", "Cancelled"}}

var Seasons = []Choice{{"winter", "WINTER", "Winter"}, {"spring", "SPRING", "Spring"}, {"summer", "SUMMER", "Summer"}, {"fall", "FALL", "Fall"}}

var Sources = []Choice{{"original", "ORIGINAL", "Original"}, {"manga", "MANGA", "Manga"}, {"ln", "LIGHT_NOVEL", "Light novel"},
	{"wn", "WEB_NOVEL", "Web novel"}, {"novel", "NOVEL", "Novel"}, {"vn", "VISUAL_NOVEL", "Visual novel"},
	{"game", "VIDEO_GAME", "Video game"}, {"other", "OTHER", "Other"}}

var Countries = []Choice{{"jp", "JP", "Japan"}, {"cn", "CN", "China"}, {"kr", "KR", "South Korea"}, {"tw", "TW", "Taiwan"}}

var Sorts = []Choice{{"match", "SEARCH_MATCH", "Best match"}, {"pop", "POPULARITY_DESC", "Most popular"},
	{"score", "SCORE_DESC", "Highest score"}, {"trending", "TRENDING_DESC", "Trending"}, {"fav", "FAVOURITES_DESC", "Most favourited"},
	{"new", "START_DATE_DESC", "Newest"}, {"old", "START_DATE", "Oldest"}, {"title", "TITLE_ROMAJI", "Title A–Z"},
	{"eps", "EPISODES_DESC", "Most episodes"}, {"worst", "SCORE", "Lowest score"}, {"obscure", "POPULARITY", "Least popular"}}

var aliases = map[string]string{"short": "short", "tv_short": "short", "film": "movie", "sp": "special", "web": "ona", "mv": "music",
	"releasing": "airing", "ongoing": "airing", "done": "finished", "unreleased": "upcoming", "notyet": "upcoming", "canceled": "cancelled",
	"autumn": "fall", "popular": "pop", "popularity": "pop", "rating": "score", "top": "score", "rank": "score", "trend": "trending",
	"newest": "new", "oldest": "old", "episodes": "eps", "favourites": "fav", "favorites": "fav", "relevance": "match",
	"lowest": "worst", "light_novel": "ln", "lightnovel": "ln", "web_novel": "wn", "webnovel": "wn", "visual_novel": "vn",
	"video_game": "game", "japan": "jp", "china": "cn", "korea": "kr", "taiwan": "tw", "least": "obscure"}

func find(cs []Choice, s string) (Choice, bool) {
	s = strings.ToLower(s)
	if a, ok := aliases[s]; ok {
		s = a
	}
	for _, c := range cs {
		if c.Token == s || strings.EqualFold(c.Value, s) {
			return c, true
		}
	}
	return Choice{}, false
}

// ByValue finds a choice by API value.
func ByValue(cs []Choice, v string) (Choice, bool) {
	for _, c := range cs {
		if c.Value == v {
			return c, true
		}
	}
	return Choice{}, false
}

// Parse turns a query string into a filter. Problems (unknown genre, bad
// number…) are returned as warnings; the rest of the query still applies.
func Parse(s string, v *Vocab) (anilist.Filter, []string) {
	var f anilist.Filter
	var warn []string
	var text []string
	for _, tok := range tokenize(s) {
		neg := false
		body := tok
		if strings.HasPrefix(body, "-") && strings.ContainsAny(body, ":<>") {
			neg, body = true, body[1:]
		}
		key, val, op, ok := splitKV(body)
		if !ok {
			text = append(text, strings.Trim(tok, `"`))
			continue
		}
		multi := func(cs []Choice, what string, dst *[]string) {
			for _, item := range splitList(val) {
				if c, ok := find(cs, item); ok {
					*dst = appendUnique(*dst, c.Value)
				} else {
					warn = append(warn, fmt.Sprintf("unknown %s %q", what, item))
				}
			}
		}
		single := func(cs []Choice, what string, dst *string) {
			if c, ok := find(cs, strings.Trim(val, `"`)); ok {
				*dst = c.Value
			} else {
				warn = append(warn, fmt.Sprintf("unknown %s %q", what, val))
			}
		}
		switch key {
		case "g", "genre", "genres", "t", "tag", "tags":
			for _, item := range splitList(val) {
				name, isGenre, found := v.lookup(item, key[0] == 'g')
				if !found {
					warn = append(warn, fmt.Sprintf("unknown genre/tag %q", item))
					continue
				}
				switch {
				case isGenre && neg:
					f.NotGenres = appendUnique(f.NotGenres, name)
				case isGenre:
					f.Genres = appendUnique(f.Genres, name)
				case neg:
					f.NotTags = appendUnique(f.NotTags, name)
				default:
					f.Tags = appendUnique(f.Tags, name)
				}
			}
		case "f", "format", "type":
			multi(Formats, "format", &f.Formats)
		case "src", "source":
			multi(Sources, "source", &f.Sources)
		case "y", "year":
			if err := parseRange(val, op, 1900, 2100, &f.YearFrom, &f.YearTo, &f.Year); err != nil {
				warn = append(warn, "year: "+err.Error())
			}
		case "s", "season":
			single(Seasons, "season", &f.Season)
		case "st", "status":
			single(Statuses, "status", &f.Status)
		case "c", "country":
			single(Countries, "country", &f.Country)
		case "score", "rating":
			var exact int
			if err := parseRange(scaleScore(val), op, 0, 100, &f.ScoreMin, &f.ScoreMax, &exact); err != nil {
				warn = append(warn, "score: "+err.Error())
			} else if exact > 0 {
				f.ScoreMin, f.ScoreMax = exact, exact
			}
		case "eps", "ep", "episodes":
			var exact int
			if err := parseRange(val, op, 0, 5000, &f.EpsMin, &f.EpsMax, &exact); err != nil {
				warn = append(warn, "episodes: "+err.Error())
			} else if exact > 0 {
				f.EpsMin, f.EpsMax = exact, exact
			}
		case "pop", "popularity", "users":
			n, err := strconv.Atoi(strings.TrimSuffix(strings.ToLower(val), "k"))
			if err != nil {
				warn = append(warn, fmt.Sprintf("bad popularity %q", val))
				continue
			}
			if strings.HasSuffix(strings.ToLower(val), "k") {
				n *= 1000
			}
			if op == '>' {
				n++
			}
			f.PopMin = n
		case "sort", "order":
			if c, ok := find(Sorts, val); ok {
				f.Sort = []string{c.Value}
			} else {
				warn = append(warn, fmt.Sprintf("unknown sort %q", val))
			}
		case "list", "onlist":
			b := yes(val)
			f.OnList = &b
		case "adult", "nsfw":
			f.Adult = yes(val)
		default:
			text = append(text, tok)
		}
	}
	f.Search = strings.Join(text, " ")
	return f, warn
}

// Format renders a filter back into query syntax (Parse(Format(f)) == f).
func Format(f anilist.Filter) string {
	var p []string
	if f.Search != "" {
		p = append(p, f.Search)
	}
	q := func(s string) string {
		if strings.ContainsAny(s, " ,") {
			return `"` + s + `"`
		}
		return s
	}
	for _, g := range f.Genres {
		p = append(p, "g:"+q(g))
	}
	for _, g := range f.NotGenres {
		p = append(p, "-g:"+q(g))
	}
	for _, t := range f.Tags {
		p = append(p, "t:"+q(t))
	}
	for _, t := range f.NotTags {
		p = append(p, "-t:"+q(t))
	}
	tokens := func(cs []Choice, vals []string) string {
		var out []string
		for _, v := range vals {
			if c, ok := ByValue(cs, v); ok {
				out = append(out, c.Token)
			}
		}
		return strings.Join(out, ",")
	}
	if len(f.Formats) > 0 {
		p = append(p, "f:"+tokens(Formats, f.Formats))
	}
	if f.Season != "" {
		p = append(p, "s:"+tokens(Seasons, []string{f.Season}))
	}
	switch {
	case f.Year > 0:
		p = append(p, fmt.Sprintf("y:%d", f.Year))
	case f.YearFrom > 0 || f.YearTo > 0:
		p = append(p, "y:"+rangeStr(f.YearFrom, f.YearTo))
	}
	if f.Status != "" {
		p = append(p, "st:"+tokens(Statuses, []string{f.Status}))
	}
	if f.ScoreMin > 0 || f.ScoreMax > 0 {
		p = append(p, "score:"+rangeStr(f.ScoreMin, f.ScoreMax))
	}
	if f.EpsMin > 0 || f.EpsMax > 0 {
		p = append(p, "eps:"+rangeStr(f.EpsMin, f.EpsMax))
	}
	if f.PopMin > 0 {
		p = append(p, fmt.Sprintf("pop:%d", f.PopMin))
	}
	if len(f.Sources) > 0 {
		p = append(p, "src:"+tokens(Sources, f.Sources))
	}
	if f.Country != "" {
		p = append(p, "c:"+tokens(Countries, []string{f.Country}))
	}
	if f.OnList != nil {
		p = append(p, map[bool]string{true: "list:yes", false: "list:no"}[*f.OnList])
	}
	if f.Adult {
		p = append(p, "adult:yes")
	}
	if len(f.Sort) > 0 {
		if c, ok := ByValue(Sorts, f.Sort[0]); ok {
			p = append(p, "sort:"+c.Token)
		}
	}
	return strings.Join(p, " ")
}

// scaleScore turns a 10-point score ("7.5", "7-9") into the 100 scale.
func scaleScore(val string) string {
	prefix := ""
	if len(val) > 0 && (val[0] == '>' || val[0] == '<') {
		prefix, val = val[:1], val[1:]
	}
	parts := strings.Split(val, "-")
	nums := make([]float64, len(parts))
	for i, p := range parts {
		if p == "" {
			continue
		}
		n, err := strconv.ParseFloat(p, 64)
		if err != nil || n > 10 {
			return prefix + val // already 0-100 (or invalid: reported later)
		}
		nums[i] = n
	}
	for i, p := range parts {
		if p != "" {
			parts[i] = strconv.Itoa(int(nums[i]*10 + 0.5))
		}
	}
	return prefix + strings.Join(parts, "-")
}

func rangeStr(lo, hi int) string {
	switch {
	case lo > 0 && hi > 0 && lo == hi:
		return strconv.Itoa(lo)
	case lo > 0 && hi > 0:
		return fmt.Sprintf("%d-%d", lo, hi)
	case lo > 0:
		return fmt.Sprintf(">%d", lo-1)
	}
	return fmt.Sprintf("<%d", hi+1)
}

func appendUnique(l []string, s string) []string {
	for _, x := range l {
		if x == s {
			return l
		}
	}
	return append(l, s)
}

func yes(s string) bool {
	switch strings.ToLower(s) {
	case "yes", "y", "true", "1", "on":
		return true
	}
	return false
}

func (v *Vocab) lookup(item string, preferGenre bool) (name string, isGenre, ok bool) {
	l := strings.ToLower(strings.TrimSpace(item))
	if v == nil {
		return item, preferGenre, item != ""
	}
	g, gok := v.Genres[l]
	t, tok := v.Tags[l]
	switch {
	case gok && (preferGenre || !tok):
		return g, true, true
	case tok:
		return t, false, true
	}
	return "", false, false
}

// splitKV handles "key:value", "key>value", "key<value", "key:>value".
func splitKV(tok string) (key, val string, op byte, ok bool) {
	i := strings.IndexAny(tok, ":<>")
	if i <= 0 || i == len(tok)-1 {
		return "", "", 0, false
	}
	key = strings.ToLower(tok[:i])
	for _, r := range key {
		if !unicode.IsLetter(r) {
			return "", "", 0, false
		}
	}
	op, val = tok[i], tok[i+1:]
	if op == ':' && len(val) > 1 && (val[0] == '<' || val[0] == '>') {
		op, val = val[0], val[1:]
	}
	if op == ':' {
		op = '='
	}
	return key, val, op, true
}

// parseRange handles "2020", "2015-2020", ">2018", "<2010" (op from key>val).
func parseRange(val string, op byte, lo, hi int, min, max, exact *int) error {
	num := func(s string) (int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < lo || n > hi {
			return 0, fmt.Errorf("bad value %q", s)
		}
		return n, nil
	}
	if op == '=' && len(val) > 1 && (val[0] == '>' || val[0] == '<') {
		op, val = val[0], val[1:]
	}
	if a, b, ok := strings.Cut(val, "-"); ok && op == '=' {
		var x, y int
		var err error
		if a != "" {
			if x, err = num(a); err != nil {
				return err
			}
		}
		if b != "" {
			if y, err = num(b); err != nil {
				return err
			}
		}
		if x > 0 && y > 0 && x > y {
			return fmt.Errorf("empty range %q", val)
		}
		*min, *max = x, y
		return nil
	}
	n, err := num(val)
	if err != nil {
		return err
	}
	switch op {
	case '>':
		*min = n + 1
	case '<':
		*max = n - 1
	default:
		*exact = n
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	var b strings.Builder
	inQ := false
	flush := func() {
		if p := strings.TrimSpace(b.String()); p != "" {
			out = append(out, p)
		}
		b.Reset()
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
		case r == ',' && !inQ:
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return out
}

// tokenize splits on spaces, keeping quoted parts together (t:"time skip").
func tokenize(s string) []string {
	var out []string
	var b strings.Builder
	inQ := false
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
			b.WriteRune(r)
		case unicode.IsSpace(r) && !inQ:
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}
