package query

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// VNHelp documents the visual novel search syntax (VNDB).
const VNHelp = `free text               title search (any language, aliases)
t:"male protagonist"    tags: must have all       -t:nukige   exclude tag
len:short,medium        length: vshort,short,medium,long,vlong (any of)
y:2023  y:2015-2020  y:>2018                      release year
rating>7.5  rating:70-90                          VNDB rating (0-10 or 10-100)
votes>100                                         number of votes
lang:en,ja              available in any of these languages
ol:ja                   original language
p:win,lin               platform (any of): win lin mac web and ios swi ps4 ps5 psv
dev:key  dev:"type-moon"  dev:p24                 developer (any of)
sort:match|rating|votes|new|old|title|worst|obscure`

// VNFilter is a VNDB visual novel search. Zero values mean "no filter".
type VNFilter struct {
	Search    string
	Tags      []string // names (or g123 ids): must have all
	NotTags   []string
	Lengths   []string // "1".."5": any of
	Langs     []string // any of
	OLang     string
	Platforms []string // any of
	Devs      []string // names or p123 ids: any of
	Year      int
	YearFrom  int
	YearTo    int
	RatingMin int // 10-100
	RatingMax int
	VotesMin  int
	Sort      string // VNSorts value
}

// Empty reports whether no filter (other than text/sort) is set.
func (f VNFilter) Empty() bool {
	return len(f.Tags)+len(f.NotTags)+len(f.Lengths)+len(f.Langs)+len(f.Platforms)+len(f.Devs) == 0 && f.OLang == "" &&
		f.Year == 0 && f.YearFrom == 0 && f.YearTo == 0 && f.RatingMin == 0 && f.RatingMax == 0 && f.VotesMin == 0
}

var VNSorts = []Choice{{"match", "searchrank", "Best match"}, {"rating", "rating", "Highest rated"},
	{"votes", "votecount", "Most votes"}, {"new", "released", "Newest"}, {"old", "released_asc", "Oldest"},
	{"title", "title", "Title A–Z"}, {"worst", "rating_asc", "Lowest rated"}, {"obscure", "votecount_asc", "Fewest votes"}}

var VNLengths = []Choice{{"vshort", "1", "Very short (< 2h)"}, {"short", "2", "Short (2–10h)"}, {"medium", "3", "Medium (10–30h)"},
	{"long", "4", "Long (30–50h)"}, {"vlong", "5", "Very long (> 50h)"}}

var VNLangs = []Choice{{"en", "en", "English"}, {"ja", "ja", "Japanese"}, {"zh-hans", "zh-Hans", "Chinese (simplified)"},
	{"zh-hant", "zh-Hant", "Chinese (traditional)"}, {"ko", "ko", "Korean"}, {"ru", "ru", "Russian"}, {"es", "es", "Spanish"},
	{"pt-br", "pt-br", "Portuguese (Brazil)"}, {"de", "de", "German"}, {"fr", "fr", "French"}, {"it", "it", "Italian"},
	{"pl", "pl", "Polish"}, {"vi", "vi", "Vietnamese"}, {"id", "id", "Indonesian"}, {"th", "th", "Thai"}, {"uk", "uk", "Ukrainian"},
	{"tr", "tr", "Turkish"}}

var VNPlatforms = []Choice{{"win", "win", "Windows"}, {"lin", "lin", "Linux"}, {"mac", "mac", "macOS"}, {"web", "web", "Web"},
	{"and", "and", "Android"}, {"ios", "ios", "iOS"}, {"swi", "swi", "Switch"}, {"ps4", "ps4", "PS4"}, {"ps5", "ps5", "PS5"},
	{"psv", "psv", "PS Vita"}, {"psp", "psp", "PSP"}, {"ps3", "ps3", "PS3"}, {"ps2", "ps2", "PS2"}, {"xbo", "xbo", "Xbox One"},
	{"xxs", "xxs", "Xbox Series"}, {"n3d", "n3d", "3DS"}, {"nds", "nds", "DS"}, {"dvd", "dvd", "DVD player"}}

var vnAliases = map[string]string{"relevance": "match", "searchrank": "match", "score": "rating", "top": "rating", "best": "rating",
	"popular": "votes", "pop": "votes", "popularity": "votes", "votecount": "votes", "newest": "new", "released": "new",
	"oldest": "old", "lowest": "worst", "least": "obscure", "veryshort": "vshort", "very_short": "vshort", "verylong": "vlong",
	"very_long": "vlong", "english": "en", "japanese": "ja", "chinese": "zh-hans", "zh": "zh-hans", "zh-cn": "zh-hans",
	"zh-tw": "zh-hant", "korean": "ko", "russian": "ru", "spanish": "es", "german": "de", "french": "fr", "pt": "pt-br",
	"windows": "win", "linux": "lin", "macos": "mac", "osx": "mac", "android": "and", "switch": "swi", "vita": "psv",
	"1": "vshort", "2": "short", "3": "medium", "4": "long", "5": "vlong"}

// VNVocab maps tag names (lower case) to tag ids and canonical names.
type VNVocab struct {
	Tags map[string]VNTag
}

type VNTag struct{ ID, Name string }

func NewVNVocab(tags []VNTag) *VNVocab {
	v := &VNVocab{Tags: map[string]VNTag{}}
	for _, t := range tags {
		v.Add(t)
	}
	return v
}

func (v *VNVocab) Add(t VNTag) {
	if v != nil && t.ID != "" && t.Name != "" {
		v.Tags[strings.ToLower(t.Name)] = t
	}
}

// TagList returns the known tag names (in no particular order).
func (v *VNVocab) TagList() []string {
	if v == nil {
		return nil
	}
	out := make([]string, 0, len(v.Tags))
	for _, t := range v.Tags {
		out = append(out, t.Name)
	}
	return out
}

// TagID returns the id for a tag name or id ("" if unknown).
func (v *VNVocab) TagID(name string) string {
	if reTagID.MatchString(name) {
		return name
	}
	if v == nil {
		return ""
	}
	return v.Tags[strings.ToLower(name)].ID
}

var (
	reTagID  = regexp.MustCompile(`^g\d+$`)
	reProdID = regexp.MustCompile(`^p\d+$`)
)

// ParseVN turns a query string into a VN filter. Problems are returned as
// warnings; the rest still applies. Tags unknown to the vocabulary are kept
// as typed (they're resolved against VNDB when searching).
func ParseVN(s string, v *VNVocab) (VNFilter, []string) {
	var f VNFilter
	var warn, text []string
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
				if c, ok := findIn(cs, item, vnAliases); ok {
					*dst = appendUnique(*dst, c.Value)
				} else {
					warn = append(warn, fmt.Sprintf("unknown %s %q", what, item))
				}
			}
		}
		switch key {
		case "t", "tag", "tags", "g":
			for _, item := range splitList(val) {
				name := item
				if v != nil {
					if t, ok := v.Tags[strings.ToLower(item)]; ok {
						name = t.Name
					}
				}
				if neg {
					f.NotTags = appendUnique(f.NotTags, name)
				} else {
					f.Tags = appendUnique(f.Tags, name)
				}
			}
		case "len", "length", "l":
			multi(VNLengths, "length", &f.Lengths)
		case "lang", "language":
			multi(VNLangs, "language", &f.Langs)
		case "ol", "olang", "orig":
			if c, ok := findIn(VNLangs, strings.Trim(val, `"`), vnAliases); ok {
				f.OLang = c.Value
			} else {
				warn = append(warn, fmt.Sprintf("unknown language %q", val))
			}
		case "p", "platform", "plat":
			multi(VNPlatforms, "platform", &f.Platforms)
		case "dev", "developer", "by":
			for _, item := range splitList(val) {
				f.Devs = appendUnique(f.Devs, item)
			}
		case "y", "year":
			if err := parseRange(val, op, 1970, 2100, &f.YearFrom, &f.YearTo, &f.Year); err != nil {
				warn = append(warn, "year: "+err.Error())
			}
		case "rating", "score", "r":
			var exact int
			if err := parseRange(scaleScore(val), op, 10, 100, &f.RatingMin, &f.RatingMax, &exact); err != nil {
				warn = append(warn, "rating: "+err.Error())
			} else if exact > 0 {
				f.RatingMin, f.RatingMax = exact, exact
			}
		case "votes", "votecount", "v":
			n, err := strconv.Atoi(strings.TrimSuffix(strings.ToLower(val), "k"))
			if err != nil || n < 0 {
				warn = append(warn, fmt.Sprintf("bad vote count %q", val))
				continue
			}
			if strings.HasSuffix(strings.ToLower(val), "k") {
				n *= 1000
			}
			if op == '>' {
				n++
			}
			f.VotesMin = n
		case "sort", "order":
			if c, ok := findIn(VNSorts, val, vnAliases); ok {
				f.Sort = c.Value
			} else {
				warn = append(warn, fmt.Sprintf("unknown sort %q", val))
			}
		default:
			text = append(text, tok)
		}
	}
	f.Search = strings.Join(text, " ")
	return f, warn
}

// FormatVN renders a VN filter back into query syntax (ParseVN(FormatVN(f)) == f).
func FormatVN(f VNFilter) string {
	var p []string
	if f.Search != "" {
		p = append(p, f.Search)
	}
	q := func(s string) string {
		if strings.ContainsAny(s, " ,:<>") {
			return `"` + s + `"`
		}
		return s
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
	for _, t := range f.Tags {
		p = append(p, "t:"+q(t))
	}
	for _, t := range f.NotTags {
		p = append(p, "-t:"+q(t))
	}
	if len(f.Lengths) > 0 {
		p = append(p, "len:"+tokens(VNLengths, f.Lengths))
	}
	switch {
	case f.Year > 0:
		p = append(p, fmt.Sprintf("y:%d", f.Year))
	case f.YearFrom > 0 || f.YearTo > 0:
		p = append(p, "y:"+rangeStr(f.YearFrom, f.YearTo))
	}
	if f.RatingMin > 0 || f.RatingMax > 0 {
		p = append(p, "rating:"+rangeStr(f.RatingMin, f.RatingMax))
	}
	if f.VotesMin > 0 {
		p = append(p, fmt.Sprintf("votes:%d", f.VotesMin))
	}
	if len(f.Langs) > 0 {
		p = append(p, "lang:"+tokens(VNLangs, f.Langs))
	}
	if f.OLang != "" {
		p = append(p, "ol:"+tokens(VNLangs, []string{f.OLang}))
	}
	if len(f.Platforms) > 0 {
		p = append(p, "p:"+tokens(VNPlatforms, f.Platforms))
	}
	for _, d := range f.Devs {
		p = append(p, "dev:"+q(d))
	}
	if f.Sort != "" {
		if c, ok := ByValue(VNSorts, f.Sort); ok {
			p = append(p, "sort:"+c.Token)
		}
	}
	return strings.Join(p, " ")
}

// VNSortParams returns the API sort field and direction for a filter.
// "Best match" needs a text search; without one it falls back to votes.
func VNSortParams(f VNFilter) (sort string, reverse bool) {
	s := f.Sort
	if s == "" {
		s = "searchrank"
	}
	if s == "searchrank" && strings.TrimSpace(f.Search) == "" {
		s = "votecount"
	}
	switch s {
	case "searchrank", "title":
		return s, false
	case "rating_asc", "votecount_asc", "released_asc":
		return strings.TrimSuffix(s, "_asc"), false
	}
	return s, true
}

// VNFilters builds the VNDB "filters" value. tagID resolves a tag name to
// its id; tags it can't resolve are returned in missing (and left out).
func VNFilters(f VNFilter, tagID func(string) string) (filters any, missing []string) {
	and := []any{}
	anyOf := func(preds []any) {
		switch len(preds) {
		case 0:
		case 1:
			and = append(and, preds[0])
		default:
			and = append(and, append([]any{"or"}, preds...))
		}
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		and = append(and, []any{"search", "=", s})
	}
	for _, list := range []struct {
		names []string
		op    string
	}{{f.Tags, "="}, {f.NotTags, "!="}} {
		for _, n := range list.names {
			id := tagID(n)
			if id == "" {
				missing = append(missing, n)
				continue
			}
			and = append(and, []any{"tag", list.op, id})
		}
	}
	var preds []any
	for _, l := range f.Lengths {
		if n, err := strconv.Atoi(l); err == nil {
			preds = append(preds, []any{"length", "=", n})
		}
	}
	anyOf(preds)
	lo, hi := f.YearFrom, f.YearTo
	if f.Year > 0 {
		lo, hi = f.Year, f.Year
	}
	if lo > 0 {
		and = append(and, []any{"released", ">=", fmt.Sprintf("%04d-01-01", lo)})
	}
	if hi > 0 {
		and = append(and, []any{"released", "<", fmt.Sprintf("%04d-01-01", hi+1)})
	}
	if f.RatingMin > 0 {
		and = append(and, []any{"rating", ">=", f.RatingMin})
	}
	if f.RatingMax > 0 {
		and = append(and, []any{"rating", "<=", f.RatingMax})
	}
	if f.VotesMin > 0 {
		and = append(and, []any{"votecount", ">=", f.VotesMin})
	}
	preds = nil
	for _, l := range f.Langs {
		preds = append(preds, []any{"lang", "=", l})
	}
	anyOf(preds)
	if f.OLang != "" {
		and = append(and, []any{"olang", "=", f.OLang})
	}
	preds = nil
	for _, p := range f.Platforms {
		preds = append(preds, []any{"platform", "=", p})
	}
	anyOf(preds)
	preds = nil
	for _, d := range f.Devs {
		if reProdID.MatchString(d) {
			preds = append(preds, []any{"developer", "=", []any{"id", "=", d}})
		} else {
			preds = append(preds, []any{"developer", "=", []any{"search", "=", d}})
		}
	}
	anyOf(preds)
	switch len(and) {
	case 0:
		return nil, missing
	case 1:
		return and[0], missing
	}
	return append([]any{"and"}, and...), missing
}
