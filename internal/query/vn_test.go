package query

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

var vnVocab = NewVNVocab([]VNTag{{"g133", "Male Protagonist"}, {"g96", "Romance"}, {"g235", "No Sexual Content"}})

func TestParseVN(t *testing.T) {
	f, w := ParseVN(`summer pockets t:"male protagonist" -t:romance len:long,vlong y:2015-2020 rating>7.5 votes>=100 lang:en,ja ol:ja p:win,lin dev:key dev:"type-moon" sort:rating`, vnVocab)
	if len(w) != 0 {
		t.Fatalf("warnings: %v", w)
	}
	want := VNFilter{Search: "summer pockets", Tags: []string{"Male Protagonist"}, NotTags: []string{"Romance"}, Lengths: []string{"4", "5"},
		YearFrom: 2015, YearTo: 2020, RatingMin: 76, VotesMin: 100, Langs: []string{"en", "ja"}, OLang: "ja",
		Platforms: []string{"win", "lin"}, Devs: []string{"key", "type-moon"}, Sort: "rating"}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("got  %+v\nwant %+v", f, want)
	}
	f, w = ParseVN(`len:huge p:gameboy rating:abc Fate/stay night: Heaven's Feel t:"Unknown Tag" sort:popular`, vnVocab)
	if len(w) != 3 {
		t.Errorf("expected 3 warnings, got %v", w)
	}
	if f.Search != "Fate/stay night: Heaven's Feel" || f.Tags[0] != "Unknown Tag" || f.Sort != "votecount" {
		t.Errorf("%+v", f)
	}
	f, _ = ParseVN(`y:<2010 rating:70-90 len:3 lang:chinese`, nil)
	if f.YearTo != 2009 || f.RatingMin != 70 || f.RatingMax != 90 || f.Lengths[0] != "3" || f.Langs[0] != "zh-Hans" {
		t.Errorf("%+v", f)
	}
}

func TestFormatVNRoundTrip(t *testing.T) {
	cases := []VNFilter{
		{Search: "ever17"},
		{Tags: []string{"Male Protagonist", "g42"}, NotTags: []string{"No Sexual Content"}, Lengths: []string{"1", "3"}, Sort: "released_asc"},
		{Year: 2019, RatingMin: 80, VotesMin: 500, Langs: []string{"zh-Hant", "pt-br"}, OLang: "ko", Sort: "title"},
		{YearFrom: 2010, RatingMax: 59, Platforms: []string{"swi", "ps5"}, Devs: []string{"p24", "Liar-soft", "07th Expansion"}},
		{YearTo: 2005, Sort: "votecount_asc", Search: "a b"},
	}
	for _, c := range cases {
		s := FormatVN(c)
		got, w := ParseVN(s, vnVocab)
		if len(w) > 0 {
			t.Errorf("%q: warnings %v", s, w)
		}
		if !reflect.DeepEqual(got, c) {
			t.Errorf("round trip %q\n got  %+v\n want %+v", s, got, c)
		}
	}
}

func TestVNFilters(t *testing.T) {
	f, _ := ParseVN(`sp t:romance -t:"no sexual content" t:mystery len:short,medium y:2018 rating>=70 votes:50 lang:en p:win dev:key dev:p29`, vnVocab)
	got, missing := VNFilters(f, vnVocab.TagID)
	if !reflect.DeepEqual(missing, []string{"mystery"}) {
		t.Errorf("missing %v", missing)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(got)
	b := bytes.TrimSpace(buf.Bytes())
	want := `["and",["search","=","sp"],["tag","=","g96"],["tag","!=","g235"],["or",["length","=",2],["length","=",3]],` +
		`["released",">=","2018-01-01"],["released","<","2019-01-01"],["rating",">=",70],["votecount",">=",50],` +
		`["lang","=","en"],["platform","=","win"],["or",["developer","=",["search","=","key"]],["developer","=",["id","=","p29"]]]]`
	if string(b) != want {
		t.Errorf("filters\n got  %s\n want %s", b, want)
	}
	if one, _ := VNFilters(VNFilter{Search: "x"}, vnVocab.TagID); !reflect.DeepEqual(one, []any{"search", "=", "x"}) {
		t.Errorf("single predicate: %v", one)
	}
	if none, _ := VNFilters(VNFilter{}, vnVocab.TagID); none != nil {
		t.Errorf("empty: %v", none)
	}
	if s, r := VNSortParams(VNFilter{}); s != "votecount" || !r {
		t.Errorf("default sort without text: %s %v", s, r)
	}
	if s, r := VNSortParams(VNFilter{Search: "x"}); s != "searchrank" || r {
		t.Errorf("default sort with text: %s %v", s, r)
	}
	if s, r := VNSortParams(VNFilter{Sort: "released_asc"}); s != "released" || r {
		t.Errorf("oldest: %s %v", s, r)
	}
}
