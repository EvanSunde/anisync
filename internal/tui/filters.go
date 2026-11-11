package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/evansunde/anisync/internal/query"
)

// The filter editor and chips work on a filterSpec, so the anime (AniList)
// and visual novel (VNDB) searches share the same UX.
type filterSpec interface {
	fields() []fieldDef
	sortField() int
	tagField() int    // tri field whose chips get a "#"
	firstFilter() int // field the "+ filters" button opens
	format() string   // the filter as query text
	empty() bool      // no filters besides text and sort
	clearFilters()    // keeps text and sort
	enumGet(i int) string
	enumSet(i int, v string) // "" = any (or default sort)
	multiPtr(i int) *[]string
	triPtrs(i int) (inc, exc *[]string)
	pickOptions(i int) []string // names for tri fields
	rangeLabel(i int) string
	rangeText(i int) string // editable text of a typed field
	clearRange(i int)
	setRange(i int, text string) error
	cycleSort(d int) string // returns the new sort's label
}

func (m *model) spec() filterSpec {
	if m.vn {
		return vnSpec{m}
	}
	return animeSpec{m}
}

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
	// typed fields (kRange): input placeholder and hint
	placeholder string
	hint        string
}

// value renders a field's current value ("" = not set).
func (m *model) value(i int) string {
	sp := m.spec()
	d := sp.fields()[i]
	switch d.kind {
	case kEnum:
		v := sp.enumGet(i)
		if v == "" {
			return ""
		}
		if c, ok := query.ByValue(d.choices, v); ok {
			return c.Label
		}
		return v
	case kMulti:
		var out []string
		for _, v := range *sp.multiPtr(i) {
			if c, ok := query.ByValue(d.choices, v); ok {
				out = append(out, c.Label)
			}
		}
		return strings.Join(out, ", ")
	case kTri:
		inc, exc := sp.triPtrs(i)
		var out []string
		for _, v := range *inc {
			out = append(out, "+"+v)
		}
		for _, v := range *exc {
			out = append(out, "−"+v)
		}
		return strings.Join(out, " ")
	case kRange:
		return sp.rangeLabel(i)
	}
	return ""
}

func (m *model) clearField(i int) {
	sp := m.spec()
	switch sp.fields()[i].kind {
	case kEnum:
		sp.enumSet(i, "")
	case kMulti:
		*sp.multiPtr(i) = nil
	case kTri:
		inc, exc := sp.triPtrs(i)
		*inc, *exc = nil, nil
	case kRange:
		sp.clearRange(i)
	}
}

// cycleEnum steps an enum field (with "Any" first when allowed).
func (m *model) cycleEnum(i, d int) {
	sp := m.spec()
	def := sp.fields()[i]
	vals := []string{}
	if def.any {
		vals = append(vals, "")
	}
	for _, c := range def.choices {
		vals = append(vals, c.Value)
	}
	cur := sp.enumGet(i)
	k := 0
	for j, v := range vals {
		if v == cur {
			k = j
		}
	}
	sp.enumSet(i, vals[(k+d+len(vals))%len(vals)])
}

func (m *model) cycleSort(d int) {
	m.flash("sort: "+m.spec().cycleSort(d), false)
}

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
	p.input.CharLimit = 80
	m.panel = p
	m.closeDetail()
}

func (m *model) panelActivate(i int, d int) tea.Cmd {
	p := m.panel
	p.sel = i
	sp := m.spec()
	switch sp.fields()[i].kind {
	case kEnum:
		if i == sp.sortField() {
			m.cycleSort(d)
		} else {
			m.cycleEnum(i, d)
		}
		return m.filterChanged()
	case kRange:
		p.editing = true
		p.input.SetValue(sp.rangeText(i))
		p.input.Placeholder = sp.fields()[i].placeholder
		p.input.CursorEnd()
		p.input.Focus()
		return textinput.Blink
	default:
		m.openPicker(i)
		return textinput.Blink
	}
}

func (m *model) openPicker(i int) {
	sp := m.spec()
	pk := &picker{field: i, tri: sp.fields()[i].kind == kTri}
	if pk.tri {
		for _, n := range sp.pickOptions(i) {
			pk.opts = append(pk.opts, query.Choice{Value: n, Label: n})
		}
	} else {
		pk.opts = sp.fields()[i].choices
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
	sp := m.spec()
	if pk.tri {
		inc, exc := sp.triPtrs(pk.field)
		if contains(*inc, v) {
			return 1
		}
		if contains(*exc, v) {
			return 2
		}
		return 0
	}
	if contains(*sp.multiPtr(pk.field), v) {
		return 1
	}
	return 0
}

func (m *model) pickToggle(pk *picker, v string, back bool) tea.Cmd {
	st := m.pickState(pk, v)
	sp := m.spec()
	if pk.tri {
		inc, exc := sp.triPtrs(pk.field)
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
		l := sp.multiPtr(pk.field)
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
	sp := m.spec()
	fields := sp.fields()
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
			if err := sp.setRange(p.sel, p.input.Value()); err != nil {
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
		sp.clearFilters()
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
	sp := m.spec()
	fields := sp.fields()
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
	if c, ok := query.ByValue(fields[sp.sortField()].choices, sp.enumGet(sp.sortField())); ok {
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
		if i == sp.sortField() {
			continue
		}
		i := i
		switch d.kind {
		case kTri:
			inc, exc := sp.triPtrs(i)
			pre := ""
			if i == sp.tagField() {
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
	segs = append(segs, seg{text: sButton.Render("+ filters (f)"), click: onClick(func() tea.Cmd { m.openPanel(sp.firstFilter()); return nil })})
	if !sp.empty() {
		segs = append(segs, seg{text: sDim.Render(" clear all"), click: onClick(func() tea.Cmd { return m.searchKey("X") })})
	}
	wrapSegs(&b, " ", segs, " ", w)
	return b
}

func (m *model) panelBlock(b block, w, h int) block {
	p := m.panel
	fields := m.spec().fields()
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
			if d.hint != "" {
				hint = d.hint
			}
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
	nres, more := len(m.results), m.hasNext
	if m.vn {
		nres, more = len(m.vs.results), m.vs.more
	}
	n := fmt.Sprintf("%d", nres)
	if more {
		n += "+"
	}
	b.add("", sDim.Render(" → "+n+" results"))
	b.addSegs(button("done (esc)", func() tea.Cmd { m.panel = nil; return nil }))
	return b
}
