package media

import "testing"

func TestParsePath(t *testing.T) {
	cases := []struct {
		in      string
		title   string
		season  int
		episode int
		skip    bool
	}{
		{"/a/[SubsPlease] Sousou no Frieren - 05 (1080p) [ABCD1234].mkv", "Sousou no Frieren", 0, 5, false},
		{"/a/Mushoku Tensei S02E05 1080p WEB H.264-VARYG.mkv", "Mushoku Tensei", 2, 5, false},
		{"/anime/Bocchi the Rock!/05.mkv", "Bocchi the Rock!", 0, 5, false},
		{"/anime/Bocchi the Rock!/Episode 05.mkv", "Bocchi the Rock!", 0, 5, false},
		{"/anime/[Group] Spy x Family (BD 1080p)/Season 2/03.mkv", "Spy x Family", 2, 3, false},
		{"/a/Kaguya-sama wa Kokurasetai Season 2 - 04.mkv", "Kaguya-sama wa Kokurasetai", 2, 4, false},
		{"/a/One Piece - 1100 [1080p].mkv", "One Piece", 0, 1100, false},
		{"/a/[Group] Show - 12.5 [1080p].mkv", "Show", 0, 12, true},
		{"/a/[Group] Show - NCOP 01 [1080p].mkv", "Show", 0, 1, true},
		{"/movies/Kimi no Na wa (2016) [1080p].mkv", "Kimi no Na wa", 0, 0, false},
	}
	for _, c := range cases {
		p := ParsePath(c.in)
		if c.skip {
			if !p.Skip {
				t.Errorf("%s: expected skip, got %+v", c.in, p)
			}
			continue
		}
		if p.Title != c.title || p.Season != c.season || p.Episode != c.episode || p.Skip != c.skip {
			t.Errorf("%s\n got  %+v\n want title=%q s=%d e=%d skip=%v", c.in, p, c.title, c.season, c.episode, c.skip)
		}
	}
}

func TestParseTitle(t *testing.T) {
	cases := []struct {
		in, title string
		ep        int
	}{
		{"Sousou no Frieren Episode 5", "Sousou no Frieren", 5},
		{"Sousou no Frieren - 05", "Sousou no Frieren", 5},
		{"Dandadan Ep. 12", "Dandadan", 12},
	}
	for _, c := range cases {
		p := ParseTitle(c.in)
		if p.Title != c.title || p.Episode != c.ep {
			t.Errorf("%q: got %+v", c.in, p)
		}
	}
}

func TestSimilarity(t *testing.T) {
	if Similarity("Re:Zero", "re zero") != 1 {
		t.Fatal("normalize")
	}
	if s := Similarity("Sousou no Frieren", "Sousou no Frieren 2nd Season"); s < 0.6 || s >= 1 {
		t.Fatalf("partial %v", s)
	}
	if s := Similarity("Bocchi the Rock", "Naruto"); s > 0.2 {
		t.Fatalf("unrelated %v", s)
	}
}

func TestCleanVNFolder(t *testing.T) {
	if got := CleanVNFolder("[Key] Summer_Pockets v1.02 (ENG)"); got != "Summer Pockets" {
		t.Fatalf("got %q", got)
	}
}
