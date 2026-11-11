package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/evansunde/anisync/internal/query"
)

const bodyTop = 2 // header + rule

func (m *model) bodyHeight() int { return max(1, m.h-4) }

func (m *model) View() string {
	if m.w == 0 {
		return ""
	}
	m.hits, m.areas = nil, nil
	h := m.bodyHeight()

	var body block
	switch {
	case m.modal == modalHelp:
		body.add(m.helpLines()...)
	case m.full:
		if d, ok := m.detailPane(m.w-2, h); ok {
			body = d.shift(1, 0)
			m.areas = append(m.areas, area{0, bodyTop, m.w, bodyTop + h, m.scrollDetail})
		}
	default:
		var left func(w, h int) block
		switch {
		case m.tab == tabList && m.vn:
			left = m.vnListBlock
		case m.tab == tabList:
			left = m.listBlock
		case m.tab == tabNow && m.vn:
			left = m.vnNowBlock
		case m.tab == tabNow:
			left = m.nowBlock
		case m.vn:
			left = m.vnSearchBlock
		default:
			left = m.searchBlock
		}
		body = m.split(left, h)
	}

	lines := make([]string, h)
	for i := range lines {
		if i < len(body.lines) {
			lines[i] = fit(body.lines[i], m.w)
		} else {
			lines[i] = strings.Repeat(" ", m.w)
		}
	}
	for _, x := range body.hits {
		if x.y < h {
			x.y += bodyTop
			m.hits = append(m.hits, x)
		}
	}
	head := m.header()
	foot := m.footer()
	return strings.Join(append(append([]string{head, sDim.Render(strings.Repeat("─", m.w))}, lines...), foot...), "\n")
}

func (b block) shift(dx, dy int) block {
	var out block
	for _, l := range b.lines {
		out.lines = append(out.lines, strings.Repeat(" ", dx)+l)
	}
	for _, x := range b.hits {
		x.x0 += dx
		x.x1 += dx
		x.y += dy
		out.hits = append(out.hits, x)
	}
	return out
}

// detailPane renders the detail of the current show or VN.
func (m *model) detailPane(w, h int) (block, bool) {
	if m.vn {
		if r := m.vnCurrent(); r != nil {
			return m.vnDetailBlock(*r, w, h), true
		}
		return block{}, false
	}
	if r := m.current(); r != nil {
		return m.detailBlock(*r, w, h), true
	}
	return block{}, false
}

func (m *model) scrollDetail(dir int) tea.Cmd {
	m.dScroll = max(0, m.dScroll+dir*2)
	return nil
}

func (m *model) scrollLeft(dir int) tea.Cmd {
	if m.panel != nil {
		if pk := m.panel.picker; pk != nil {
			pk.sel = max(0, min(len(pk.visible())-1, pk.sel+dir))
		} else {
			n := len(m.spec().fields())
			m.panel.sel = (m.panel.sel + dir + n) % n
		}
		return nil
	}
	k := "j"
	if dir < 0 {
		k = "k"
	}
	m.closeDetail()
	if m.vn {
		switch m.tab {
		case tabList:
			c, _ := m.move(k, &m.vs.lSel, len(m.vnVisible()))
			return c
		case tabNow:
			c, _ := m.move(k, &m.vs.nSel, m.vnNowCount())
			return c
		default:
			c, _ := m.move(k, &m.vs.sSel, len(m.vs.results))
			return tea.Batch(c, m.loadMore())
		}
	}
	switch m.tab {
	case tabList:
		c, _ := m.move(k, &m.lSel, len(m.visibleList()))
		return c
	case tabNow:
		c, _ := m.move(k, &m.nSel, m.nowCount())
		return c
	default:
		c, _ := m.move(k, &m.sSel, len(m.results))
		return tea.Batch(c, m.loadMore())
	}
}

// split renders the left pane and, on wide terminals, the detail panel.
func (m *model) split(left func(w, h int) block, h int) block {
	if !m.wide() {
		b := left(m.w, h)
		m.areas = append(m.areas, area{0, bodyTop, m.w, bodyTop + h, m.scrollLeft})
		return b
	}
	lw := max(56, m.w*55/100)
	rw := m.w - lw - 3
	l := left(lw, h)
	r, _ := m.detailPane(rw, h)
	m.areas = append(m.areas, area{0, bodyTop, lw, bodyTop + h, m.scrollLeft}, area{lw + 1, bodyTop, m.w, bodyTop + h, m.scrollDetail})
	sep := sDim.Render(" │ ")
	if m.focusDetail {
		sep = sAccent.Render(" ┃ ")
	}
	var out block
	for i := 0; i < h; i++ {
		var a, c string
		if i < len(l.lines) {
			a = l.lines[i]
		}
		if i < len(r.lines) {
			c = r.lines[i]
		}
		out.lines = append(out.lines, fit(a, lw)+sep+c)
	}
	out.hits = append(out.hits, l.hits...)
	for _, x := range r.hits {
		x.x0 += lw + 3
		x.x1 += lw + 3
		out.hits = append(out.hits, x)
	}
	return out
}

func (m *model) header() string {
	var segs []seg
	segs = append(segs, txt(sAccent.Render(" anisync ")))
	for i, t := range tabNames {
		i := i
		label := t
		if tabID(i) == tabNow {
			n := len(m.pending)
			if m.vn {
				n = len(m.vnPending())
			}
			if n > 0 {
				label += fmt.Sprintf(" (%d)", n)
			}
		}
		st := sTabOff
		if tabID(i) == m.tab {
			st = sTabOn
		}
		segs = append(segs, seg{text: st.Render(label), click: onClick(func() tea.Cmd { return m.switchTab(tabID(i)) })})
	}
	// mode switch
	modeSeg := func(label string, vn bool) seg {
		st := sTabOff
		if m.vn == vn {
			st = sModeOn
		}
		return seg{text: st.Render(label), click: onClick(func() tea.Cmd { return m.setMode(vn) })}
	}
	segs = append(segs, txt("  "), modeSeg("Anime", false), txt(sDim.Render("│")), modeSeg("VN", true))
	var b block
	b.addSegs(segs...)
	left := b.lines[0]
	m.hits = append(m.hits, b.hits...)

	var right []seg
	switch {
	case m.daemonErr:
		right = append(right, txt(sErr.Render("daemon off ")))
	case m.st != nil:
		for _, p := range m.st.Now {
			if p.Tracked && p.State == "Playing" {
				t := p.Parsed
				if t == "" {
					t = filepath.Base(p.File)
				}
				if p.Episode > 0 {
					t += fmt.Sprintf(" %d", p.Episode)
				}
				mark := fmt.Sprintf("%.0f%%", p.Watched*100)
				if p.Counted {
					mark = "✓"
				}
				right = append(right, seg{text: sOK.Render("▶ ") + ansi.Truncate(t, 32, "…") + " " + sDim.Render(mark) + "  ",
					click: onClick(func() tea.Cmd { return m.switchTab(tabNow) })})
				break
			}
		}
		for _, v := range m.st.VNNow {
			right = append(right, seg{text: sOK.Render("▶ ") + ansi.Truncate(firstNonEmpty(v.Title, v.Folder), 28, "…") + " " +
				sDim.Render(fmtPlaytime(int64(v.Session.Seconds()))) + "  ",
				click: onClick(func() tea.Cmd { return tea.Batch(m.setMode(true), m.switchTab(tabNow)) })})
			break
		}
		if n := len(m.st.Queue); n > 0 {
			q := fmt.Sprintf("⇅ %d queued", n)
			st := sDim
			if m.st.LastErr != "" {
				q += " · offline"
				st = sWarn
			}
			right = append(right, seg{text: st.Render(q + " "), click: onClick(func() tea.Cmd { return m.switchTab(tabNow) })})
		}
	}
	rw := 0
	for _, s := range right {
		rw += lipgloss.Width(s.text)
	}
	gap := m.w - lipgloss.Width(left) - rw
	if gap < 1 {
		return fit(left, m.w)
	}
	var rb block
	rb.addSegs(right...)
	for _, x := range rb.hits {
		x.x0 += lipgloss.Width(left) + gap
		x.x1 += lipgloss.Width(left) + gap
		m.hits = append(m.hits, x)
	}
	return left + strings.Repeat(" ", gap) + rb.lines[0]
}

func (m *model) footer() []string {
	y := bodyTop + m.bodyHeight()
	var line1 block
	if m.vn && m.modal != modalNone && m.modal != modalHelp {
		line1 = m.vnFooterLine()
	} else {
		m.footerLine(&line1)
	}
	for _, x := range line1.hits {
		x.y = y
		m.hits = append(m.hits, x)
	}
	return []string{fit(line1.lines[0], m.w), fit(" "+m.hint(), m.w)}
}

func (m *model) footerLine(line1 *block) {
	switch m.modal {
	case modalStatus:
		segs := []seg{txt(sBold.Render(" Set status: "))}
		for _, sk := range statusKeys {
			sk := sk
			segs = append(segs, seg{text: sKey.Render(sk.key) + " " + statusStyle(sk.status).Render(sk.label), click: onClick(func() tea.Cmd {
				m.modal = modalNone
				return m.statusChoice(sk.key)
			})}, txt("  "))
		}
		segs = append(segs, seg{text: sKey.Render("x") + " " + sErr.Render("remove"), click: onClick(func() tea.Cmd {
			m.modal = modalNone
			return m.statusChoice("x")
		})}, txt(sDim.Render("   esc cancel")))
		line1.addSegs(segs...)
	case modalDelete:
		line1.add(sErr.Render(" Remove from your list on AniList and MAL? ") + keys("y", "yes", "esc", "no"))
	case modalProgress:
		line1.add(" Episodes watched: " + m.modalInput.View())
	case modalScore:
		line1.add(" Your score (0-10, e.g. 8.5; 0 clears): " + m.modalInput.View())
	default:
		switch {
		case m.msg == "":
			line1.add("")
		case m.msgErr:
			line1.add(sErr.Render(" ✗ " + m.msg))
		default:
			line1.add(sOK.Render(" ✓ ") + m.msg)
		}
	}
}

func (m *model) hint() string {
	var hint string
	switch {
	case m.typing:
		hint = keys("enter", "search", "esc", "cancel", "?", "syntax")
	case m.filtering:
		hint = keys("enter", "apply", "esc", "clear")
	case m.panel != nil && m.panel.picker != nil:
		hint = keys("type", "filter", "↑↓", "move", "space", "toggle", "enter", "done")
	case m.panel != nil:
		hint = keys("j/k", "field", "enter", "change", "←/→", "cycle", "x", "clear", "esc", "close")
	case m.vn && (m.focusDetail || m.full):
		hint = keys("j/k", "links", "enter", "open", "A", "add to search", "esc", "back", "s", "label", "r", "vote", "e/E", "dates", "o", "vndb.org")
	case m.vn && m.tab == tabSearch && m.resolving != nil:
		hint = keys("enter", "this is it", "/", "search again", "esc", "cancel")
	case m.vn && m.tab == tabSearch:
		hint = keys("/", "type query", "f", "filters", "S", "sort", "X", "clear", "enter", "details", "s", "add/label", "r", "vote", "v", "anime", "?", "help")
	case m.vn && m.tab == tabNow:
		hint = keys("f", "finish + vote", "m", "change match", "x", "ignore", "enter", "details/pick", "v", "anime", "?", "help")
	case m.vn:
		hint = keys("h/l", "label", "/", "filter", "S", "sort", "enter", "details", "s", "label", "r", "vote", "e/E", "dates", "ctrl+r", "sync", "v", "anime", "?", "help")
	case m.focusDetail || m.full:
		hint = keys("j/k", "links", "enter", "open", "A", "add to search", "esc", "back", "s", "status", "+/-", "ep", "r", "score", "o", "open web")
	case m.tab == tabSearch && m.resolving != nil:
		hint = keys("enter", "this is it", "/", "search again", "esc", "cancel")
	case m.tab == tabSearch:
		hint = keys("/", "type query", "f", "filters", "S", "sort", "X", "clear", "enter", "details", "s", "add/status", "+/-", "ep", "?", "help")
	case m.tab == tabNow:
		hint = keys("u", "mark watched now", "m", "change match", "x", "ignore", "enter", "details/pick", "?", "help")
	default:
		hint = keys("h/l", "section", "/", "filter", "S", "sort", "enter", "details", "s", "status", "+/-", "ep", "e", "set ep", "r", "score", "ctrl+r", "sync", "v", "VN", "?", "help")
	}
	return hint
}

// vnFooterLine is the VN modal prompt (label picker, vote, dates).
func (m *model) vnFooterLine() block {
	var line block
	switch m.modal {
	case modalStatus:
		segs := []seg{txt(sBold.Render(" Label: "))}
		for _, sk := range vnStatusKeys {
			sk := sk
			segs = append(segs, seg{text: sKey.Render(sk.key) + " " + labelStyle(sk.label).Render(m.vnLabelName(sk.label)), click: onClick(func() tea.Cmd {
				m.modal = modalNone
				return m.vnLabelChoice(sk.key)
			})}, txt("  "))
		}
		cur := m.vnCurrent()
		for i, l := range m.vnCustomLabels() {
			k := fmt.Sprint(i + 1)
			mark := "○ "
			if cur != nil && cur.Has(l.ID) {
				mark = "● "
			}
			segs = append(segs, seg{text: sKey.Render(k) + " " + labelStyle(l.ID).Render(mark+l.Label), click: onClick(func() tea.Cmd {
				m.modal = modalNone
				return m.vnLabelChoice(k)
			})}, txt("  "))
		}
		segs = append(segs, seg{text: sKey.Render("x") + " " + sErr.Render("remove"), click: onClick(func() tea.Cmd {
			m.modal = modalNone
			return m.vnLabelChoice("x")
		})}, txt(sDim.Render("   esc cancel")))
		line.addSegs(segs...)
	case modalDelete:
		line.add(sErr.Render(" Remove from your VNDB list? ") + keys("y", "yes", "esc", "no"))
	case modalScore:
		line.add(" Your vote (1-10, e.g. 8.5; 0 clears): " + m.modalInput.View())
	case modalStarted:
		line.add(" Started (YYYY-MM-DD, today, - clears): " + m.modalInput.View())
	case modalFinished:
		line.add(" Finished (YYYY-MM-DD, today, - clears): " + m.modalInput.View())
	case modalFinish:
		line.add(" Mark finished — your vote (1-10, enter to skip): " + m.modalInput.View())
	default:
		line.add("")
	}
	return line
}

func keys(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, sKey.Render(kv[i])+" "+sDim.Render(kv[i+1]))
	}
	return strings.Join(parts, sDim.Render(" · "))
}

func (m *model) helpLines() []string {
	help, title := query.Help, " Search syntax"
	if m.vn {
		help, title = query.VNHelp, " Visual novel search syntax (VNDB)"
	}
	lines := []string{sHead.Render(title) + sDim.Render("   (or press f in Search for the clickable filter editor)"), ""}
	for _, l := range strings.Split(help, "\n") {
		lines = append(lines, "  "+l)
	}
	lines = append(lines, "", sHead.Render(" Keys"), "",
		"  1 2 3 · tab        my list · now · search    enter       details (then j/k links, enter open, esc back)",
		"  j k ↑↓ g G pgup    move                      s           status / add to list   (x in it: remove)",
		"  + -                episode +1 / −1           e / r       set episodes / your score (0–10)",
		"  h l  (list)        status section            S           sort",
		"  /                  search / filter           f  X        filter editor / clear filters (search)",
		"  u  m  (now)        mark watched / change match            J K  scroll details   o O  AniList / MAL page",
		"  ctrl+r             sync / reload             q           quit",
		"  v                  anime ↔ visual novels     (VN: s label · r vote · e/E started/finished · f finish now)",
		"",
		sHead.Render(" Mouse"),
		"  click rows, tabs, chips, buttons, genres, tags and related shows · right-click cycles back / removes",
		"  wheel scrolls the pane under the pointer · ctrl/alt-click a genre or tag to add it to the current search",
		"  hold shift to select text in the terminal",
		"",
		sDim.Render("  Edits go to the anisync daemon: queued offline, applied to AniList and MAL together, undo with `anisync undo`."),
		sDim.Render("  esc / ? to close"))
	return lines
}
