package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/daemon"
	"github.com/evansunde/anisync/internal/query"
)

// ---------------------------------------------------------------- searching

func (m *model) runSearch() tea.Cmd {
	m.seq++
	m.loading, m.results, m.sSel, m.hasNext = true, nil, 0, false
	if !m.typing {
		m.input.SetValue(query.Format(m.f))
	}
	return m.searchCmd(m.f, 1, m.seq)
}

// filterChanged re-runs the search shortly after the last filter edit.
func (m *model) filterChanged() tea.Cmd {
	m.input.SetValue(query.Format(m.f))
	m.liveSeq++
	return liveSearch(m.liveSeq)
}

func (m *model) loadMore() tea.Cmd {
	if m.hasNext && !m.loading && m.sSel >= len(m.results)-4 {
		m.loading = true
		return m.searchCmd(m.f, m.page+1, m.seq)
	}
	return nil
}

func (m *model) searchKey(s string) tea.Cmd {
	switch s {
	case "/", "i":
		m.typing = true
		m.input.Focus()
		m.input.CursorEnd()
		return textinput.Blink
	case "f":
		m.openPanel(0)
		return nil
	case "X":
		text, sort := m.f.Search, m.f.Sort
		m.f = anilist.Filter{Search: text, Sort: sort}
		m.flash("filters cleared", false)
		return m.runSearch()
	case "S":
		m.cycleSort(1)
		return m.runSearch()
	case "esc":
		if m.resolving != nil {
			m.resolving = nil
			m.tab = tabNow
			m.flash("cancelled", false)
		}
		return nil
	case "enter":
		if m.resolving != nil {
			return m.choose()
		}
		return m.openDetail()
	case "ctrl+r":
		return m.runSearch()
	}
	if cmd, ok := m.move(s, &m.sSel, len(m.results)); ok {
		return tea.Batch(cmd, m.loadMore())
	}
	cmd, _ := m.actionKey(s)
	return cmd
}

func (m *model) cycleSort(d int) {
	cur := "SEARCH_MATCH"
	if len(m.f.Sort) > 0 {
		cur = m.f.Sort[0]
	} else if m.f.Search == "" {
		cur = "POPULARITY_DESC"
	}
	i := 0
	for j, c := range query.Sorts {
		if c.Value == cur {
			i = j
		}
	}
	i = (i + d + len(query.Sorts)) % len(query.Sorts)
	m.f.Sort = []string{query.Sorts[i].Value}
	m.flash("sort: "+query.Sorts[i].Label, false)
}

func (m *model) choose() tea.Cmd {
	if m.sSel >= len(m.results) || m.resolving == nil {
		return nil
	}
	r := m.results[m.sSel]
	t := m.resolving
	m.resolving = nil
	m.tab = tabNow
	return mapCmd(daemon.ResolveArgs{Key: t.Key, AnilistID: r.AnilistID, MalID: r.MalID, Title: r.Title},
		fmt.Sprintf("“%s” → %s (queued episodes follow)", t.Query, r.Title))
}

// ---------------------------------------------------------------- filter fields

type fieldKind int

const (
	kEnum fieldKind = iota
	kMulti
	kRange
	kTri
)

type fieldDef struct {
	label   string
	kind    fieldKind
	choices []query.Choice
	any     bool
	token   string // range: query key
}

var popChoices = []query.Choice{{Value: "1000", Label: "≥ 1k users"}, {Value: "10000", Label: "≥ 10k users"},
	{Value: "50000", Label: "≥ 50k users"}, {Value: "100000", Label: "≥ 100k users"}, {Value: "250000", Label: "≥ 250k users"},
	{Value: "500000", Label: "≥ 500k users"}}

var yesNo = []query.Choice{{Value: "yes", Label: "Yes"}, {Value: "no", Label: "No"}}

var fields = []fieldDef{
	{label: "Sort", kind: kEnum, choices: query.Sorts},
	{label: "Format", kind: kMulti, choices: query.Formats},
	{label: "Status", kind: kEnum, choices: query.Statuses, any: true},
	{label: "Season", kind: kEnum, choices: query.Seasons, any: true},
	{label: "Year", kind: kRange, token: "y"},
	{label: "Score", kind: kRange, token: "score"},
	{label: "Episodes", kind: kRange, token: "eps"},
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

func (m *model) enumGet(i int) string {
	f := &m.f
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

func (m *model) enumSet(i int, v string) {
	f := &m.f
	switch i {
	case fSort:
		f.Sort = []string{v}
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

func (m *model) multiPtr(i int) *[]string {
	switch i {
	case fFormat:
		return &m.f.Formats
	case fSource:
		return &m.f.Sources
	}
	return nil
}

func (m *model) triPtrs(i int) (inc, exc *[]string) {
	if i == fGenres {
		return &m.f.Genres, &m.f.NotGenres
	}
	return &m.f.Tags, &m.f.NotTags
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

// value renders a field's current value ("" = not set).
func (m *model) value(i int) string {
	f := &m.f
	d := fields[i]
	switch d.kind {
	case kEnum:
		v := m.enumGet(i)
		if v == "" {
			return ""
		}
		if c, ok := query.ByValue(d.choices, v); ok {
			return c.Label
		}
		return v
	case kMulti:
		var out []string
		for _, v := range *m.multiPtr(i) {
			if c, ok := query.ByValue(d.choices, v); ok {
				out = append(out, c.Label)
			}
		}
		return strings.Join(out, ", ")
	case kTri:
		inc, exc := m.triPtrs(i)
		var out []string
		for _, v := range *inc {
			out = append(out, "+"+v)
		}
		for _, v := range *exc {
			out = append(out, "−"+v)
		}
		return strings.Join(out, " ")
	case kRange:
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
	}
	return ""
}

func (m *model) clearField(i int) {
	f := &m.f
	switch fields[i].kind {
	case kEnum:
		if i == fSort {
			f.Sort = nil
		} else {
			m.enumSet(i, "")
		}
	case kMulti:
		*m.multiPtr(i) = nil
	case kTri:
		inc, exc := m.triPtrs(i)
		*inc, *exc = nil, nil
	case kRange:
		switch i {
		case fYear:
			f.Year, f.YearFrom, f.YearTo = 0, 0, 0
		case fScore:
			f.ScoreMin, f.ScoreMax = 0, 0
		case fEps:
			f.EpsMin, f.EpsMax = 0, 0
		}
	}
}

// cycleEnum steps an enum field (with "Any" first when allowed).
func (m *model) cycleEnum(i, d int) {
	def := fields[i]
	vals := []string{}
	if def.any {
		vals = append(vals, "")
	}
	for _, c := range def.choices {
		vals = append(vals, c.Value)
	}
	cur := m.enumGet(i)
	k := 0
	for j, v := range vals {
		if v == cur {
			k = j
		}
	}
	m.enumSet(i, vals[(k+d+len(vals))%len(vals)])
}

// rangeText is the editable text of a range field ("2018-2024", ">7").
func (m *model) rangeText(i int) string {
	s := query.Format(m.f)
	pre := fields[i].token + ":"
	for _, tok := range strings.Fields(s) {
		if strings.HasPrefix(tok, pre) {
			return strings.TrimPrefix(tok, pre)
		}
	}
	return ""
}

func (m *model) setRange(i int, text string) error {
	m.clearField(i)
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	nf, warns := query.Parse(fields[i].token+":"+text, m.vocab)
	if len(warns) > 0 {
		return fmt.Errorf("%s", warns[0])
	}
	switch i {
	case fYear:
		m.f.Year, m.f.YearFrom, m.f.YearTo = nf.Year, nf.YearFrom, nf.YearTo
	case fScore:
		m.f.ScoreMin, m.f.ScoreMax = nf.ScoreMin, nf.ScoreMax
	case fEps:
		m.f.EpsMin, m.f.EpsMax = nf.EpsMin, nf.EpsMax
	}
	return nil
}

// ---------------------------------------------------------------- filter panel

type filterPanel struct {
	sel     int
	editing bool
	input   textinput.Model
	picker  *picker
}

type picker struct {
	field  int
	tri    bool
	opts   []query.Choice // Value = name for genres/tags
	filter textinput.Model
	sel    int
}

func (m *model) openPanel(field int) {
	p := &filterPanel{sel: field}
	p.input = textinput.New()
	p.input.Prompt = ""
	p.input.CharLimit = 16
	m.panel = p
	m.closeDetail()
}

func (m *model) panelActivate(i int, d int) tea.Cmd {
	p := m.panel
	p.sel = i
	switch fields[i].kind {
	case kEnum:
		if i == fSort {
			m.cycleSort(d)
		} else {
			m.cycleEnum(i, d)
		}
		return m.filterChanged()
	case kRange:
		p.editing = true
		p.input.SetValue(m.rangeText(i))
		p.input.Placeholder = map[int]string{fYear: "2023  2015-2020  >2018", fScore: "75-100  >7.5", fEps: "<13  12  24-30"}[i]
		p.input.CursorEnd()
		p.input.Focus()
		return textinput.Blink
	default:
		m.openPicker(i)
		return textinput.Blink
	}
}

func (m *model) openPicker(i int) {
	pk := &picker{field: i, tri: fields[i].kind == kTri}
	switch i {
	case fGenres, fTags:
		var names []string
		if m.vocab != nil {
			if i == fGenres {
				names = m.vocab.GenreList()
			} else {
				names = m.vocab.TagList()
			}
		}
		for _, n := range names {
			pk.opts = append(pk.opts, query.Choice{Value: n, Label: n})
		}
	default:
		pk.opts = fields[i].choices
	}
	pk.filter = textinput.New()
	pk.filter.Prompt = " type to filter › "
	pk.filter.Focus()
	m.panel.picker = pk
}

func (pk *picker) visible() []query.Choice {
	q := strings.ToLower(strings.TrimSpace(pk.filter.Value()))
	if q == "" {
		return pk.opts
	}
	var out []query.Choice
	for _, o := range pk.opts {
		if strings.Contains(strings.ToLower(o.Label), q) {
			out = append(out, o)
		}
	}
	return out
}

// state: 0 none, 1 include, 2 exclude
func (m *model) pickState(pk *picker, v string) int {
	if pk.tri {
		inc, exc := m.triPtrs(pk.field)
		if contains(*inc, v) {
			return 1
		}
		if contains(*exc, v) {
			return 2
		}
		return 0
	}
	if contains(*m.multiPtr(pk.field), v) {
		return 1
	}
	return 0
}

func (m *model) pickToggle(pk *picker, v string, back bool) tea.Cmd {
	st := m.pickState(pk, v)
	if pk.tri {
		inc, exc := m.triPtrs(pk.field)
		*inc, *exc = remove(*inc, v), remove(*exc, v)
		next := (st + 1) % 3
		if back {
			next = (st + 2) % 3
		}
		switch next {
		case 1:
			*inc = append(*inc, v)
		case 2:
			*exc = append(*exc, v)
		}
	} else {
		l := m.multiPtr(pk.field)
		if st == 1 {
			*l = remove(*l, v)
		} else {
			*l = append(*l, v)
		}
	}
	return m.filterChanged()
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func remove(l []string, s string) []string {
	var out []string
	for _, x := range l {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func (m *model) panelKey(k tea.KeyMsg) tea.Cmd {
	p := m.panel
	s := k.String()
	if pk := p.picker; pk != nil {
		vis := pk.visible()
		switch s {
		case "esc", "enter":
			p.picker = nil
			return nil
		case "up", "ctrl+p", "ctrl+k":
			pk.sel = max(0, pk.sel-1)
			return nil
		case "down", "ctrl+n", "ctrl+j":
			pk.sel = min(len(vis)-1, pk.sel+1)
			return nil
		case " ", "tab":
			if pk.sel < len(vis) {
				return m.pickToggle(pk, vis[pk.sel].Value, false)
			}
			return nil
		case "shift+tab":
			if pk.sel < len(vis) {
				return m.pickToggle(pk, vis[pk.sel].Value, true)
			}
			return nil
		}
		var cmd tea.Cmd
		pk.filter, cmd = pk.filter.Update(k)
		pk.sel = 0
		return cmd
	}
	if p.editing {
		switch s {
		case "enter":
			p.editing = false
			if err := m.setRange(p.sel, p.input.Value()); err != nil {
				m.flash(err.Error(), true)
				return nil
			}
			return m.filterChanged()
		case "esc":
			p.editing = false
			return nil
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(k)
		return cmd
	}
	switch s {
	case "esc", "f", "q":
		m.panel = nil
	case "j", "down", "tab":
		p.sel = (p.sel + 1) % len(fields)
	case "k", "up", "shift+tab":
		p.sel = (p.sel - 1 + len(fields)) % len(fields)
	case "enter", " ", "l", "right":
		return m.panelActivate(p.sel, 1)
	case "h", "left":
		if fields[p.sel].kind == kEnum {
			return m.panelActivate(p.sel, -1)
		}
	case "x", "backspace", "delete":
		m.clearField(p.sel)
		return m.filterChanged()
	case "X":
		text, sort := m.f.Search, m.f.Sort
		m.f = anilist.Filter{Search: text, Sort: sort}
		return m.filterChanged()
	case "/":
		m.panel = nil
		return m.searchKey("/")
	}
	return nil
}

// ---------------------------------------------------------------- rendering

// chips shows the active filters; click to edit, × to remove.
func (m *model) chips(w int) block {
	var b block
	var segs []seg
	chip := func(label string, field int, st lipgloss.Style, rm func()) {
		open := onClick(func() tea.Cmd {
			m.openPanel(field)
			if fields[field].kind == kTri || fields[field].kind == kMulti {
				m.openPicker(field)
			}
			return nil
		})
		segs = append(segs, seg{text: st.Background(cChip).Render(" " + label), click: open},
			seg{text: sDim.Background(cChip).Render(" × "), glue: true, click: onClick(func() tea.Cmd { rm(); return m.filterChanged() })})
	}
	sortLabel := "Best match"
	if c, ok := query.ByValue(query.Sorts, m.enumGet(fSort)); ok {
		sortLabel = c.Label
	}
	segs = append(segs, seg{text: lipgloss.NewStyle().Foreground(cAccent).Background(cChip).Render(" ⇅ " + sortLabel + " "),
		click: func(ms tea.MouseMsg) tea.Cmd {
			d := 1
			if ms.Button == tea.MouseButtonRight {
				d = -1
			}
			m.cycleSort(d)
			return m.runSearch()
		}})
	fg := lipgloss.NewStyle().Foreground(cText)
	for i, d := range fields {
		if i == fSort {
			continue
		}
		i := i
		switch d.kind {
		case kTri:
			inc, exc := m.triPtrs(i)
			pre := ""
			if i == fTags {
				pre = "#"
			}
			for _, v := range *inc {
				v := v
				chip("+"+pre+v, i, sOK, func() { *inc = remove(*inc, v) })
			}
			for _, v := range *exc {
				v := v
				chip("−"+pre+v, i, sErr, func() { *exc = remove(*exc, v) })
			}
		default:
			if v := m.value(i); v != "" {
				chip(d.label+": "+v, i, fg, func() { m.clearField(i) })
			}
		}
	}
	segs = append(segs, seg{text: sButton.Render("+ filters (f)"), click: onClick(func() tea.Cmd { m.openPanel(fStatus); return nil })})
	if !m.f.Empty() {
		segs = append(segs, seg{text: sDim.Render(" clear all"), click: onClick(func() tea.Cmd { return m.searchKey("X") })})
	}
	wrapSegs(&b, " ", segs, " ", w)
	return b
}

func (m *model) searchBlock(w, h int) block {
	var b block
	in := m.input
	in.Width = max(10, w-6)
	prompt := sAccent.Render(" ⌕ ")
	if m.typing {
		prompt = sAccent.Render(" › ")
	}
	b.addSegs(seg{text: prompt + in.View(), click: onClick(func() tea.Cmd { return m.searchKey("/") })})
	if m.panel != nil {
		return m.panelBlock(b, w, h)
	}
	c := m.chips(w)
	for _, x := range c.hits {
		x.y += len(b.lines)
		b.hits = append(b.hits, x)
	}
	b.lines = append(b.lines, c.lines...)

	var info string
	switch {
	case m.resolving != nil:
		info = sWarn.Render(fmt.Sprintf(" Pick the show for “%s”", m.resolving.Query)) + sDim.Render("  enter on a result · esc cancels")
	case m.loading && len(m.results) == 0:
		info = sDim.Render(" searching…")
	case len(m.warns) > 0:
		info = sErr.Render(" " + strings.Join(m.warns, "; "))
	case m.hasNext:
		info = sDim.Render(fmt.Sprintf(" showing %d · more load as you scroll", len(m.results)))
	default:
		info = sDim.Render(fmt.Sprintf(" %d result%s", len(m.results), plural(len(m.results))))
	}
	b.add(info)
	rows := h - len(b.lines)
	off := 0
	if m.sSel >= rows {
		off = m.sSel - rows + 1
	}
	for i := off; i < len(m.results) && i < off+rows; i++ {
		i := i
		b.addSegs(seg{text: m.resultRow(i, w), click: onClick(func() tea.Cmd {
			if m.sSel == i {
				if m.resolving != nil {
					return m.choose()
				}
				return m.openDetail()
			}
			m.sSel = i
			m.closeDetail()
			return tea.Batch(m.selChanged(), m.loadMore())
		})})
	}
	if m.loading && len(m.results) > 0 && len(b.lines) < h {
		b.add(sDim.Render("   loading more…"))
	}
	return b
}

func (m *model) resultRow(i, w int) string {
	r := m.enrich(m.results[i])
	selected := i == m.sSel
	var right []string
	right = append(right, formatBadge(r.Format))
	when := ""
	if r.Year > 0 {
		when = fmt.Sprint(r.Year)
		if r.Season != "" && w >= 90 {
			when = strings.Title(strings.ToLower(r.Season))[:3] + " " + when
		}
	}
	right = append(right, sDim.Render(fmt.Sprintf("%8s", when)))
	right = append(right, fmt.Sprintf("%5s", epsStr(r.Episodes)+"ep"))
	if r.Score > 0 {
		right = append(right, scoreStyle(r.Score).Render(fmt.Sprintf("★%3d", r.Score)))
	} else {
		right = append(right, "    ")
	}
	if w >= 90 {
		right = append(right, sDim.Render(fmt.Sprintf("%6s", humanInt(r.Popularity))))
	}
	if r.Status != "" {
		st := ""
		if r.Status == "CURRENT" || r.Status == "REPEATING" || r.Status == "PAUSED" {
			st = fmt.Sprintf("%d/%s", r.Progress, epsStr(r.Episodes))
		}
		right = append(right, statusStyle(r.Status).Render("● "+fmt.Sprintf("%-7s", st)))
	} else {
		right = append(right, strings.Repeat(" ", 9))
	}
	rs := strings.Join(right, " ")
	num := sDim.Render(fmt.Sprintf("%3d ", i+1))
	marker := "  "
	if selected {
		marker = sAccent.Render("▸ ")
	}
	tw := w - lipgloss.Width(rs) - 8
	line := num + marker + fitTitle(r.Title, tw) + " " + rs
	if selected {
		return highlight(line, w)
	}
	return line
}

func (m *model) panelBlock(b block, w, h int) block {
	p := m.panel
	if pk := p.picker; pk != nil {
		head := fields[pk.field].label
		help := "space/click: toggle · enter: done"
		if pk.tri {
			help = "space/click: include → exclude → off · enter: done"
		}
		b.add(sHead.Render(" "+head)+"  "+sDim.Render(help), pk.filter.View())
		vis := pk.visible()
		rows := h - len(b.lines)
		off := 0
		if pk.sel >= rows {
			off = pk.sel - rows + 1
		}
		for j := off; j < len(vis) && j < off+rows; j++ {
			o := vis[j]
			j := j
			mark := sDim.Render("[ ]")
			st := lipgloss.NewStyle()
			switch m.pickState(pk, o.Value) {
			case 1:
				mark, st = sOK.Render("[+]"), sOK
			case 2:
				mark, st = sErr.Render("[−]"), sErr
			}
			line := "  " + mark + " " + st.Render(o.Label)
			if j == pk.sel {
				line = highlight(sAccent.Render("▸ ")+mark+" "+st.Render(o.Label), w)
			}
			b.addSegs(seg{text: line, click: func(ms tea.MouseMsg) tea.Cmd {
				pk.sel = j
				return m.pickToggle(pk, o.Value, ms.Button == tea.MouseButtonRight)
			}})
		}
		if len(vis) == 0 {
			b.add(sDim.Render("   no match"))
		}
		return b
	}
	b.add(sHead.Render(" Filters")+sDim.Render("  j/k move · enter/click change · ←/→ cycle · x clear · X clear all · esc close"), "")
	for i, d := range fields {
		i := i
		v := m.value(i)
		val := v
		if val == "" {
			val = sDim.Render("any")
		} else {
			val = sAccent.Render(val)
		}
		if p.editing && i == p.sel {
			val = p.input.View()
		}
		hint := ""
		switch d.kind {
		case kEnum:
			hint = "‹ ›"
		case kRange:
			hint = "type a range"
		default:
			hint = "pick…"
		}
		label := fmt.Sprintf("  %-11s", d.label)
		line := []seg{
			{text: label, click: onClick(func() tea.Cmd { p.sel = i; return nil })},
			{text: fit(val, max(10, w-len(label)-16)), click: func(ms tea.MouseMsg) tea.Cmd {
				dir := 1
				if ms.Button == tea.MouseButtonRight {
					dir = -1
				}
				return m.panelActivate(i, dir)
			}},
			{text: sDim.Render(" " + hint)},
		}
		if v != "" {
			line = append(line, seg{text: sDim.Render("  ×"), click: onClick(func() tea.Cmd { m.clearField(i); return m.filterChanged() })})
		}
		if i == p.sel {
			line[0].text = sAccent.Render("▸ ") + strings.TrimPrefix(label, "  ")
		}
		b.addSegs(line...)
	}
	n := fmt.Sprintf("%d", len(m.results))
	if m.hasNext {
		n += "+"
	}
	b.add("", sDim.Render(" → "+n+" results"))
	b.addSegs(button("done (esc)", func() tea.Cmd { m.panel = nil; return nil }))
	return b
}
