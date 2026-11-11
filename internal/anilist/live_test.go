//go:build live

package anilist

import (
	"context"
	"testing"
)

func TestLiveBrowse(t *testing.T) {
	c := &Client{}
	ctx := context.Background()
	p, err := c.Browse(ctx, Filter{Genres: []string{"Action"}, Tags: []string{"Isekai"}, YearFrom: 2018, YearTo: 2024,
		ScoreMin: 75, EpsMax: 13, Formats: []string{"TV"}, Sort: []string{"SCORE_DESC"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("total=%d next=%v", p.Total, p.HasNext)
	for _, m := range p.Media[:min(5, len(p.Media))] {
		t.Logf("%-40s %s %d eps=%d score=%d %v rank=%v", m.Title.Best(), m.Format, m.SeasonYear, m.Episodes, m.AverageScore, m.Genres, m.Rankings[:min(1, len(m.Rankings))])
		if m.AverageScore < 75 || m.Episodes > 13 || m.SeasonYear < 2018 || m.SeasonYear > 2024 {
			t.Errorf("filter violated: %+v", m.Title)
		}
	}
	d, err := c.Detail(ctx, 154587)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("detail: %s studio=%v src=%s cover=%s tags=%d rel=%d recs=%d", d.Title.Best(), d.Studios.Nodes, d.Source, d.CoverImage.Large, len(d.Tags), len(d.Relations.Edges), len(d.Recommendations.Nodes))
	t.Logf("desc: %.120s", PlainDescription(d.Description))
	g, tags, err := c.Collections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("genres=%d tags=%d", len(g), len(tags))
	p, err = c.Browse(ctx, Filter{Search: "frieren"}, 1)
	if err != nil || len(p.Media) == 0 {
		t.Fatal(err)
	}
	t.Logf("search frieren → %s", p.Media[0].Title.Best())
}
