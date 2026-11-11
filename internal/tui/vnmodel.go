package tui

// VN mode: the same three tabs (your list, now, search) and detail panel,
// backed by VNDB instead of AniList. `v` or the header switch flips modes.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/evansunde/anisync/internal/daemon"
	"github.com/evansunde/anisync/internal/ipc"
	"github.com/evansunde/anisync/internal/netx"
	"github.com/evansunde/anisync/internal/query"
	"github.com/evansunde/anisync/internal/store"
	"github.com/evansunde/anisync/internal/vndb"
	"github.com/evansunde/anisync/internal/vnlist"
)

// vnrow is one visual novel as displayed (list entry, search result, relation).
type vnrow = vnlist.Entry

type vnState struct {
	started bool   // loaded on first use
	token   string // VNDB token (direct list sync when the daemon isn't running)
	user    string
	covers  string // safe | all | off

	// my list
	list     []*vnrow
	labels   []vndb.Label
	fetched  time.Time
	syncing  bool
	err      string
	loaded   bool
	secID    int // selected label (0 = all)
	lSel     int
	sortMode int
	queueLen int

	// search
	input   textinput.Model
	f       query.VNFilter
	seq     int
	results []vnrow
	page    int
	more    bool
	loading bool
	total   int
	warns   []string
	sSel    int
	vocab   *query.VNVocab

	// detail
	details   map[uint32]*vndb.VN
	detailErr map[uint32]string
	detailReq map[uint32]bool
	peek      []vnrow

	// now
	nSel int
}

var defaultVNFilter = query.VNFilter{Sort: "rating", VotesMin: 500}

func newVNState() vnState {
	vs := vnState{covers: "safe", secID: vndb.Playing, page: 1, f: defaultVNFilter,
		details: map[uint32]*vndb.VN{}, detailErr: map[uint32]string{}, detailReq: map[uint32]bool{}}
	vs.input = textinput.New()
	vs.input.Prompt = "  "
	vs.input.Placeholder = `title and/or filters — t:"male protagonist" len:long y:>2015 rating>7.5 lang:en sort:rating  (f: filter editor, ?: syntax)`
	vs.input.SetValue(query.FormatVN(vs.f))
	return vs
}

var vnSortModes = []string{"recently updated", "title", "your vote", "rating", "released", "playtime"}

// ---------------------------------------------------------------- mode

// setMode switches between anime and VN mode, keeping the tab.
func (m *model) setMode(vn bool) tea.Cmd {
	if m.vn == vn {
		return nil
	}
	m.vn = vn
	m.closeDetail()
	m.panel, m.typing, m.resolving, m.filtering, m.modal = nil, false, nil, false, modalNone
	m.input.Blur()
	m.vs.input.Blur()
	m.filter.SetValue("")
	var cmds []tea.Cmd
	if vn && !m.vs.started {
		m.vs.started = true
		m.vs.loading, m.vs.syncing = true, m.vs.token != ""
		cmds = append(cmds, vnLocalListCmd(), m.vnSearchCmd(m.vs.f, 1, m.vs.seq), vnVocabCmd())
		if m.vs.token != "" {
			cmds = append(cmds, m.vnSyncCmd())
		} else {
			cmds = append(cmds, daemonVNListCmd())
		}
	}
	if vn {
		m.flash("visual novels (VNDB) — v switches back to anime", false)
	} else {
		m.flash("anime — v switches to visual novels", false)
	}
	return tea.Batch(append(cmds, m.selChanged())...)
}

// ---------------------------------------------------------------- list

type vnSection struct {
	id   int // label id, 0 = all
	name string
}

// vnSections: the built-in statuses, then custom labels, Blacklist/Voted
// when used, and All.
func (m *model) vnSections() []vnSection {
	counts := m.vnCounts()
	name := func(id int) string {
		for _, l := range m.vs.labels {
			if l.ID == id {
				return l.Label
			}
		}
		return vnlist.DefaultLabels[id]
	}
	var out []vnSection
	for id := vndb.Playing; id <= vndb.Wishlist; id++ {
		out = append(out, vnSection{id, name(id)})
	}
	var extra []vndb.Label
	for _, l := range m.vs.labels {
		if l.ID > vndb.Voted || ((l.ID == vndb.Blacklist || l.ID == vndb.Voted) && counts[l.ID] > 0) {
			extra = append(extra, l)
		}
	}
	sort.SliceStable(extra, func(i, j int) bool { return extra[i].ID > vndb.Voted && extra[j].ID <= vndb.Voted })
	for _, l := range extra {
		out = append(out, vnSection{l.ID, l.Label})
	}
	return append(out, vnSection{0, "All"})
}

func (m *model) vnCounts() map[int]int {
	c := map[int]int{}
	for _, r := range m.vs.list {
		for _, l := range r.Labels {
			c[l]++
		}
	}
	return c
}

func (m *model) vnSecIndex() int {
	for i, s := range m.vnSections() {
		if s.id == m.vs.secID {
			return i
		}
	}
	return 0
}

func (m *model) vnVisible() []*vnrow {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	var out []*vnrow
	for _, r := range m.vs.list {
		if m.vs.secID != 0 && !r.Has(m.vs.secID) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.Titles()), q) {
			continue
		}
		out = append(out, r)
	}
	switch m.vs.sortMode {
	case 1:
		sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
	case 2:
		sort.SliceStable(out, func(i, j int) bool { return out[i].Vote > out[j].Vote })
	case 3:
		sort.SliceStable(out, func(i, j int) bool { return out[i].Rating > out[j].Rating })
	case 4:
		sort.SliceStable(out, func(i, j int) bool { return out[i].Released > out[j].Released })
	case 5:
		sort.SliceStable(out, func(i, j int) bool { return out[i].Playtime > out[j].Playtime })
	default:
		sort.SliceStable(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	}
	return out
}

func (m *model) vnEntry(id string) *vnrow {
	for _, e := range m.vs.list {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// vnEnrich overlays your (local, possibly pending) list entry on a row.
func (m *model) vnEnrich(r vnrow) vnrow {
	if e := m.vnEntry(r.ID); e != nil {
		r.OnList, r.Labels, r.Vote, r.Started, r.Finished, r.Notes = true, e.Labels, e.Vote, e.Started, e.Finished, e.Notes
		r.Pending, r.Playtime, r.Updated = e.Pending, e.Playtime, e.Updated
		if r.Cover == "" {
			r.Cover, r.Sexual, r.Violence = e.Cover, e.Sexual, e.Violence
		}
	} else if m.vs.loaded {
		r.OnList, r.Labels, r.Vote, r.Started, r.Finished, r.Notes, r.Pending = false, nil, 0, "", "", "", false
	}
	return r
}

// vnCurrent is the VN the detail panel and actions refer to.
func (m *model) vnCurrent() *vnrow {
	vs := &m.vs
	if n := len(vs.peek); n > 0 {
		r := m.vnEnrich(vs.peek[n-1])
		return &r
	}
	switch m.tab {
	case tabList:
		if v := m.vnVisible(); vs.lSel < len(v) {
			r := *v[vs.lSel]
			return &r
		}
	case tabSearch:
		if vs.sSel < len(vs.results) {
			r := m.vnEnrich(vs.results[vs.sSel])
			return &r
		}
	case tabNow:
		if n := m.vnNowSelected(); n != nil && n.VNDBID != "" {
			r := vnrow{ID: n.VNDBID, Title: n.Title}
			if d := vs.details[r.Key()]; d != nil {
				r = vnlist.FromVN(d)
			}
			r = m.vnEnrich(r)
			return &r
		}
	}
	return nil
}

func (m *model) vnSelChanged() tea.Cmd {
	m.dScroll, m.link = 0, 0
	r := m.vnCurrent()
	if r == nil {
		return nil
	}
	if m.vs.details[r.Key()] != nil {
		return m.vnCover(*r)
	}
	return tea.Tick(180*time.Millisecond, func(time.Time) tea.Msg { return vnDebounceMsg{id: r.Key()} })
}

// vnCoverShown says whether a VN's cover may be displayed (vn_covers).
func (m *model) vnCoverShown(r vnrow) bool {
	switch m.vs.covers {
	case "off":
		return false
	case "all":
		return true
	}
	if d := m.vs.details[r.Key()]; d != nil && d.Image != nil {
		return d.Image.Sexual < 1 && d.Image.Violence < 2
	}
	return !r.Explicit()
}

func (m *model) vnCover(r vnrow) tea.Cmd {
	if !m.vnCoverShown(r) {
		return nil
	}
	url := r.Cover
	if d := m.vs.details[r.Key()]; url == "" && d != nil && d.Image != nil {
		url = d.Image.URL
	}
	if url == "" {
		return nil
	}
	return m.cov.request(r.Key(), url)
}

// ---------------------------------------------------------------- keys

func (m *model) vnListKey(s string) tea.Cmd {
	vs := &m.vs
	secs := m.vnSections()
	switch s {
	case "/":
		m.filtering = true
		m.filter.Focus()
		return textinput.Blink
	case "h", "left":
		i := (m.vnSecIndex() + len(secs) - 1) % len(secs)
		vs.secID, vs.lSel = secs[i].id, 0
		return m.selChanged()
	case "l", "right":
		i := (m.vnSecIndex() + 1) % len(secs)
		vs.secID, vs.lSel = secs[i].id, 0
		return m.selChanged()
	case "S":
		vs.sortMode = (vs.sortMode + 1) % len(vnSortModes)
		m.flash("sorted by "+vnSortModes[vs.sortMode], false)
		return m.selChanged()
	case "ctrl+r":
		vs.syncing = true
		return m.vnSyncCmd()
	case "enter":
		return m.openDetail()
	}
	if cmd, ok := m.move(s, &vs.lSel, len(m.vnVisible())); ok {
		return cmd
	}
	cmd, _ := m.vnActionKey(s)
	return cmd
}

func (m *model) vnActionKey(s string) (tea.Cmd, bool) {
	r := m.vnCurrent()
	if r == nil {
		return nil, false
	}
	switch s {
	case "s", "a":
		m.modal = modalStatus
	case "r":
		m.modal = modalScore
		m.modalInput.SetValue("")
		m.modalInput.Placeholder = fmt.Sprintf("%.1f", float64(r.Vote)/10)
		m.modalInput.Focus()
	case "e", "E":
		m.modal = modalStarted
		cur := r.Started
		if s == "E" {
			m.modal, cur = modalFinished, r.Finished
		}
		m.modalInput.SetValue("")
		m.modalInput.CharLimit = 10
		m.modalInput.Placeholder = orDefault(cur, "YYYY-MM-DD")
		m.modalInput.Focus()
	case "o":
		u := "https://vndb.org/" + r.ID
		_ = exec.Command("xdg-open", u).Start()
		m.flash("opened "+u, false)
	default:
		return nil, false
	}
	return nil, true
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

var reISODate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func (m *model) vnModalKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	r := m.vnCurrent()
	switch m.modal {
	case modalStatus:
		m.modal = modalNone
		return m, m.vnLabelChoice(s)
	case modalDelete:
		m.modal = modalNone
		if s == "y" && r != nil {
			return m, m.vnSet(r, store.VNEdit{Delete: true}, "removed from your VNDB list")
		}
		return m, nil
	case modalScore, modalStarted, modalFinished, modalFinish:
		if s != "enter" {
			var cmd tea.Cmd
			m.modalInput, cmd = m.modalInput.Update(k)
			return m, cmd
		}
		kind := m.modal
		m.modal = modalNone
		m.modalInput.Blur()
		m.modalInput.CharLimit = 8
		v := strings.TrimSpace(m.modalInput.Value())
		if kind == modalFinish {
			n := m.vnNowSelected()
			if n == nil {
				return m, nil
			}
			vote, err := parseVote(v, true)
			if err != nil {
				m.flash(err.Error(), true)
				return m, nil
			}
			return m, vnFinishCmd(n.Key, vote, firstNonEmpty(n.Title, n.Folder))
		}
		if r == nil || v == "" {
			return m, nil
		}
		if kind == modalScore {
			vote, err := parseVote(v, false)
			if err != nil {
				m.flash(err.Error(), true)
				return m, nil
			}
			desc := "vote cleared"
			if vote > 0 {
				desc = fmt.Sprintf("vote %.1f", float64(vote)/10)
			}
			return m, m.vnSet(r, store.VNEdit{Vote: &vote}, desc)
		}
		switch strings.ToLower(v) {
		case "today", "t", "now":
			v = netx.Today()
		case "-", "x", "clear", "none":
			v = ""
		default:
			if !reISODate.MatchString(v) {
				m.flash("dates are YYYY-MM-DD (or today, or - to clear)", true)
				return m, nil
			}
		}
		if kind == modalStarted {
			return m, m.vnSet(r, store.VNEdit{Started: &v}, "started "+orDefault(v, "cleared"))
		}
		return m, m.vnSet(r, store.VNEdit{Finished: &v}, "finished "+orDefault(v, "cleared"))
	}
	return m, nil
}

// parseVote reads "8.5" (1-10) into VNDB's 10-100; 0 clears.
func parseVote(v string, optional bool) (int, error) {
	if v == "" && optional {
		return 0, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 10 || (f > 0 && f < 1) {
		return 0, errors.New("vote is 1-10 (e.g. 8.5); 0 clears it")
	}
	return int(math.Round(f * 10)), nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// vnStatusKeys are the built-in statuses; picking one replaces the others.
var vnStatusKeys = []struct {
	key   string
	label int
}{{"p", vndb.Playing}, {"f", vndb.Finished}, {"s", vndb.Stalled}, {"d", vndb.Dropped}, {"w", vndb.Wishlist}}

// vnCustomLabels are the labels toggled with 1-9 in the label picker.
func (m *model) vnCustomLabels() []vndb.Label {
	var out []vndb.Label
	for _, l := range m.vs.labels {
		if l.ID > vndb.Voted {
			out = append(out, l)
		}
	}
	if len(out) > 9 {
		out = out[:9]
	}
	return out
}

func (m *model) vnLabelName(id int) string {
	for _, l := range m.vs.labels {
		if l.ID == id {
			return l.Label
		}
	}
	return vnlist.DefaultLabels[id]
}

func (m *model) vnLabelChoice(s string) tea.Cmd {
	r := m.vnCurrent()
	if r == nil {
		return nil
	}
	if s == "x" {
		if r.OnList {
			m.modal = modalDelete
		}
		return nil
	}
	for _, sk := range vnStatusKeys {
		if sk.key == s {
			var unset []int
			for _, o := range vnStatusKeys {
				if o.label != sk.label {
					unset = append(unset, o.label)
				}
			}
			return m.vnSet(r, store.VNEdit{LabelsSet: []int{sk.label}, LabelsUnset: unset}, m.vnLabelName(sk.label))
		}
	}
	if n, err := strconv.Atoi(s); err == nil && n >= 1 {
		if cl := m.vnCustomLabels(); n <= len(cl) {
			l := cl[n-1]
			if r.Has(l.ID) {
				return m.vnSet(r, store.VNEdit{LabelsUnset: []int{l.ID}}, "−"+l.Label)
			}
			return m.vnSet(r, store.VNEdit{LabelsSet: []int{l.ID}}, "+"+l.Label)
		}
	}
	return nil
}

// vnSet sends an edit to the daemon and applies it locally right away.
func (m *model) vnSet(r *vnrow, e store.VNEdit, desc string) tea.Cmd {
	if m.daemonErr {
		m.flash("daemon not running — start it to edit (systemctl --user start anisync)", true)
		return nil
	}
	meta := *r
	meta.OnList, meta.Labels, meta.Vote, meta.Started, meta.Finished, meta.Notes, meta.Pending = false, nil, 0, "", "", "", false
	cur := store.VNSnapshot{}
	ent := m.vnEntry(r.ID)
	if ent != nil {
		cur = store.VNSnapshot{Exists: true, Labels: append([]int(nil), ent.Labels...), Vote: ent.Vote, Started: ent.Started,
			Finished: ent.Finished, Notes: ent.Notes}
	}
	next, del, skip := daemon.ApplyVNEdit(cur, &e, netx.Today())
	if skip != "" && !del {
		m.flash(r.Title+": "+skip, false)
		return nil
	}
	if del {
		out := m.vs.list[:0]
		for _, x := range m.vs.list {
			if x.ID != r.ID {
				out = append(out, x)
			}
		}
		m.vs.list = out
	} else {
		if ent == nil {
			cp := meta
			ent = &cp
			ent.Added = time.Now().Unix()
			m.vs.list = append(m.vs.list, ent)
		}
		ent.OnList = true
		ent.Labels, ent.Vote, ent.Started, ent.Finished, ent.Notes = next.Labels, next.Vote, next.Started, next.Finished, next.Notes
		ent.Pending, ent.Updated = true, time.Now().Unix()
	}
	a := daemon.VNSetArgs{ID: r.ID, Title: r.Title, VNEdit: e, Meta: &meta}
	return vnSetCmd(a, r.Title+": "+desc)
}

// ---------------------------------------------------------------- now

func (m *model) vnRunning() []daemon.VNNow {
	if m.st == nil {
		return nil
	}
	return m.st.VNNow
}

func (m *model) vnPending() []*store.Pending {
	var out []*store.Pending
	for _, p := range m.pending {
		if p.Kind == "vn" {
			out = append(out, p)
		}
	}
	return out
}

func (m *model) vnNowCount() int { return len(m.vnRunning()) + len(m.vnPending()) }

func (m *model) vnNowSelected() *daemon.VNNow {
	rs := m.vnRunning()
	if m.vs.nSel < len(rs) {
		return &rs[m.vs.nSel]
	}
	return nil
}

func (m *model) vnNowPending() *store.Pending {
	i := m.vs.nSel - len(m.vnRunning())
	if ps := m.vnPending(); i >= 0 && i < len(ps) {
		return ps[i]
	}
	return nil
}

func (m *model) vnNowKey(s string) tea.Cmd {
	if cmd, ok := m.move(s, &m.vs.nSel, m.vnNowCount()); ok {
		return cmd
	}
	n, pend := m.vnNowSelected(), m.vnNowPending()
	switch s {
	case "f", "F":
		if n != nil {
			return m.vnAskFinish()
		}
	case "m":
		if n != nil {
			return m.vnStartResolve(n.Key, cleanFolder(n.Folder))
		}
		if pend != nil {
			return m.vnStartResolve(pend.Key, cleanFolder(pend.Query))
		}
	case "x":
		if pend != nil {
			return ignoreCmd(pend.Key, pend.Query)
		}
	case "enter":
		if pend != nil {
			return m.vnStartResolve(pend.Key, cleanFolder(pend.Query))
		}
		if n != nil && n.VNDBID != "" {
			return m.openDetail()
		}
		if n != nil {
			return m.vnStartResolve(n.Key, cleanFolder(n.Folder))
		}
	case "ctrl+r":
		return tea.Batch(statusCmd(), pendingCmd())
	}
	cmd, _ := m.vnActionKey(s)
	return cmd
}

func (m *model) vnAskFinish() tea.Cmd {
	if m.daemonErr {
		m.flash("daemon not running", true)
		return nil
	}
	m.modal = modalFinish
	m.modalInput.SetValue("")
	m.modalInput.Placeholder = "8.5"
	m.modalInput.Focus()
	return textinput.Blink
}

// vnStartResolve opens a VN search to pick the VN for a folder.
func (m *model) vnStartResolve(key, q string) tea.Cmd {
	m.resolving = &resolveTarget{Key: key, Query: q}
	m.tab = tabSearch
	m.closeDetail()
	m.vs.f = query.VNFilter{Search: q}
	return m.vnRunSearch()
}

func (m *model) vnChoose() tea.Cmd {
	vs := &m.vs
	if vs.sSel >= len(vs.results) || m.resolving == nil {
		return nil
	}
	r := vs.results[vs.sSel]
	t := m.resolving
	m.resolving = nil
	m.tab = tabNow
	return mapCmd(daemon.ResolveArgs{Key: t.Key, VNDBID: r.ID, Title: r.Title},
		fmt.Sprintf("“%s” → %s (queued updates follow)", t.Query, r.Title))
}

// ---------------------------------------------------------------- search

func (m *model) vnRunSearch() tea.Cmd {
	vs := &m.vs
	vs.seq++
	vs.loading, vs.results, vs.sSel, vs.more, vs.warns = true, nil, 0, false, nil
	if !m.typing {
		vs.input.SetValue(query.FormatVN(vs.f))
	}
	return m.vnSearchCmd(vs.f, 1, vs.seq)
}

func (m *model) vnLoadMore() tea.Cmd {
	vs := &m.vs
	if vs.more && !vs.loading && vs.sSel >= len(vs.results)-4 {
		vs.loading = true
		return m.vnSearchCmd(vs.f, vs.page+1, vs.seq)
	}
	return nil
}

// ---------------------------------------------------------------- messages

type vnListMsg struct {
	list   *vnlist.List
	err    string
	synced bool
}
type vnSearchMsg struct {
	seq, page int
	res       vndb.Page
	err       error
	tags      []query.VNTag // tag names resolved for this search
	missing   []string
}
type vnDetailMsg struct {
	key uint32
	v   *vndb.VN
	err error
}
type vnVocabMsg struct{ v *query.VNVocab }
type vnDebounceMsg struct{ id uint32 }

// vnUpdate handles VN messages; ok=false if msg isn't one.
func (m *model) vnUpdate(msg tea.Msg) (tea.Cmd, bool) {
	vs := &m.vs
	switch msg := msg.(type) {
	case vnListMsg:
		if msg.list != nil {
			vs.list, vs.loaded = msg.list.Entries, true
			if len(msg.list.Labels) > 0 {
				vs.labels = msg.list.Labels
			}
			if !msg.list.Fetched.IsZero() {
				vs.fetched = msg.list.Fetched
			}
			if n := len(m.vnVisible()); vs.lSel >= n {
				vs.lSel = max(0, n-1)
			}
		}
		if msg.synced || msg.err != "" {
			vs.syncing = false
			vs.err = msg.err
		}
		if m.vn && m.tab == tabList && msg.list != nil {
			return m.selChanged(), true
		}
		return nil, true

	case vnSearchMsg:
		if msg.seq != vs.seq {
			return nil, true
		}
		for _, t := range msg.tags {
			if vs.vocab == nil {
				vs.vocab = query.NewVNVocab(nil)
			}
			vs.vocab.Add(t)
		}
		vs.loading = false
		if msg.err != nil {
			m.flash("VNDB search failed: "+msg.err.Error(), true)
			return nil, true
		}
		vs.warns = nil
		for _, t := range msg.missing {
			vs.warns = append(vs.warns, fmt.Sprintf("unknown tag %q (ignored)", t))
		}
		var rows []vnrow
		for i := range msg.res.Results {
			rows = append(rows, vnlist.FromVN(&msg.res.Results[i]))
		}
		if msg.page == 1 {
			vs.results, vs.sSel, vs.total = rows, 0, msg.res.Count
		} else {
			vs.results = append(vs.results, rows...)
		}
		vs.page, vs.more = msg.page, msg.res.More
		if m.vn && m.tab == tabSearch {
			return m.selChanged(), true
		}
		return nil, true

	case vnDetailMsg:
		delete(vs.detailReq, msg.key)
		if msg.err != nil {
			vs.detailErr[msg.key] = msg.err.Error()
			return nil, true
		}
		vs.details[msg.key] = msg.v
		delete(vs.detailErr, msg.key)
		for _, t := range msg.v.Tags {
			if vs.vocab == nil {
				vs.vocab = query.NewVNVocab(nil)
			}
			vs.vocab.Add(query.VNTag{ID: t.ID, Name: t.Name})
		}
		if n := len(vs.peek); n > 0 && vs.peek[n-1].Key() == msg.key {
			vs.peek[n-1] = vnlist.FromVN(msg.v)
		}
		if r := m.vnCurrent(); m.vn && r != nil && r.Key() == msg.key {
			return m.vnCover(*r), true
		}
		return nil, true

	case vnVocabMsg:
		if msg.v != nil {
			if vs.vocab != nil {
				for k, t := range vs.vocab.Tags {
					if _, ok := msg.v.Tags[k]; !ok {
						msg.v.Tags[k] = t
					}
				}
			}
			vs.vocab = msg.v
		}
		return nil, true

	case vnDebounceMsg:
		r := m.vnCurrent()
		if !m.vn || r == nil || r.Key() != msg.id {
			return nil, true
		}
		var cmds []tea.Cmd
		if vs.details[msg.id] == nil && !vs.detailReq[msg.id] {
			vs.detailReq[msg.id] = true
			cmds = append(cmds, vnDetailCmd(r.ID, msg.id))
		}
		cmds = append(cmds, m.vnCover(*r))
		return tea.Batch(cmds...), true
	}
	return nil, false
}

// ---------------------------------------------------------------- commands

func vnLocalListCmd() tea.Cmd {
	return func() tea.Msg {
		l, err := vnlist.Load()
		if err != nil {
			return vnListMsg{err: err.Error()}
		}
		return vnListMsg{list: l}
	}
}

func daemonVNListCmd() tea.Cmd {
	return func() tea.Msg {
		var rep daemon.VNListReply
		if err := call("vnlist", nil, &rep); err != nil {
			return nil
		}
		return vnListMsg{list: &rep.List}
	}
}

// vnSyncCmd refreshes the VN list: through the daemon if it runs with VNDB
// enabled (it keeps the file), else directly with your token.
func (m *model) vnSyncCmd() tea.Cmd {
	tok, user := m.vs.token, m.vs.user
	return func() tea.Msg {
		var rep daemon.VNListReply
		err := call("vnlist-refresh", nil, &rep)
		if err == nil && (rep.Error == "" || !strings.Contains(rep.Error, "isn't set up") || tok == "") {
			return vnListMsg{list: &rep.List, err: rep.Error, synced: rep.Error == ""}
		}
		if err != nil && !errors.Is(err, ipc.ErrNotRunning) {
			return vnListMsg{err: err.Error()}
		}
		if tok == "" || user == "" {
			return vnListMsg{err: "not logged in to VNDB (run: anisync auth vndb)"}
		}
		ctx, cancel := ctx20()
		defer cancel()
		c := &vndb.Client{Token: tok}
		labels, err := c.Labels(ctx, user)
		if err != nil {
			return vnListMsg{err: err.Error()}
		}
		items, err := c.UList(ctx, user)
		if err != nil {
			return vnListMsg{err: err.Error()}
		}
		l := &vnlist.List{Fetched: time.Now(), User: user, Labels: labels, Entries: vnlist.FromUList(items)}
		if old, err := vnlist.Load(); err == nil { // keep playtime the daemon recorded
			for _, e := range l.Entries {
				if o := old.Find(e.ID); o != nil {
					e.Playtime = o.Playtime
				}
			}
		}
		_ = l.Save()
		return vnListMsg{list: l, synced: true}
	}
}

func (m *model) vnSearchCmd(f query.VNFilter, page, seq int) tea.Cmd {
	known := map[string]string{}
	if m.vs.vocab != nil {
		for k, t := range m.vs.vocab.Tags {
			known[k] = t.ID
		}
	}
	return func() tea.Msg {
		ctx, cancel := ctx20()
		defer cancel()
		c := &vndb.Client{}
		var found []query.VNTag
		lookup := func(n string) string {
			if id := (&query.VNVocab{}).TagID(n); id != "" {
				return id
			}
			return known[strings.ToLower(n)]
		}
		_, missing := query.VNFilters(f, lookup)
		var unknown []string
		for i, n := range missing {
			if i >= 5 {
				unknown = append(unknown, n)
				continue
			}
			t, err := c.FindTag(ctx, n)
			if err != nil {
				return vnSearchMsg{seq: seq, page: page, err: err}
			}
			if t == nil {
				unknown = append(unknown, n)
				continue
			}
			known[strings.ToLower(n)] = t.ID
			found = append(found, query.VNTag{ID: t.ID, Name: t.Name})
		}
		filters, _ := query.VNFilters(f, lookup)
		s, rev := query.VNSortParams(f)
		res, err := c.Browse(ctx, vndb.Query{Filters: filters, Sort: s, Reverse: rev, Page: page, Results: 30})
		return vnSearchMsg{seq: seq, page: page, res: res, err: err, tags: found, missing: unknown}
	}
}

func vnDetailCmd(id string, key uint32) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctx20()
		defer cancel()
		v, err := (&vndb.Client{}).Detail(ctx, id)
		return vnDetailMsg{key: key, v: v, err: err}
	}
}

// vnVocabCmd loads the 500 most used VNDB tags (cached for a week).
func vnVocabCmd() tea.Cmd {
	return func() tea.Msg {
		path := filepath.Join(cacheDir(), "vndb_tags.json")
		var c struct {
			Tags    []query.VNTag
			Fetched time.Time
		}
		if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &c) == nil && time.Since(c.Fetched) < 7*24*time.Hour {
			return vnVocabMsg{query.NewVNVocab(c.Tags)}
		}
		ctx, cancel := ctx20()
		defer cancel()
		var tags []query.VNTag
		for page := 1; page <= 5; page++ {
			res, more, err := (&vndb.Client{}).Tags(ctx, page)
			if err != nil {
				if len(c.Tags) > 0 {
					return vnVocabMsg{query.NewVNVocab(c.Tags)}
				}
				return vnVocabMsg{nil}
			}
			for _, t := range res {
				tags = append(tags, query.VNTag{ID: t.ID, Name: t.Name})
			}
			if !more {
				break
			}
		}
		c.Tags, c.Fetched = tags, time.Now()
		if b, err := json.Marshal(c); err == nil {
			os.MkdirAll(filepath.Dir(path), 0o755)
			os.WriteFile(path, b, 0o644)
		}
		return vnVocabMsg{query.NewVNVocab(tags)}
	}
}

func vnSetCmd(a daemon.VNSetArgs, desc string) tea.Cmd {
	return func() tea.Msg {
		var id int64
		if err := call("vnset", a, &id); err != nil {
			return actionMsg{err: daemonErr(err)}
		}
		return actionMsg{text: desc, reload: true}
	}
}

func vnFinishCmd(key string, vote int, title string) tea.Cmd {
	return func() tea.Msg {
		var got string
		if err := call("vn-finish", daemon.VNFinishArgs{Key: key, Vote: vote}, &got); err != nil {
			return actionMsg{err: daemonErr(err)}
		}
		desc := title + ": marked finished"
		if vote > 0 {
			desc += fmt.Sprintf(", vote %.1f", float64(vote)/10)
		}
		return actionMsg{text: desc + " (queued)", reload: true}
	}
}

// ---------------------------------------------------------------- filter spec

const (
	vSort = iota
	vTags
	vLength
	vYear
	vRating
	vVotes
	vLang
	vOLang
	vPlatform
	vDev
)

var voteChoices = []query.Choice{{Value: "10", Label: "≥ 10 votes"}, {Value: "100", Label: "≥ 100 votes"},
	{Value: "500", Label: "≥ 500 votes"}, {Value: "1000", Label: "≥ 1000 votes"}, {Value: "5000", Label: "≥ 5000 votes"}}

var vnFields = []fieldDef{
	{label: "Sort", kind: kEnum, choices: query.VNSorts},
	{label: "Tags", kind: kTri},
	{label: "Length", kind: kMulti, choices: query.VNLengths},
	{label: "Released", kind: kRange, token: "y", placeholder: "2023  2015-2020  >2018"},
	{label: "Rating", kind: kRange, token: "rating", placeholder: "7.5-10  >8  70-90"},
	{label: "Votes", kind: kEnum, choices: voteChoices, any: true},
	{label: "Language", kind: kMulti, choices: query.VNLangs},
	{label: "Original", kind: kEnum, choices: query.VNLangs, any: true},
	{label: "Platform", kind: kMulti, choices: query.VNPlatforms},
	{label: "Developer", kind: kRange, token: "dev", placeholder: "key, type-moon, p24", hint: "type names"},
}

// vnSpec is the VNDB search filter (m.vs.f) for the filter editor.
type vnSpec struct{ m *model }

func (s vnSpec) fields() []fieldDef { return vnFields }
func (s vnSpec) sortField() int     { return vSort }
func (s vnSpec) tagField() int      { return vTags }
func (s vnSpec) firstFilter() int   { return vTags }
func (s vnSpec) format() string     { return query.FormatVN(s.m.vs.f) }
func (s vnSpec) empty() bool        { return s.m.vs.f.Empty() }

func (s vnSpec) clearFilters() {
	f := &s.m.vs.f
	*f = query.VNFilter{Search: f.Search, Sort: f.Sort}
}

func (s vnSpec) enumGet(i int) string {
	f := &s.m.vs.f
	switch i {
	case vSort:
		st, rev := query.VNSortParams(*f)
		if !rev && st != "searchrank" && st != "title" {
			st += "_asc"
		}
		return st
	case vVotes:
		if f.VotesMin > 0 {
			return strconv.Itoa(f.VotesMin)
		}
	case vOLang:
		return f.OLang
	}
	return ""
}

func (s vnSpec) enumSet(i int, v string) {
	f := &s.m.vs.f
	switch i {
	case vSort:
		f.Sort = v
	case vVotes:
		f.VotesMin, _ = strconv.Atoi(v)
	case vOLang:
		f.OLang = v
	}
}

func (s vnSpec) multiPtr(i int) *[]string {
	f := &s.m.vs.f
	switch i {
	case vLength:
		return &f.Lengths
	case vLang:
		return &f.Langs
	case vPlatform:
		return &f.Platforms
	}
	return nil
}

func (s vnSpec) triPtrs(int) (inc, exc *[]string) { return &s.m.vs.f.Tags, &s.m.vs.f.NotTags }

func (s vnSpec) pickOptions(int) []string {
	names := s.m.vs.vocab.TagList()
	sort.Strings(names)
	return names
}

func (s vnSpec) rangeLabel(i int) string {
	f := &s.m.vs.f
	switch i {
	case vYear:
		if f.Year > 0 {
			return fmt.Sprint(f.Year)
		}
		return rangeLabel(f.YearFrom, f.YearTo, "")
	case vRating:
		lo, hi := float64(f.RatingMin)/10, float64(f.RatingMax)/10
		switch {
		case lo > 0 && hi > 0 && lo == hi:
			return fmt.Sprintf("%.1f", lo)
		case lo > 0 && hi > 0:
			return fmt.Sprintf("%.1f–%.1f", lo, hi)
		case lo > 0:
			return fmt.Sprintf("≥ %.1f", lo)
		case hi > 0:
			return fmt.Sprintf("≤ %.1f", hi)
		}
	case vDev:
		return strings.Join(f.Devs, ", ")
	}
	return ""
}

func (s vnSpec) rangeText(i int) string {
	if i == vDev {
		return strings.Join(s.m.vs.f.Devs, ", ")
	}
	return tokenValue(s.format(), vnFields[i].token)
}

func (s vnSpec) clearRange(i int) {
	f := &s.m.vs.f
	switch i {
	case vYear:
		f.Year, f.YearFrom, f.YearTo = 0, 0, 0
	case vRating:
		f.RatingMin, f.RatingMax = 0, 0
	case vDev:
		f.Devs = nil
	}
}

func (s vnSpec) setRange(i int, text string) error {
	s.clearRange(i)
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	f := &s.m.vs.f
	if i == vDev {
		for _, d := range strings.Split(text, ",") {
			if d = strings.TrimSpace(strings.Trim(d, `"`)); d != "" {
				f.Devs = appendUnique(f.Devs, d)
			}
		}
		return nil
	}
	nf, warns := query.ParseVN(vnFields[i].token+":"+text, s.m.vs.vocab)
	if len(warns) > 0 {
		return fmt.Errorf("%s", warns[0])
	}
	switch i {
	case vYear:
		f.Year, f.YearFrom, f.YearTo = nf.Year, nf.YearFrom, nf.YearTo
	case vRating:
		f.RatingMin, f.RatingMax = nf.RatingMin, nf.RatingMax
	}
	return nil
}

func (s vnSpec) cycleSort(d int) string {
	cur := s.enumGet(vSort)
	i := 0
	for j, c := range query.VNSorts {
		if c.Value == cur {
			i = j
		}
	}
	i = (i + d + len(query.VNSorts)) % len(query.VNSorts)
	s.m.vs.f.Sort = query.VNSorts[i].Value
	return query.VNSorts[i].Label
}

func fmtPlaytime(sec int64) string {
	if sec <= 0 {
		return ""
	}
	d := time.Duration(sec) * time.Second
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
