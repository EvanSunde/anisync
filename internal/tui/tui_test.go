package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/daemon"
	"github.com/evansunde/anisync/internal/listcache"
	"github.com/evansunde/anisync/internal/mpris"
	"github.com/evansunde/anisync/internal/query"
	"github.com/evansunde/anisync/internal/store"
)

func testModel(w, h int, withCovers bool) *model {
	m := newModel()
	m.cov = &covers{enabled: withCovers, cols: 17, rows: 12, state: map[uint32]int{}}
	m.vocab = query.NewVocab([]string{"Action", "Drama", "Ecchi", "Fantasy"}, []string{"Isekai", "Time Skip"})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.st = &daemon.Status{}
	return m
}

func mk(id int, title string, eps, score int, status string, prog int) anilist.Media {
	md := anilist.Media{ID: id, IDMal: id + 1, Title: anilist.Title{Romaji: title, English: title + " EN"}, Format: "TV",
		Status: "FINISHED", Episodes: eps, SeasonYear: 2023, AverageScore: score, Genres: []string{"Action", "Drama"},
		Rankings: []anilist.Ranking{{Rank: 3, Type: "RATED", AllTime: true, Context: "highest rated all time"}}}
	if status != "" {
		md.MediaListEntry = &anilist.Entry{Status: status, Progress: prog, Score: 85}
	}
	return md
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeKeys(m *model, s string) {
	for _, r := range s {
		m.Update(key(string(r)))
	}
}

func checkView(t *testing.T, m *model) string {
	t.Helper()
	v := m.View()
	lines := strings.Split(v, "\n")
	if len(lines) != m.h {
		t.Fatalf("view has %d lines, want %d", len(lines), m.h)
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw > m.w {
			t.Fatalf("line %d is %d wide (> %d): %q", i, lw, m.w, ansi.Strip(l))
		}
	}
	return ansi.Strip(v)
}

// click finds the first hit whose screen text contains s and clicks it.
func click(t *testing.T, m *model, s string, btn tea.MouseButton) {
	t.Helper()
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	for _, h := range m.hits {
		if h.y >= len(lines) {
			continue
		}
		r := []rune(lines[h.y])
		seg := ""
		// hits are in cells; these test strings are ASCII/1-cell
		if h.x0 < len(r) {
			seg = string(r[h.x0:min(h.x1, len(r))])
		}
		if strings.Contains(seg, s) {
			cmd := m.mouse(tea.MouseMsg{X: h.x0, Y: h.y, Button: btn, Action: tea.MouseActionPress})
			drain(m, cmd)
			return
		}
	}
	t.Fatalf("no clickable %q", s)
}

// drain runs synchronous commands that don't hit the network/daemon.
func drain(m *model, cmd tea.Cmd) {
	_ = cmd
}

func listModel(w, h int) *model {
	m := testModel(w, h, false)
	l := &listcache.List{Fetched: time.Now().Add(-2 * time.Hour), Entries: []*listcache.Entry{
		{AnilistID: 1, Title: "Alpha", Status: "CURRENT", Progress: 3, Episodes: 12, Updated: 3, NextEp: 6, NextAt: time.Now().Add(26 * time.Hour).Unix()},
		{AnilistID: 2, Title: "Bravo", Status: "PLANNING", Episodes: 24, Updated: 2},
		{AnilistID: 3, Title: "Charlie", Status: "COMPLETED", Progress: 12, Episodes: 12, MyScore: 90, Updated: 1},
	}}
	m.Update(listMsg{list: l})
	return m
}

func TestListIsFirstAndWorksOffline(t *testing.T) {
	m := listModel(140, 32)
	if m.tab != tabList {
		t.Fatal("list should be the first tab")
	}
	m.Update(listMsg{err: "dial tcp: network is unreachable"}) // sync failed
	v := checkView(t, m)
	for _, want := range []string{"1 My list", "2 Now", "3 Search", "Watching 1", "Alpha", "3/12", "+2 new", "offline — local copy, synced 2h ago"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(v, "Bravo") {
		t.Error("planning entry shown in Watching")
	}
}

func TestListEditsAndSections(t *testing.T) {
	m := listModel(140, 32)
	m.Update(key("+"))
	if m.list[0].Progress != 4 || !m.list[0].Pending {
		t.Fatalf("optimistic +1: %+v", m.list[0])
	}
	if !strings.Contains(checkView(t, m), "⇅") {
		t.Error("pending marker missing")
	}
	m.Update(key("e"))
	typeKeys(m, "12")
	m.Update(key("enter"))
	if m.list[0].Status != "COMPLETED" {
		t.Fatalf("completion: %+v", m.list[0])
	}
	// click on a section tab
	click(t, m, "Completed", tea.MouseButtonLeft)
	if sections[m.section] != "COMPLETED" {
		t.Fatalf("section click → %q", sections[m.section])
	}
	if v := m.visibleList(); len(v) != 2 {
		t.Fatalf("completed now has %d", len(v))
	}
	// filter
	m.section = len(sections) - 1
	m.Update(key("/"))
	typeKeys(m, "bra")
	if v := m.visibleList(); len(v) != 1 || v[0].Title != "Bravo" {
		t.Fatalf("filter: %+v", v)
	}
}

func TestMouseWheelOneStepPerNotch(t *testing.T) {
	m := listModel(140, 32)
	m.section = len(sections) - 1
	checkView(t, m)
	// kitty sends a burst of 5 events per notch
	for i := 0; i < 5; i++ {
		m.mouse(tea.MouseMsg{X: 5, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	}
	if m.lSel != 1 {
		t.Fatalf("one notch moved %d rows", m.lSel)
	}
	// wheel over the detail pane scrolls the detail, not the list
	m.mouse(tea.MouseMsg{X: 130, Y: 10, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if m.lSel != 1 {
		t.Fatal("detail wheel moved the list")
	}
	m.lastWheel = time.Time{}
	m.mouse(tea.MouseMsg{X: 130, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.lSel != 1 || m.dScroll == 0 && len(m.details) > 0 {
		t.Fatal("detail wheel")
	}
}

func searchModel(w, h int) *model {
	m := testModel(w, h, true)
	m.tab = tabSearch
	res := anilist.Page{Media: []anilist.Media{mk(154587, "Sousou no Frieren", 28, 89, "CURRENT", 5), mk(2, "Dandadan", 12, 84, "", 0)}, Total: 2}
	m.Update(searchMsg{seq: m.seq, page: 1, res: res})
	return m
}

func TestSearchDetailWithCoverAndLinks(t *testing.T) {
	m := searchModel(150, 44)
	m.cov.state[154587] = coverReady
	d := mk(154587, "Sousou no Frieren", 28, 89, "CURRENT", 5)
	d.Description = "The adventure is over but life goes on.<br><br><i>(Source: Crunchyroll)</i>"
	d.Tags = []anilist.Tag{{Name: "Elf", Rank: 94}, {Name: "Spoilery", Rank: 99, Spoiler: true}}
	d.Relations.Edges = append(d.Relations.Edges, struct {
		RelationType string `json:"relationType"`
		Node         struct {
			ID       int           `json:"id"`
			Type     string        `json:"type"`
			Format   string        `json:"format"`
			Status   string        `json:"status"`
			Episodes int           `json:"episodes"`
			Title    anilist.Title `json:"title"`
		} `json:"node"`
	}{RelationType: "SEQUEL"})
	d.Relations.Edges[0].Node.ID, d.Relations.Edges[0].Node.Type, d.Relations.Edges[0].Node.Format = 182255, "ANIME", "TV"
	d.Relations.Edges[0].Node.Title = anilist.Title{Romaji: "Sousou no Frieren 2nd Season"}
	m.Update(detailMsg{key: 154587, m: &d})
	v := checkView(t, m)
	for _, want := range []string{"Sousou no Frieren", "Watching  5/28", "★ 89%", "#3", "genres", "Elf 94%", "The adventure is over", "Related", "2nd Season", "⇅ Trending"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q", want)
		}
	}
	if strings.Contains(v, "Spoilery") {
		t.Error("spoiler tag shown")
	}
	if !strings.Contains(m.View(), "\U0010EEEE") {
		t.Error("cover placeholders missing")
	}

	// keyboard: focus detail, walk links to the sequel, open it, go back
	m.Update(key("enter"))
	if !m.focusDetail {
		t.Fatal("enter should focus the detail panel")
	}
	m.View()
	for i := 0; i < len(m.links) && m.links[m.link].kind != "media"; i++ {
		m.Update(key("j"))
		m.View()
	}
	m.Update(key("enter"))
	if len(m.peek) != 1 || m.current().AnilistID != 182255 {
		t.Fatalf("related link didn't open: %+v", m.peek)
	}
	if !strings.Contains(checkView(t, m), "← back") {
		t.Error("breadcrumb missing")
	}
	m.Update(key("esc"))
	if len(m.peek) != 0 || m.current().AnilistID != 154587 {
		t.Fatal("esc should go back")
	}

	// click a genre → new search with that genre
	click(t, m, "Drama", tea.MouseButtonLeft)
	if m.tab != tabSearch || len(m.f.Genres) != 1 || m.f.Genres[0] != "Drama" || m.f.Status != "" {
		t.Fatalf("genre click → %+v", m.f)
	}
	if !strings.Contains(m.input.Value(), "g:Drama") {
		t.Fatalf("query not synced: %q", m.input.Value())
	}
}

func TestFilterPanelAndChips(t *testing.T) {
	m := searchModel(140, 40)
	m.f = anilist.Filter{}
	m.Update(key("f"))
	if m.panel == nil {
		t.Fatal("f should open the filter editor")
	}
	// Sort: cycle forward
	m.panel.sel = fSort
	m.Update(key("l"))
	if len(m.f.Sort) == 0 {
		t.Fatal("sort not set")
	}
	// Status: enter cycles from Any to Airing
	m.panel.sel = fStatus
	m.Update(key("enter"))
	if m.f.Status != "RELEASING" {
		t.Fatalf("status %q", m.f.Status)
	}
	// Year: type a range
	m.panel.sel = fYear
	m.Update(key("enter"))
	typeKeys(m, "2018-2024")
	m.Update(key("enter"))
	if m.f.YearFrom != 2018 || m.f.YearTo != 2024 {
		t.Fatalf("year %+v", m.f)
	}
	// Genres: picker, filter, include then exclude
	m.panel.sel = fGenres
	m.Update(key("enter"))
	if m.panel.picker == nil {
		t.Fatal("picker")
	}
	typeKeys(m, "act")
	m.Update(key(" "))
	if len(m.f.Genres) != 1 || m.f.Genres[0] != "Action" {
		t.Fatalf("include %+v", m.f.Genres)
	}
	m.Update(key(" "))
	if len(m.f.Genres) != 0 || len(m.f.NotGenres) != 1 {
		t.Fatalf("exclude %+v / %+v", m.f.Genres, m.f.NotGenres)
	}
	m.Update(key("esc"))
	m.Update(key("esc"))
	if m.panel != nil {
		t.Fatal("panel should close")
	}
	q := m.input.Value()
	for _, want := range []string{"-g:Action", "y:2018-2024", "st:airing"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q missing %q", q, want)
		}
	}
	// chips are shown and × removes one
	v := checkView(t, m)
	if !strings.Contains(v, "−Action") || !strings.Contains(v, "Year: 2018–2024") {
		t.Fatalf("chips missing:\n%s", v)
	}
	click(t, m, "Status: Airing", tea.MouseButtonLeft) // opens the editor at Status
	if m.panel == nil || m.panel.sel != fStatus {
		t.Fatal("chip click should open the editor at that field")
	}
	m.Update(key("x"))
	if m.f.Status != "" {
		t.Fatal("x should clear the field")
	}
}

func TestStatusModalClickAndScore(t *testing.T) {
	m := searchModel(120, 30)
	m.Update(listMsg{list: &listcache.List{}})
	m.sSel = 1 // Dandadan, not on list
	m.Update(key("s"))
	if !strings.Contains(checkView(t, m), "Set status") {
		t.Fatal("status prompt not shown")
	}
	click(t, m, "watching", tea.MouseButtonLeft)
	if e := m.listEntry(&row{AnilistID: 2}); e == nil || e.Status != "CURRENT" {
		t.Fatalf("add as watching by click: %+v", e)
	}
	m.Update(key("r"))
	typeKeys(m, "8.5")
	m.Update(key("enter"))
	if e := m.listEntry(&row{AnilistID: 2}); e.MyScore != 85 {
		t.Fatalf("score %d", e.MyScore)
	}
	// search rows reflect the local list immediately
	if !strings.Contains(checkView(t, m), "●") {
		t.Error("status dot missing on result")
	}
}

func TestNowTab(t *testing.T) {
	m := listModel(140, 36)
	m.st = &daemon.Status{
		Now: []daemon.NowPlaying{{Status: mpris.Status{Player: "mpv", Bus: "org.mpris.MediaPlayer2.mpv", State: "Playing", Tracked: true, Watched: 0.62},
			File: "/a/[SubsPlease] Alpha - 04 (1080p).mkv", Parsed: "Alpha", Episode: 4, Key: "a:alpha", Match: "Alpha", MatchID: 1}},
		Queue: []*store.Item{{Kind: "anime", Title: "Bravo", Episode: 1}}, LastErr: "HTTP 503",
	}
	m.pending = []*store.Pending{{Key: "a:mystery#s2", Kind: "anime", Query: "Mystery (season 2)"}}
	m.switchTab(tabNow)
	v := checkView(t, m)
	for _, want := range []string{"Now playing", "mpv", "Alpha - 04", "detected  Alpha · episode 4", "match     Alpha", "Watching 3/12",
		"62% watched", "counts at 80%", "mark watched", "change match", "Needs a match (1)", "Mystery (season 2)", "Queued updates (1)", "offline", "▶ Alpha 4"} {
		if !strings.Contains(v, want) {
			t.Errorf("now tab missing %q", want)
		}
	}
	// the matched show's details are on the right
	if r := m.current(); r == nil || r.AnilistID != 1 {
		t.Fatalf("current on now tab: %+v", r)
	}
	if cmd := m.nowKey("u"); cmd == nil {
		t.Fatal("u should send watched-now")
	}
	// m → search to change the match; enter picks
	m.nowKey("m")
	if m.tab != tabSearch || m.resolving == nil || m.resolving.Key != "a:alpha" || m.f.Search != "Alpha" {
		t.Fatalf("change match: tab=%v resolving=%+v f=%+v", m.tab, m.resolving, m.f)
	}
	m.Update(searchMsg{seq: m.seq, page: 1, res: anilist.Page{Media: []anilist.Media{mk(9, "Alpha (2024)", 12, 70, "", 0)}}})
	if !strings.Contains(checkView(t, m), "Pick the show for") {
		t.Fatal("resolve banner")
	}
	m.Update(key("enter"))
	if m.resolving != nil || m.tab != tabNow {
		t.Fatal("enter should choose and return to Now")
	}
	// pending: season suffix becomes a search term
	m.nSel = 1
	m.nowKey("enter")
	if m.f.Search != "Mystery season 2" {
		t.Fatalf("pending query %q", m.f.Search)
	}
}

func TestNarrowFullDetail(t *testing.T) {
	m := searchModel(80, 24)
	checkView(t, m)
	m.Update(key("enter"))
	if !m.full {
		t.Fatal("enter should open full detail on narrow terminals")
	}
	checkView(t, m)
	m.Update(key("esc"))
	if m.full {
		t.Fatal("esc closes")
	}
}

func TestFitTitleKeepsEnd(t *testing.T) {
	got := fitTitle("Re:Zero kara Hajimeru Isekai Seikatsu 3rd Season", 30)
	if lipgloss.Width(got) != 30 || !strings.HasSuffix(strings.TrimSpace(got), "3rd Season") || !strings.Contains(got, "…") {
		t.Fatalf("%q", got)
	}
	if w := lipgloss.Width(fitTitle("葬送のフリーレン 第2期 とても長いタイトル", 20)); w != 20 {
		t.Fatalf("cjk width %d", w)
	}
}

func TestHelpAndAllTabsFit(t *testing.T) {
	for _, w := range []int{70, 100, 160} {
		m := listModel(w, 30)
		for _, tb := range []tabID{tabList, tabNow, tabSearch} {
			m.switchTab(tb)
			checkView(t, m)
		}
		m.Update(key("?"))
		checkView(t, m)
	}
}

func TestHighlightAndChipGlue(t *testing.T) {
	h := highlight(sAccent.Render("a")+" plain "+sOK.Render("b"), 20)
	// every reset is followed by the background again
	if strings.Count(h, "\x1b[0m") != strings.Count(h, "\x1b[0m\x1b[48;5;237m")+1 {
		t.Fatalf("background not restored after resets: %q", h)
	}
	if w := lipgloss.Width(h); w != 20 {
		t.Fatalf("width %d", w)
	}
	var b block
	segs := []seg{txt("aaaa"), {text: "×", glue: true}, txt("bbbb"), {text: "×", glue: true}}
	wrapSegs(&b, "", segs, " ", 9) // "aaaa× bbbb×" is 11 wide → must wrap before bbbb, not before ×
	if len(b.lines) != 2 || b.lines[0] != "aaaa×" || strings.TrimSpace(b.lines[1]) != "bbbb×" {
		t.Fatalf("glue: %q", b.lines)
	}
}

// A show opened from Related/recommendations has no cover URL until its
// details load; asking for the cover early must not mark it failed, or the
// cover never shows once the URL arrives.
func TestCoverForRelatedShowLoads(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := searchModel(150, 44)
	m.cov.state = map[uint32]int{}
	m.Update(key("enter"))
	m.peek = append(m.peek, row{AnilistID: 182255, Title: "Sousou no Frieren 2nd Season", Format: "TV"})
	// debounce fires before details arrive: no URL yet
	m.Update(debounceMsg{id: 182255})
	if s := m.cov.get(182255); s != coverNone {
		t.Fatalf("cover state after URL-less request = %d, want none", s)
	}
	d := mk(182255, "Sousou no Frieren 2nd Season", 10, 88, "", 0)
	d.CoverImage.Large = "https://example.invalid/cover.jpg"
	_, cmd := m.Update(detailMsg{key: 182255, m: &d})
	if cmd == nil || m.cov.get(182255) != coverLoading {
		t.Fatalf("cover not requested once the URL is known (state %d)", m.cov.get(182255))
	}
}
