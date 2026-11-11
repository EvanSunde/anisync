package query

import (
	"reflect"
	"testing"

	"github.com/evansunde/anisync/internal/anilist"
)

var vocab = NewVocab([]string{"Action", "Drama", "Ecchi", "Romance", "Slice of Life"}, []string{"Isekai", "Time Skip", "Harem"})

func TestParse(t *testing.T) {
	f, w := Parse(`isekai g:action,romance -g:ecchi t:"time skip" -t:harem f:tv,ona y:2018-2024 score>75 eps<13 sort:score st:finished`, vocab)
	if len(w) != 0 {
		t.Fatalf("warnings: %v", w)
	}
	if f.Search != "isekai" {
		t.Errorf("search %q", f.Search)
	}
	if !reflect.DeepEqual(f.Genres, []string{"Action", "Romance"}) || !reflect.DeepEqual(f.NotGenres, []string{"Ecchi"}) {
		t.Errorf("genres %v / %v", f.Genres, f.NotGenres)
	}
	if !reflect.DeepEqual(f.Tags, []string{"Time Skip"}) || !reflect.DeepEqual(f.NotTags, []string{"Harem"}) {
		t.Errorf("tags %v / %v", f.Tags, f.NotTags)
	}
	if !reflect.DeepEqual(f.Formats, []string{"TV", "ONA"}) {
		t.Errorf("formats %v", f.Formats)
	}
	if f.YearFrom != 2018 || f.YearTo != 2024 || f.ScoreMin != 76 || f.EpsMax != 12 || f.Status != "FINISHED" {
		t.Errorf("ranges %+v", f)
	}
	if !reflect.DeepEqual(f.Sort, []string{"SCORE_DESC"}) {
		t.Errorf("sort %v", f.Sort)
	}
}

func TestParseMisc(t *testing.T) {
	f, w := Parse(`g:isekai t:drama g:"slice of life" s:fall y:2023 list:no score:>8 src:manga,ln c:cn pop>50k`, vocab)
	if len(w) != 0 {
		t.Fatalf("warnings: %v", w)
	}
	if !reflect.DeepEqual(f.Tags, []string{"Isekai"}) || !reflect.DeepEqual(f.Genres, []string{"Drama", "Slice of Life"}) {
		t.Errorf("cross lookup: genres=%v tags=%v", f.Genres, f.Tags)
	}
	if f.Season != "FALL" || f.Year != 2023 || f.OnList == nil || *f.OnList || f.ScoreMin != 81 {
		t.Errorf("%+v", f)
	}
	if !reflect.DeepEqual(f.Sources, []string{"MANGA", "LIGHT_NOVEL"}) || f.Country != "CN" || f.PopMin != 50001 {
		t.Errorf("src/country/pop: %+v", f)
	}
	f, w = Parse(`g:nonsense y:abc Re:Zero`, vocab)
	if len(w) != 2 {
		t.Errorf("expected 2 warnings, got %v", w)
	}
	if f.Search != "Re:Zero" {
		t.Errorf("title with colon kept as text: %q", f.Search)
	}
	f, _ = Parse(`"steins gate" eps:24 y:<2010 score:7.5-9`, vocab)
	if f.Search != "steins gate" || f.EpsMin != 24 || f.EpsMax != 24 || f.YearTo != 2009 || f.ScoreMin != 75 || f.ScoreMax != 90 {
		t.Errorf("%+v", f)
	}
	f, _ = Parse(`sort:rank y:2015-`, vocab)
	if f.Sort[0] != "SCORE_DESC" || f.YearFrom != 2015 || f.YearTo != 0 {
		t.Errorf("%+v", f)
	}
}

// Format must round-trip through Parse so the filter editor and the typed
// query never disagree.
func TestFormatRoundTrip(t *testing.T) {
	no := false
	cases := []anilist.Filter{
		{Search: "frieren"},
		{Genres: []string{"Action", "Slice of Life"}, NotGenres: []string{"Ecchi"}, Tags: []string{"Time Skip"}, NotTags: []string{"Harem"}},
		{Formats: []string{"TV", "TV_SHORT"}, Season: "FALL", Year: 2023, Status: "RELEASING", Sort: []string{"TRENDING_DESC"}},
		{YearFrom: 2015, YearTo: 2020, ScoreMin: 76, EpsMax: 12, PopMin: 10000, Sources: []string{"LIGHT_NOVEL"}, Country: "KR", OnList: &no},
		{YearFrom: 2019, ScoreMax: 59, EpsMin: 25, Adult: true, Sort: []string{"SCORE"}},
	}
	for _, c := range cases {
		s := Format(c)
		got, w := Parse(s, vocab)
		if len(w) > 0 {
			t.Errorf("%q: warnings %v", s, w)
		}
		if !reflect.DeepEqual(got, c) {
			t.Errorf("round trip %q\n got  %+v\n want %+v", s, got, c)
		}
	}
}

func TestInclusiveBounds(t *testing.T) {
	f, w := Parse(`score>=75 y:>=2018 eps<=12`, vocab)
	if len(w) > 0 || f.ScoreMin != 75 || f.YearFrom != 2018 || f.EpsMax != 12 {
		t.Fatalf("%+v %v", f, w)
	}
}
