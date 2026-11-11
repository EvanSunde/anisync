package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/query"
)

// animeSpec is the AniList search filter (m.f) for the filter editor.
type animeSpec struct{ m *model }

var popChoices = []query.Choice{{Value: "1000", Label: "≥ 1k users"}, {Value: "10000", Label: "≥ 10k users"},
	{Value: "50000", Label: "≥ 50k users"}, {Value: "100000", Label: "≥ 100k users"}, {Value: "250000", Label: "≥ 250k users"},
	{Value: "500000", Label: "≥ 500k users"}}

var yesNo = []query.Choice{{Value: "yes", Label: "Yes"}, {Value: "no", Label: "No"}}

var fields = []fieldDef{
	{label: "Sort", kind: kEnum, choices: query.Sorts},
	{label: "Format", kind: kMulti, choices: query.Formats},
	{label: "Status", kind: kEnum, choices: query.Statuses, any: true},
	{label: "Season", kind: kEnum, choices: query.Seasons, any: true},
	{label: "Year", kind: kRange, token: "y", placeholder: "2023  2015-2020  >2018"},
	{label: "Score", kind: kRange, token: "score", placeholder: "75-100  >7.5"},
	{label: "Episodes", kind: kRange, token: "eps", placeholder: "<13  12  24-30"},
	{label: "Popularity", kind: kEnum, choices: popChoices, any: true},
	{label: "Genres", kind: kTri},
	{label: "Tags", kind: kTri},
	{label: "Source", kind: kMulti, choices: query.Sources},
	{label: "Country", kind: kEnum, choices: query.Countries, any: true},
	{label: "On my list", kind: kEnum, choices: yesNo, any: true},
	{label: "Adult", kind: kEnum, choices: yesNo[:1], any: true},
}

const (
	fSort = iota
	fFormat
	fStatus
	fSeason
	fYear
	fScore
	fEps
	fPop
	fGenres
	fTags
	fSource
	fCountry
	fList
	fAdult
)

func (s animeSpec) fields() []fieldDef { return fields }
func (s animeSpec) sortField() int     { return fSort }
func (s animeSpec) tagField() int      { return fTags }
func (s animeSpec) firstFilter() int   { return fStatus }
func (s animeSpec) format() string     { return query.Format(s.m.f) }
func (s animeSpec) empty() bool        { return s.m.f.Empty() }

func (s animeSpec) clearFilters() {
	text, sort := s.m.f.Search, s.m.f.Sort
	s.m.f = anilist.Filter{Search: text, Sort: sort}
}

func (s animeSpec) enumGet(i int) string {
	f := &s.m.f
	switch i {
	case fSort:
		if len(f.Sort) > 0 {
			return f.Sort[0]
		}
		if f.Search != "" {
			return "SEARCH_MATCH"
		}
		return "POPULARITY_DESC"
	case fStatus:
		return f.Status
	case fSeason:
		return f.Season
	case fPop:
		if f.PopMin > 0 {
			return strconv.Itoa(f.PopMin)
		}
	case fCountry:
		return f.Country
	case fList:
		if f.OnList != nil {
			return map[bool]string{true: "yes", false: "no"}[*f.OnList]
		}
	case fAdult:
		if f.Adult {
			return "yes"
		}
	}
	return ""
}

func (s animeSpec) enumSet(i int, v string) {
	f := &s.m.f
	switch i {
	case fSort:
		if v == "" {
			f.Sort = nil
		} else {
			f.Sort = []string{v}
		}
	case fStatus:
		f.Status = v
	case fSeason:
		f.Season = v
	case fPop:
		f.PopMin, _ = strconv.Atoi(v)
	case fCountry:
		f.Country = v
	case fList:
		if v == "" {
			f.OnList = nil
		} else {
			b := v == "yes"
			f.OnList = &b
		}
	case fAdult:
		f.Adult = v == "yes"
	}
}

func (s animeSpec) multiPtr(i int) *[]string {
	switch i {
	case fFormat:
		return &s.m.f.Formats
	case fSource:
		return &s.m.f.Sources
	}
	return nil
}

func (s animeSpec) triPtrs(i int) (inc, exc *[]string) {
	if i == fGenres {
		return &s.m.f.Genres, &s.m.f.NotGenres
	}
	return &s.m.f.Tags, &s.m.f.NotTags
}

func (s animeSpec) pickOptions(i int) []string {
	if s.m.vocab == nil {
		return nil
	}
	if i == fGenres {
		return s.m.vocab.GenreList()
	}
	return s.m.vocab.TagList()
}

func (s animeSpec) rangeLabel(i int) string {
	f := &s.m.f
	switch i {
	case fYear:
		if f.Year > 0 {
			return fmt.Sprint(f.Year)
		}
		return rangeLabel(f.YearFrom, f.YearTo, "")
	case fScore:
		return rangeLabel(f.ScoreMin, f.ScoreMax, "%")
	case fEps:
		return rangeLabel(f.EpsMin, f.EpsMax, " eps")
	}
	return ""
}

func (s animeSpec) clearRange(i int) {
	f := &s.m.f
	switch i {
	case fYear:
		f.Year, f.YearFrom, f.YearTo = 0, 0, 0
	case fScore:
		f.ScoreMin, f.ScoreMax = 0, 0
	case fEps:
		f.EpsMin, f.EpsMax = 0, 0
	}
}

func (s animeSpec) rangeText(i int) string { return tokenValue(s.format(), fields[i].token) }

func (s animeSpec) setRange(i int, text string) error {
	s.clearRange(i)
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	nf, warns := query.Parse(fields[i].token+":"+text, s.m.vocab)
	if len(warns) > 0 {
		return fmt.Errorf("%s", warns[0])
	}
	f := &s.m.f
	switch i {
	case fYear:
		f.Year, f.YearFrom, f.YearTo = nf.Year, nf.YearFrom, nf.YearTo
	case fScore:
		f.ScoreMin, f.ScoreMax = nf.ScoreMin, nf.ScoreMax
	case fEps:
		f.EpsMin, f.EpsMax = nf.EpsMin, nf.EpsMax
	}
	return nil
}

func (s animeSpec) cycleSort(d int) string {
	cur := s.enumGet(fSort)
	i := 0
	for j, c := range query.Sorts {
		if c.Value == cur {
			i = j
		}
	}
	i = (i + d + len(query.Sorts)) % len(query.Sorts)
	s.m.f.Sort = []string{query.Sorts[i].Value}
	return query.Sorts[i].Label
}

// tokenValue returns the value of the first "key:value" token in a query.
func tokenValue(q, key string) string {
	pre := key + ":"
	for _, tok := range strings.Fields(q) {
		if strings.HasPrefix(tok, pre) {
			return strings.TrimPrefix(tok, pre)
		}
	}
	return ""
}

func rangeLabel(lo, hi int, unit string) string {
	switch {
	case lo > 0 && hi > 0 && lo == hi:
		return fmt.Sprint(lo) + unit
	case lo > 0 && hi > 0:
		return fmt.Sprintf("%d–%d%s", lo, hi, unit)
	case lo > 0:
		return fmt.Sprintf("≥ %d%s", lo, unit)
	case hi > 0:
		return fmt.Sprintf("≤ %d%s", hi, unit)
	}
	return ""
}
