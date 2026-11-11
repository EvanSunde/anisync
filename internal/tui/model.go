// Package tui is the interactive app: your list (works offline from the
// local copy), what's playing now, and AniList search with clickable
// filters, details, cover art (kitty/Ghostty), and clickable related shows,
// genres and tags. Edits go through the daemon, so they're queued offline
// and applied to AniList and MAL together.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/config"
	"github.com/evansunde/anisync/internal/daemon"
	"github.com/evansunde/anisync/internal/kitty"
	"github.com/evansunde/anisync/internal/listcache"
	"github.com/evansunde/anisync/internal/query"
	"github.com/evansunde/anisync/internal/store"
)

type tabID int

const (
	tabList tabID = iota
	tabNow
	tabSearch
)

var tabNames = []string{"1 My list", "2 Now", "3 Search"}

type modalKind int

const (
	modalNone modalKind = iota
	modalStatus
	modalScore
	modalProgress
	modalDelete
	modalHelp
)

// list sections, in display order ("" = all)
var sections = []string{"CURRENT", "REPEATING", "PLANNING", "COMPLETED", "PAUSED", "DROPPED", ""}

var sortModes = []string{"recently updated", "title", "your score", "average score", "airing next"}

// resolveTarget is a file title the user is picking a show for.
type resolveTarget struct {
	Key, Query string
	Episode    int
}

type model struct {
	w, h   int
	tab    tabID
	al     *anilist.Client
	userID int
	vocab  *query.Vocab
	cov    *covers

	// detail panel
	details     map[uint32]*anilist.Media
	detailErr   map[uint32]string
	detailReq   map[uint32]bool
	peek        []row // shows opened from related / recommendations
	focusDetail bool
	link        int
	links       []dlink // links in the rendered detail (keyboard navigation)
	dScroll     int
	full        bool // detail full screen (narrow terminals)

	hits      []hit
	areas     []area
	lastWheel time.Time
	wheelDir  int

	modal      modalKind
	modalInput textinput.Model
	msg        string
	msgErr     bool

	// my list
	list        []*row
	listFetched time.Time
	listSyncing bool
	listErr     string
	listLoaded  bool
	section     int
	lSel        int
	filter      textinput.Model
	filtering   bool
	sortMode    int
	queueLen    int

	// now
	st        *daemon.Status
	daemonErr bool
	pending   []*store.Pending
	nSel      int

	// search
	input     textinput.Model
	typing    bool
	f         anilist.Filter
	seq       int
	results   []row
	page      int
	hasNext   bool
	loading   bool
	total     int
	warns     []string
	sSel      int
	resolving *resolveTarget
	panel     *filterPanel
	liveSeq   int
	thr       float64
}

// Run starts the TUI.
func Run() error {
	tok, err := store.LoadTokens(config.ConfigDir())
	if err != nil {
		return err
	}
	cfg, _ := config.Load()
	m := newModel()
	m.thr = cfg.Threshold
	if cfg.AniList.Enabled && tok.AniList.Token != "" {
		m.al.Token, m.userID = tok.AniList.Token, tok.AniList.UserID
	}
	out := &ttyOut{f: os.Stdout}
	m.cov = newCovers(out, kitty.Supported() && cfg.Covers)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithOutput(out))
	_, err = p.Run()
	m.cov.cleanup()
	return err
}

func newModel() *model {
	m := &model{
		al:        &anilist.Client{},
		details:   map[uint32]*anilist.Media{},
		detailErr: map[uint32]string{},
		detailReq: map[uint32]bool{},
		page:      1,
		cov:       &covers{state: map[uint32]int{}},
		f:         anilist.Filter{Status: "RELEASING", Sort: []string{"TRENDING_DESC"}},
	}
	m.input = textinput.New()
	m.input.Prompt = "  "
	m.input.Placeholder = `title and/or filters — isekai g:action -g:ecchi y:2018-2024 score>75 sort:score  (f: filter editor, ?: syntax)`
	m.input.SetValue(query.Format(m.f))
	m.filter = textinput.New()
	m.filter.Prompt = " filter › "
	m.modalInput = textinput.New()
	m.modalInput.CharLimit = 8
	return m
}

func (m *model) Init() tea.Cmd {
	m.loading = true
	m.listSyncing = true
	return tea.Batch(localListCmd(), m.syncListCmd(), vocabCmd(), m.searchCmd(m.f, 1, m.seq), statusCmd(), pendingCmd(), tick())
}

// ---------------------------------------------------------------- selection

func (m *model) visibleList() []*row {
	sec := sections[m.section]
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	var out []*row
	for _, r := range m.list {
		if sec != "" && r.Status != sec {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(strings.Join(append(r.Titles(), r.Genres...), " ")), q) {
			continue
		}
		out = append(out, r)
	}
	sortRows(out, m.sortMode)
	return out
}

// listEntry returns your list entry for a show, if any.
func (m *model) listEntry(r *row) *row {
	for _, e := range m.list {
		if (r.AnilistID != 0 && e.AnilistID == r.AnilistID) || (r.MalID != 0 && e.MalID == r.MalID) {
			return e
		}
	}
	return nil
}

// enrich overlays your (local, possibly pending) list entry on a row.
func (m *model) enrich(r row) row {
	if e := m.listEntry(&r); e != nil {
		r.Status, r.Progress, r.MyScore, r.Repeat, r.Pending = e.Status, e.Progress, e.MyScore, e.Repeat, e.Pending
	} else if m.listLoaded {
		r.Status, r.Progress, r.MyScore, r.Pending = "", 0, 0, false
	}
	return r
}

// current is the show the detail panel and actions refer to.
func (m *model) current() *row {
	if n := len(m.peek); n > 0 {
		r := m.enrich(m.peek[n-1])
		return &r
	}
	switch m.tab {
	case tabList:
		if v := m.visibleList(); m.lSel < len(v) {
			r := *v[m.lSel]
			return &r
		}
	case tabSearch:
		if m.sSel < len(m.results) {
			r := m.enrich(m.results[m.sSel])
			return &r
		}
	case tabNow:
		if n := m.nowSelected(); n != nil && n.MatchID != 0 {
			r := row{AnilistID: n.MatchID, MalID: n.MatchMal, Title: n.Match}
			if e := m.listEntry(&r); e != nil {
				r = *e
			} else if d := m.details[r.Key()]; d != nil {
				r = m.enrich(listcache.FromMedia(d))
			}
			return &r
		}
	}
	return nil
}

func (m *model) wide() bool { return m.w >= 110 }

func (m *model) flash(s string, isErr bool) { m.msg, m.msgErr = s, isErr }

// selChanged schedules loading the detail/cover of the selected show.
func (m *model) selChanged() tea.Cmd {
	m.dScroll, m.link = 0, 0
	r := m.current()
	if r == nil {
		return nil
	}
	if m.details[r.Key()] != nil {
		return m.cov.request(r.Key(), r.Cover)
	}
	return debounce(r.Key())
}

// ---------------------------------------------------------------- update

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.input.Width = max(10, m.w-6)
		return m, nil

	case tickMsg:
		return m, tea.Batch(statusCmd(), pendingCmd(), tick())

	case statusMsg:
		m.st, m.daemonErr = msg.st, msg.err != nil
		var cmd tea.Cmd
		if msg.st != nil {
			// the daemon's copy of the list changes when the queue does
			if n := len(msg.st.Queue); n != m.queueLen {
				m.queueLen = n
				cmd = daemonListCmd()
			}
			if m.nSel >= m.nowCount() {
				m.nSel = max(0, m.nowCount()-1)
			}
		}
		return m, cmd

	case pendingMsg:
		if msg.err == nil {
			m.pending = msg.p
		}
		return m, nil

	case vocabMsg:
		m.vocab = msg.v
		return m, nil

	case listMsg:
		if msg.list != nil {
			m.list, m.listLoaded = msg.list.Entries, true
			if !msg.list.Fetched.IsZero() {
				m.listFetched = msg.list.Fetched
			}
			if m.lSel >= len(m.visibleList()) {
				m.lSel = max(0, len(m.visibleList())-1)
			}
		}
		if msg.synced || msg.err != "" {
			m.listSyncing = false
			m.listErr = msg.err
		}
		if m.tab == tabList && msg.list != nil {
			return m, m.selChanged()
		}
		return m, nil

	case searchMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.flash("search failed: "+msg.err.Error(), true)
			return m, nil
		}
		var rows []row
		for i := range msg.res.Media {
			rows = append(rows, listcache.FromMedia(&msg.res.Media[i]))
		}
		if msg.page == 1 {
			m.results, m.sSel = rows, 0
		} else {
			m.results = append(m.results, rows...)
		}
		m.page, m.hasNext, m.total = msg.page, msg.res.HasNext, msg.res.Total
		if m.tab == tabSearch {
			return m, m.selChanged()
		}
		return m, nil

	case liveSearchMsg:
		if msg.seq == m.liveSeq {
			return m, m.runSearch()
		}
		return m, nil

	case debounceMsg:
		r := m.current()
		if r == nil || r.Key() != msg.id {
			return m, nil
		}
		var cmds []tea.Cmd
		if m.details[msg.id] == nil && !m.detailReq[msg.id] {
			m.detailReq[msg.id] = true
			cmds = append(cmds, m.detailCmd(*r))
		}
		cmds = append(cmds, m.cov.request(r.Key(), r.Cover))
		return m, tea.Batch(cmds...)

	case detailMsg:
		delete(m.detailReq, msg.key)
		if msg.err != nil {
			m.detailErr[msg.key] = msg.err.Error()
			return m, nil
		}
		m.details[msg.key] = msg.m
		delete(m.detailErr, msg.key)
		// a peeked show only had id/title: fill it in
		if n := len(m.peek); n > 0 && m.peek[n-1].Key() == msg.key {
			m.peek[n-1] = listcache.FromMedia(msg.m)
		}
		if r := m.current(); r != nil && r.Key() == msg.key {
			cover := r.Cover
			if cover == "" {
				cover = msg.m.CoverImage.Large
			}
			return m, m.cov.request(msg.key, cover)
		}
		return m, nil

	case coverMsg:
		return m, nil

	case actionMsg:
		if msg.err != nil {
			m.flash(msg.err.Error(), true)
		} else {
			m.flash(msg.text, false)
		}
		cmds := []tea.Cmd{pendingCmd(), statusCmd()}
		if msg.reload {
			cmds = append(cmds, daemonListCmd())
		}
		return m, tea.Batch(cmds...)

	case tea.MouseMsg:
		return m, m.mouse(msg)

	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// ---------------------------------------------------------------- mouse

func (m *model) mouse(msg tea.MouseMsg) tea.Cmd {
	if msg.Action != tea.MouseActionPress {
		return nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		dir := 1
		if msg.Button == tea.MouseButtonWheelUp {
			dir = -1
		}
		// Terminals send several events per wheel notch (kitty: 5 by
		// default); one notch = one step.
		now := time.Now()
		if dir == m.wheelDir && now.Sub(m.lastWheel) < 25*time.Millisecond {
			m.lastWheel = now
			return nil
		}
		m.lastWheel, m.wheelDir = now, dir
		for i := len(m.areas) - 1; i >= 0; i-- {
			a := m.areas[i]
			if msg.X >= a.x0 && msg.X < a.x1 && msg.Y >= a.y0 && msg.Y < a.y1 {
				return a.wheel(dir)
			}
		}
	case tea.MouseButtonLeft, tea.MouseButtonRight:
		for i := len(m.hits) - 1; i >= 0; i-- {
			h := m.hits[i]
			if msg.Y == h.y && msg.X >= h.x0 && msg.X < h.x1 {
				return h.click(msg)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- keys

func (m *model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	if m.modal != modalNone {
		return m.modalKey(k)
	}
	if m.panel != nil {
		return m, m.panelKey(k)
	}
	if m.typing {
		switch s {
		case "enter":
			m.typing = false
			m.input.Blur()
			m.f, m.warns = query.Parse(m.input.Value(), m.vocab)
			if strings.TrimSpace(m.input.Value()) == "" {
				m.f = anilist.Filter{Status: "RELEASING", Sort: []string{"TRENDING_DESC"}}
			}
			return m, m.runSearch()
		case "esc":
			m.typing = false
			m.input.Blur()
			m.input.SetValue(query.Format(m.f))
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return m, cmd
	}
	if m.filtering {
		switch s {
		case "enter", "esc":
			m.filtering = false
			m.filter.Blur()
			if s == "esc" {
				m.filter.SetValue("")
			}
			m.lSel = 0
			return m, m.selChanged()
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(k)
		m.lSel = 0
		return m, cmd
	}
	if m.focusDetail || m.full {
		if cmd, ok := m.detailKey(s); ok {
			return m, cmd
		}
	}

	switch s {
	case "q":
		return m, tea.Quit
	case "?":
		m.modal = modalHelp
		return m, nil
	case "1", "2", "3":
		return m, m.switchTab(tabID(s[0] - '1'))
	case "tab":
		return m, m.switchTab((m.tab + 1) % 3)
	case "shift+tab":
		return m, m.switchTab((m.tab + 2) % 3)
	case "J", "ctrl+d":
		m.dScroll += 3
		return m, nil
	case "K", "ctrl+u":
		m.dScroll = max(0, m.dScroll-3)
		return m, nil
	}

	switch m.tab {
	case tabList:
		return m, m.listKey(s)
	case tabNow:
		return m, m.nowKey(s)
	default:
		return m, m.searchKey(s)
	}
}

// openDetail moves keyboard focus into the detail panel (or full screen).
func (m *model) openDetail() tea.Cmd {
	if m.current() == nil {
		return nil
	}
	if m.wide() {
		m.focusDetail = true
	} else {
		m.full = true
	}
	m.link = 0
	return nil
}

func (m *model) closeDetail() {
	m.focusDetail, m.full, m.peek, m.dScroll, m.link = false, false, nil, 0, 0
}

func (m *model) detailKey(s string) (tea.Cmd, bool) {
	switch s {
	case "esc", "backspace", "h", "left":
		if n := len(m.peek); n > 0 {
			m.peek = m.peek[:n-1]
			m.link, m.dScroll = 0, 0
			return m.selChanged(), true
		}
		m.closeDetail()
		return nil, true
	case "j", "down", "tab":
		if len(m.links) > 0 {
			m.link = (m.link + 1) % len(m.links)
		} else {
			m.dScroll++
		}
		return nil, true
	case "k", "up", "shift+tab":
		if len(m.links) > 0 {
			m.link = (m.link - 1 + len(m.links)) % len(m.links)
		} else {
			m.dScroll = max(0, m.dScroll-1)
		}
		return nil, true
	case "enter", "l", "right":
		if m.link < len(m.links) {
			return m.activate(m.links[m.link], false), true
		}
		return nil, true
	case "A":
		if m.link < len(m.links) {
			return m.activate(m.links[m.link], true), true
		}
		return nil, true
	case "J", "ctrl+d":
		m.dScroll += 3
		return nil, true
	case "K", "ctrl+u":
		m.dScroll = max(0, m.dScroll-3)
		return nil, true
	}
	if cmd, ok := m.actionKey(s); ok {
		return cmd, true
	}
	return nil, false
}

func (m *model) switchTab(t tabID) tea.Cmd {
	m.tab = t
	m.closeDetail()
	switch t {
	case tabNow:
		return tea.Batch(statusCmd(), pendingCmd(), m.selChanged())
	}
	return m.selChanged()
}

// move handles list navigation keys.
func (m *model) move(s string, sel *int, n int) (tea.Cmd, bool) {
	old := *sel
	page := max(1, m.bodyHeight()-6)
	switch s {
	case "j", "down":
		*sel++
	case "k", "up":
		*sel--
	case "pgdown", "ctrl+f":
		*sel += page
	case "pgup", "ctrl+b":
		*sel -= page
	case "g", "home":
		*sel = 0
	case "G", "end":
		*sel = n - 1
	default:
		return nil, false
	}
	*sel = max(0, min(*sel, n-1))
	if *sel == old {
		return nil, true
	}
	return m.selChanged(), true
}

func (m *model) listKey(s string) tea.Cmd {
	switch s {
	case "/":
		m.filtering = true
		m.filter.Focus()
		return textinput.Blink
	case "h", "left":
		m.section = (m.section + len(sections) - 1) % len(sections)
		m.lSel = 0
		return m.selChanged()
	case "l", "right":
		m.section = (m.section + 1) % len(sections)
		m.lSel = 0
		return m.selChanged()
	case "S":
		m.sortMode = (m.sortMode + 1) % len(sortModes)
		m.flash("sorted by "+sortModes[m.sortMode], false)
		return m.selChanged()
	case "ctrl+r":
		m.listSyncing = true
		return m.syncListCmd()
	case "enter":
		return m.openDetail()
	}
	if cmd, ok := m.move(s, &m.lSel, len(m.visibleList())); ok {
		return cmd
	}
	cmd, _ := m.actionKey(s)
	return cmd
}

// ---------------------------------------------------------------- actions

func (m *model) actionKey(s string) (tea.Cmd, bool) {
	r := m.current()
	if r == nil {
		return nil, false
	}
	switch s {
	case "s", "a":
		m.modal = modalStatus
	case "+", "=":
		return m.bump(r, 1), true
	case "-":
		return m.bump(r, -1), true
	case "e":
		m.modal = modalProgress
		m.modalInput.SetValue("")
		m.modalInput.Placeholder = strconv.Itoa(r.Progress)
		m.modalInput.Focus()
	case "r":
		m.modal = modalScore
		m.modalInput.SetValue("")
		m.modalInput.Placeholder = fmt.Sprintf("%.1f", float64(r.MyScore)/10)
		m.modalInput.Focus()
	case "o":
		u := fmt.Sprintf("https://anilist.co/anime/%d", r.AnilistID)
		if r.AnilistID == 0 {
			u = fmt.Sprintf("https://myanimelist.net/anime/%d", r.MalID)
		}
		_ = exec.Command("xdg-open", u).Start()
		m.flash("opened "+u, false)
	case "O":
		if r.MalID != 0 {
			u := fmt.Sprintf("https://myanimelist.net/anime/%d", r.MalID)
			_ = exec.Command("xdg-open", u).Start()
			m.flash("opened "+u, false)
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *model) bump(r *row, d int) tea.Cmd {
	if r.Status == "" {
		if d > 0 {
			return m.set(r, "CURRENT", ptr(1), nil, false)
		}
		return nil
	}
	n := r.Progress + d
	if n < 0 || (r.Episodes > 0 && n > r.Episodes) {
		m.flash(fmt.Sprintf("episode %d is out of range", n), true)
		return nil
	}
	return m.set(r, "", ptr(n), nil, false)
}

func ptr(i int) *int { return &i }

// set sends an edit to the daemon and applies it locally right away.
func (m *model) set(r *row, status string, progress, score *int, del bool) tea.Cmd {
	if m.daemonErr {
		m.flash("daemon not running — start it to edit (systemctl --user start anisync)", true)
		return nil
	}
	meta := *r
	meta.Status, meta.Progress, meta.MyScore, meta.Pending = "", 0, 0, false
	a := daemon.SetArgs{AnilistID: r.AnilistID, MalID: r.MalID, Title: r.Title, Episodes: r.Episodes,
		Status: status, Progress: progress, Score: score, Delete: del, Meta: &meta}

	// optimistic local update (the daemon does the same to its copy)
	e := m.listEntry(r)
	if del {
		if e != nil {
			out := m.list[:0]
			for _, x := range m.list {
				if x != e {
					out = append(out, x)
				}
			}
			m.list = out
		}
	} else {
		if e == nil {
			cp := meta
			cp.Status = "PLANNING"
			e = &cp
			m.list = append(m.list, e)
		}
		if status != "" {
			if status == "REPEATING" && e.Status != "REPEATING" && progress == nil {
				e.Progress = 0
			}
			e.Status = status
		}
		if progress != nil {
			e.Progress = *progress
			if status == "" {
				switch {
				case r.Episodes > 0 && e.Progress >= r.Episodes:
					e.Status = "COMPLETED"
				case e.Progress > 0 && (e.Status == "PLANNING" || e.Status == "PAUSED" || e.Status == "DROPPED"):
					e.Status = "CURRENT"
				}
			}
		}
		if e.Status == "COMPLETED" && r.Episodes > 0 {
			e.Progress = r.Episodes
		}
		if score != nil {
			e.MyScore = *score
		}
		e.Pending, e.Updated = true, time.Now().Unix()
	}

	var parts []string
	switch {
	case del:
		parts = []string{"removed from list"}
	default:
		if status != "" {
			parts = append(parts, statusName(status))
		}
		if progress != nil {
			parts = append(parts, fmt.Sprintf("episode %d", *progress))
		}
		if score != nil {
			parts = append(parts, fmt.Sprintf("score %.1f", float64(*score)/10))
		}
		if len(parts) == 0 {
			parts = append(parts, "added to Planning")
		}
	}
	delete(m.details, r.Key()) // refetch with the new list entry
	return setCmd(a, r.Title+": "+strings.Join(parts, ", "))
}

func (m *model) modalKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "esc" || (m.modal == modalHelp && (s == "?" || s == "q" || s == "enter")) {
		m.modal = modalNone
		m.modalInput.Blur()
		return m, nil
	}
	r := m.current()
	switch m.modal {
	case modalStatus:
		m.modal = modalNone
		return m, m.statusChoice(s)
	case modalDelete:
		m.modal = modalNone
		if s == "y" && r != nil {
			return m, m.set(r, "", nil, nil, true)
		}
	case modalProgress, modalScore:
		if s == "enter" {
			kind := m.modal
			m.modal = modalNone
			m.modalInput.Blur()
			v := strings.TrimSpace(m.modalInput.Value())
			if r == nil || v == "" {
				return m, nil
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 {
				m.flash("not a number: "+v, true)
				return m, nil
			}
			if kind == modalProgress {
				if r.Episodes > 0 && int(f) > r.Episodes {
					m.flash(fmt.Sprintf("%s has %d episodes", r.Title, r.Episodes), true)
					return m, nil
				}
				return m, m.set(r, "", ptr(int(f)), nil, false)
			}
			if f > 10 {
				m.flash("score is 0-10 (e.g. 8.5); 0 clears it", true)
				return m, nil
			}
			return m, m.set(r, "", nil, ptr(int(f*10+0.5)), false)
		}
		var cmd tea.Cmd
		m.modalInput, cmd = m.modalInput.Update(k)
		return m, cmd
	}
	return m, nil
}

var statusKeys = []struct{ key, status, label string }{
	{"w", "CURRENT", "watching"}, {"p", "PLANNING", "planning"}, {"c", "COMPLETED", "completed"},
	{"h", "PAUSED", "paused"}, {"d", "DROPPED", "dropped"}, {"r", "REPEATING", "rewatching"},
}

func (m *model) statusChoice(s string) tea.Cmd {
	r := m.current()
	if r == nil {
		return nil
	}
	if s == "x" {
		if r.Status != "" {
			m.modal = modalDelete
		}
		return nil
	}
	for _, sk := range statusKeys {
		if sk.key == s {
			return m.set(r, sk.status, nil, nil, false)
		}
	}
	return nil
}
