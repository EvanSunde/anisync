//go:build live

package vndb_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/evansunde/anisync/internal/query"
	"github.com/evansunde/anisync/internal/vndb"
)

// go test -tags live ./internal/vndb/ -v   (hits the real VNDB API)
func TestLiveVNDB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := &vndb.Client{}

	tag, err := c.FindTag(ctx, "male protagonist")
	if err != nil || tag == nil || tag.ID != "g133" {
		t.Fatalf("FindTag: %+v %v", tag, err)
	}
	f, w := query.ParseVN(`summer pockets t:"male protagonist" -t:nukige len:long,vlong y:2015-2020 rating>7 lang:en p:win dev:key`, nil)
	if len(w) > 0 {
		t.Fatal(w)
	}
	ids := map[string]string{"male protagonist": "g133", "nukige": "g1351"}
	filters, missing := query.VNFilters(f, func(n string) string { return ids[strings.ToLower(n)] })
	if len(missing) > 0 {
		t.Fatal(missing)
	}
	s, rev := query.VNSortParams(f)
	p, err := c.Browse(ctx, vndb.Query{Filters: filters, Sort: s, Reverse: rev})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Results) == 0 || p.Results[0].ID != "v20424" || p.Count == 0 {
		t.Fatalf("browse: %+v", p)
	}
	d, err := c.Detail(ctx, "v20424")
	if err != nil {
		t.Fatal(err)
	}
	if d.Image == nil || d.Image.URL == "" || len(d.Tags) == 0 || len(d.Relations) == 0 || d.Description == "" || len(d.Developers) == 0 {
		t.Fatalf("detail incomplete: %+v", d)
	}
	if desc := vndb.PlainDescription(d.Description); strings.Contains(desc, "[url") {
		t.Fatalf("bbcode left: %q", desc)
	}
	tags, more, err := c.Tags(ctx, 1)
	if err != nil || len(tags) < 90 || !more {
		t.Fatalf("tags: %d %v %v", len(tags), more, err)
	}
	labels, err := c.Labels(ctx, "u2")
	if err != nil || len(labels) < 5 {
		t.Fatalf("labels: %+v %v", labels, err)
	}
	t.Logf("ok: %d results, %s has %d tags, %d labels for u2", p.Count, d.Title, len(d.Tags), len(labels))
}
