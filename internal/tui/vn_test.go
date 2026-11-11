package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/evansunde/anisync/internal/daemon"
	"github.com/evansunde/anisync/internal/netx"
	"github.com/evansunde/anisync/internal/store"
	"github.com/evansunde/anisync/internal/vndb"
	"github.com/evansunde/anisync/internal/vnlist"
)

func vnModel(w, h int, withCovers bool) *model {
	m := testModel(w, h, withCovers)
	m.vs.token, m.vs.user = "tok", "u1"
	m.Update(key("v"))
	l := &vnlist.List{Fetched: time.Now().Add(-3 * time.Hour), User: "u1",
		Labels: []vndb.Label{{ID: 1, Label: "Playing"}, {ID: 2, Label: "Finished"}, {ID: 3, Label: "Stalled"}, {ID: 4, Label: "Dropped"},
			{ID: 5, Label: "Wishlist"}, {ID: 6, Label: "Blacklist"}, {ID: 7, Label: "Voted"}, {ID: 10, Label: "Favourites"}},
		Entries: []*vnlist.Entry{
			{ID: "v20424", Title: "Summer Pockets", Rating: 84.6, Votes: 4000, Length: 5, LengthMin: 3245, Devs: []string{"Key"},
				Released: "2018-06-29", Cover: "https://t.vndb.org/cv/08/75908.jpg", OnList: true, Labels: []int{1, 10},
				Started: "2026-10-01", Playtime: 45000, Updated: 3},
			{ID: "v4936", Title: "Shiei no Sona-Nyl", Rating: 81.4, Length: 3, OnList: true, Labels: []int{2, 7}, Vote: 70,
				Started: "2025-10-24", Finished: "2026-01-03", Updated: 2},
			{ID: "v17", Title: "Ever17", Rating: 87, OnList: true, Labels: []int{5}, Updated: 1, Sexual: 1.2, Cover: "https://x/e.jpg"},
		}}
	m.Update(vnListMsg{list: l, synced: true})
	return m
}

func TestVNModeToggle(t *testing.T) {
	m := testModel(140, 32, false)
	m.Update(key("v"))
	if v := checkView(t, m); !strings.Contains(v, "not logged in to VNDB") {
		t.Errorf("no token: login hint missing:\n%s", v)
	}
	m.Update(key("v"))
	v := checkView(t, m)
	if m.vn || !strings.Contains(v, "Anime") || !strings.Contains(v, "VN") {
		t.Fatalf("mode switch missing:\n%s", v)
	}
	m.Update(key("v"))
	if !m.vn || !m.vs.started || m.tab != tabList {
		t.Fatalf("v should switch to VN mode keeping the tab (vn=%v started=%v tab=%v)", m.vn, m.vs.started, m.tab)
	}
	checkView(t, m)
	click(t, m, "Anime", tea.MouseButtonLeft)
	if m.vn {
		t.Fatal("clicking Anime should switch back")
	}
	click(t, m, "VN", tea.MouseButtonLeft)
	if !m.vn {
		t.Fatal("clicking VN should switch")
	}
}

func TestVNListSectionsAndRows(t *testing.T) {
	m := vnModel(150, 32, false)
	v := checkView(t, m)
	for _, want := range []string{"Playing 1", "Finished 1", "Wishlist 1", "Favourites 1", "Voted 1", "All 3", "Summer Pockets",
		"⏱12h30m", "8.46", "~54h", "synced 3h ago", "started 2026-10-01"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(v, "Blacklist") {
		t.Error("unused Blacklist label shown")
	}
	if strings.Contains(v, "Ever17") {
		t.Error("wishlist entry shown under Playing")
	}
	click(t, m, "Finished 1", tea.MouseButtonLeft)
	if m.vs.secID != vndb.Finished {
		t.Fatalf("section click → %d", m.vs.secID)
	}
	if v := checkView(t, m); !strings.Contains(v, "Shiei no Sona-Nyl") || !strings.Contains(v, "★ 7.0") {
		t.Errorf("finished row:\n%s", v)
	}
	m.Update(key("l"))
	m.Update(key("l"))
	m.Update(key("l"))
	m.Update(key("l"))
	if m.vs.secID != 10 {
		t.Fatalf("custom label section after Wishlist, got %d", m.vs.secID)
	}
	m.vs.secID = 0
	m.Update(key("/"))
	typeKeys(m, "key")
	if v := m.vnVisible(); len(v) != 1 || v[0].ID != "v20424" {
		t.Fatalf("filter by developer: %+v", v)
	}
	m.Update(key("esc"))
	m.Update(key("S"))
	m.Update(key("S"))
	if m.vs.sortMode != 2 || m.vnVisible()[0].Vote != 70 {
		t.Fatalf("sort by vote: %+v", m.vnVisible()[0])
	}
}

func TestVNEditsAreOptimistic(t *testing.T) {
	m := vnModel(140, 32, false)
	today := netx.Today()
	// label picker: f = Finished, replaces Playing, keeps the custom label
	m.Update(key("s"))
	if !strings.Contains(checkView(t, m), "Label:") {
		t.Fatal("label picker not shown")
	}
	m.Update(key("f"))
	e := m.vnEntry("v20424")
	if !e.Has(vndb.Finished) || e.Has(vndb.Playing) || !e.Has(10) || e.Finished != today || !e.Pending {
		t.Fatalf("finish: %+v", e)
	}
	// it left the Playing section; look at it under All
	m.vs.secID = 0
	m.vs.lSel = 0
	for i, r := range m.vnVisible() {
		if r.ID == "v20424" {
			m.vs.lSel = i
		}
	}
	m.Update(key("r"))
	typeKeys(m, "8.5")
	m.Update(key("enter"))
	if e.Vote != 85 || !e.Has(vndb.Voted) {
		t.Fatalf("vote: %+v", e)
	}
	m.Update(key("e"))
	typeKeys(m, "2024-01-02")
	m.Update(key("enter"))
	if e.Started != "2024-01-02" {
		t.Fatalf("started: %+v", e)
	}
	m.Update(key("E"))
	typeKeys(m, "-")
	m.Update(key("enter"))
	if e.Finished != "" {
		t.Fatalf("clear finished: %+v", e)
	}
	// custom label toggle (1 = first custom label)
	m.Update(key("s"))
	if !strings.Contains(checkView(t, m), "1 ● Favourites") {
		t.Fatal("custom label not offered")
	}
	m.Update(key("1"))
	if e.Has(10) {
		t.Fatalf("custom label toggle off: %+v", e.Labels)
	}
	// remove
	m.Update(key("s"))
	m.Update(key("x"))
	if m.modal != modalDelete {
		t.Fatal("x should ask")
	}
	m.Update(key("y"))
	if m.vnEntry("v20424") != nil {
		t.Fatal("not removed locally")
	}
	if !strings.Contains(m.msg, "") {
		t.Fatal()
	}
}

func vnSearchModel(w, h int, covers bool) *model {
	m := vnModel(w, h, covers)
	m.switchTab(tabSearch)
	res := vndb.Page{Results: []vndb.VN{
		{ID: "v20424", Title: "Summer Pockets", Released: "2018-06-29", Rating: 84.6, VoteCount: 4000, Length: 5, Image: &vndb.Image{URL: "https://t/a.jpg"},
			Developers: []vndb.Producer{{Name: "Key"}}},
		{ID: "v99", Title: "Spicy Game", Released: "2020-01-01", Rating: 70, Image: &vndb.Image{URL: "https://t/b.jpg", Sexual: 2}},
	}, More: true, Count: 120}
	m.Update(vnSearchMsg{seq: m.vs.seq, page: 1, res: res})
	return m
}

func TestVNSearchAndFilterEditor(t *testing.T) {
	m := vnSearchModel(140, 40, false)
	v := checkView(t, m)
	for _, want := range []string{"Summer Pockets", "2018", "8.46", "Playing", "showing 2 of 120", "⇅ Highest rated", "Votes: ≥ 500 votes", "s add/label"} {
		if !strings.Contains(v, want) {
			t.Errorf("search view missing %q", want)
		}
	}
	m.Update(key("f"))
	if m.panel == nil || len(m.spec().fields()) != len(vnFields) {
		t.Fatal("f should open the VN filter editor")
	}
	if !strings.Contains(checkView(t, m), "Developer") {
		t.Fatal("VN fields not shown")
	}
	m.panel.sel = vLength
	m.Update(key("enter"))
	typeKeys(m, "long")
	m.Update(key(" "))
	m.Update(key("esc"))
	m.panel.sel = vYear
	m.Update(key("enter"))
	typeKeys(m, ">2015")
	m.Update(key("enter"))
	m.panel.sel = vDev
	m.Update(key("enter"))
	typeKeys(m, "key, 07th Expansion")
	m.Update(key("enter"))
	m.panel.sel = vSort
	m.Update(key("l"))
	m.Update(key("esc"))
	f := m.vs.f
	if len(f.Lengths) != 1 || f.Lengths[0] != "4" || f.YearFrom != 2016 || len(f.Devs) != 2 || f.Sort != "votecount" {
		t.Fatalf("filter %+v", f)
	}
	q := m.vs.input.Value()
	for _, want := range []string{"len:long", "y:>2015", `dev:"07th Expansion"`, "sort:votes"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q missing %q", q, want)
		}
	}
	// typed query → filter
	m.Update(key("/"))
	m.vs.input.SetValue(`ever17 t:"male protagonist" lang:en`)
	m.Update(key("enter"))
	if m.vs.f.Search != "ever17" || m.vs.f.Langs[0] != "en" || m.vs.f.Tags[0] != "male protagonist" {
		t.Fatalf("typed: %+v", m.vs.f)
	}
	if !strings.Contains(checkView(t, m), "+#male protagonist") {
		t.Error("tag chip missing")
	}
	// adding from search: s → label picker → w
	m.Update(vnSearchMsg{seq: m.vs.seq, page: 1, res: vndb.Page{Results: []vndb.VN{{ID: "v17", Title: "Ever17"}, {ID: "v5", Title: "New One"}}}})
	m.vs.sSel = 1
	m.Update(key("s"))
	m.Update(key("w"))
	if e := m.vnEntry("v5"); e == nil || !e.Has(vndb.Wishlist) || !e.Pending {
		t.Fatalf("add to wishlist: %+v", e)
	}
}

func TestVNDetailLinksAndBBCode(t *testing.T) {
	m := vnSearchModel(160, 50, true)
	d := &vndb.VN{ID: "v20424", Title: "Summer Pockets", AltTitle: "サマーポケッツ", Released: "2018-06-29", Rating: 84.6, VoteCount: 4000,
		Length: 5, LengthMinutes: 3245, Developers: []vndb.Producer{{Name: "Key"}}, Platforms: []string{"win", "swi"},
		Image:       &vndb.Image{URL: "https://t/a.jpg"},
		Description: "An island [url=https://en.wikipedia.org/wiki/X]summer[/url].\n[spoiler]the twist[/spoiler]\n\n\n[b]End[/b]",
		Tags: []vndb.Tag{{ID: "g424", Name: "Island", Rating: 3}, {ID: "g349", Name: "Time Loop", Rating: 3, Spoiler: 2},
			{ID: "g1", Name: "Low Tag", Rating: 0.5}, {ID: "g23", Name: "Sexual Content", Rating: 2.5, Category: "ero"}},
		Relations: []vndb.Relation{{ID: "v69324", Title: "Summer Pockets Sunnyside Stories", Relation: "side", Official: true}}}
	m.Update(vnDetailMsg{key: (&vnrow{ID: "v20424"}).Key(), v: d})
	v := checkView(t, m)
	for _, want := range []string{"サマーポケッツ", "Very long (54h)", "by Key", "★ 8.46", "4.0k votes", "Windows, Switch", "Playing", "Favourites",
		"⏱ 12h30m", "Island 3.0", "An island summer.", "End", "Related", "side story", "Sunnyside Stories"} {
		if !strings.Contains(v, want) {
			t.Errorf("detail missing %q", want)
		}
	}
	for _, bad := range []string{"Time Loop", "Low Tag", "Sexual Content", "[url", "twist", "[b]"} {
		if strings.Contains(v, bad) {
			t.Errorf("detail shows %q", bad)
		}
	}
	// relation link opens in the peek stack, esc goes back
	click(t, m, "Sunnyside Stories", tea.MouseButtonLeft)
	if len(m.vs.peek) != 1 || m.vnCurrent().ID != "v69324" {
		t.Fatalf("relation didn't open: %+v", m.vs.peek)
	}
	if !strings.Contains(checkView(t, m), "← back") {
		t.Error("breadcrumb missing")
	}
	m.Update(key("esc"))
	if len(m.vs.peek) != 0 || m.vnCurrent().ID != "v20424" {
		t.Fatal("esc should go back")
	}
	// tag link starts a VN search with it
	click(t, m, "Island", tea.MouseButtonLeft)
	if m.tab != tabSearch || len(m.vs.f.Tags) != 1 || m.vs.f.Tags[0] != "Island" || m.vs.vocab.TagID("island") != "g424" {
		t.Fatalf("tag click → %+v", m.vs.f)
	}
}

func TestVNCoverSafety(t *testing.T) {
	m := vnSearchModel(160, 44, true)
	m.vs.sSel = 1 // sexual 2
	m.cov.state[(&vnrow{ID: "v99"}).Key()] = coverReady
	if !strings.Contains(checkView(t, m), "hidden") || strings.Contains(m.View(), "\U0010EEEE") {
		t.Fatal("explicit cover should be hidden in safe mode")
	}
	if m.vnCover(m.vs.results[1]) != nil {
		t.Fatal("explicit cover must not be fetched in safe mode")
	}
	m.vs.covers = "all"
	if !strings.Contains(m.View(), "\U0010EEEE") {
		t.Fatal("covers = all shows it")
	}
	m.vs.covers = "off"
	if strings.Contains(checkView(t, m), "hidden") || strings.Contains(m.View(), "\U0010EEEE") {
		t.Fatal("covers = off shows nothing")
	}
	m.vs.covers = "safe"
	m.vs.sSel = 0
	if m.vnCover(m.vs.results[0]) == nil {
		t.Fatal("safe cover should load")
	}
}

func TestVNNowTab(t *testing.T) {
	m := vnModel(140, 36, false)
	m.st = &daemon.Status{Services: map[string]string{"vndb": "tester"},
		VNNow: []daemon.VNNow{{Folder: "[Key] Summer_Pockets v1.02 (ENG)", Key: "v:summer pockets", Session: 42 * time.Minute,
			Playtime: 13 * time.Hour, Title: "Summer Pockets", VNDBID: "v20424"}},
		Queue: []*store.Item{{Kind: "vnset", TargetTitle: "Ever17"}, {Kind: "anime", Title: "Alpha"}}}
	m.pending = []*store.Pending{{Key: "v:mystery vn", Kind: "vn", Query: "Mystery_VN"}, {Key: "a:x", Kind: "anime", Query: "Anime X"}}
	m.switchTab(tabNow)
	v := checkView(t, m)
	for _, want := range []string{"Playing now", "Summer_Pockets", "finish", "change match", "match     Summer Pockets v20424", "Playing",
		"0h42m this session", "13h00m total", "Needs a match (1)", "Mystery_VN", "Queued VNDB updates (1)", "Ever17", "2 Now (1)"} {
		if !strings.Contains(v, want) {
			t.Errorf("now tab missing %q", want)
		}
	}
	if strings.Contains(v, "Anime X") || strings.Contains(v, "Alpha") {
		t.Error("anime items shown in VN mode")
	}
	if r := m.vnCurrent(); r == nil || r.ID != "v20424" || r.Playtime != 45000 {
		t.Fatalf("current on now tab: %+v", r)
	}
	m.Update(key("f"))
	if m.modal != modalFinish {
		t.Fatal("f should ask for a vote")
	}
	typeKeys(m, "9")
	if _, cmd := m.Update(key("enter")); cmd == nil {
		t.Fatal("finish should send vn-finish")
	}
	m.Update(key("m"))
	if m.tab != tabSearch || m.resolving == nil || m.resolving.Key != "v:summer pockets" || m.vs.f.Search != "Summer Pockets" {
		t.Fatalf("change match: tab=%v resolving=%+v f=%+v", m.tab, m.resolving, m.vs.f)
	}
	m.Update(vnSearchMsg{seq: m.vs.seq, page: 1, res: vndb.Page{Results: []vndb.VN{{ID: "v20424", Title: "Summer Pockets"}}}})
	if !strings.Contains(checkView(t, m), "Pick the visual novel for") {
		t.Fatal("resolve banner")
	}
	if _, cmd := m.Update(key("enter")); cmd == nil || m.resolving != nil || m.tab != tabNow {
		t.Fatal("enter should map and return to Now")
	}
	// the pending VN from anime mode's Now tab switches to VN mode
	m.Update(key("v"))
	m.nSel = 0
	m.nowKey("enter")
	if !m.vn || m.resolving == nil || m.resolving.Key != "v:mystery vn" || m.vs.f.Search != "Mystery VN" {
		t.Fatalf("pending VN from anime mode: vn=%v %+v %+v", m.vn, m.resolving, m.vs.f)
	}
}

func TestVNAllTabsFit(t *testing.T) {
	for _, w := range []int{60, 80, 100, 160} {
		m := vnSearchModel(w, 30, true)
		m.st = &daemon.Status{Services: map[string]string{}, VNNow: []daemon.VNNow{{Folder: strings.Repeat("Very Long Folder Name ", 6), Key: "v:x", Pending: true}}}
		for _, tb := range []tabID{tabList, tabNow, tabSearch} {
			m.switchTab(tb)
			checkView(t, m)
			m.Update(key("enter"))
			checkView(t, m)
			m.Update(key("esc"))
		}
		m.Update(key("f"))
		checkView(t, m)
		m.Update(key("esc"))
		m.Update(key("?"))
		if v := checkView(t, m); !strings.Contains(v, "VNDB") {
			t.Error("VN help not shown")
		}
		m.Update(key("esc"))
		m.Update(key("s"))
		checkView(t, m)
	}
}
