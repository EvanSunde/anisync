package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/evansunde/anisync/internal/daemon"
	"github.com/evansunde/anisync/internal/query"
)

// ---------------------------------------------------------------- searching

func (m *model) runSearch() tea.Cmd {
	if m.vn {
		return m.vnRunSearch()
	}
	m.seq++
	m.loading, m.results, m.sSel, m.hasNext = true, nil, 0, false
	if !m.typing {
		m.input.SetValue(query.Format(m.f))
	}
	return m.searchCmd(m.f, 1, m.seq)
}

// filterChanged re-runs the search shortly after the last filter edit.
func (m *model) filterChanged() tea.Cmd {
	m.searchInput().SetValue(m.spec().format())
	m.liveSeq++
	return liveSearch(m.liveSeq, m.vn)
}

// searchInput is the query box of the current mode.
func (m *model) searchInput() *textinput.Model {
	if m.vn {
		return &m.vs.input
	}
	return &m.input
}

func (m *model) loadMore() tea.Cmd {
	if m.vn {
		return m.vnLoadMore()
	}
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
		m.searchInput().Focus()
		m.searchInput().CursorEnd()
		return textinput.Blink
	case "f":
		m.openPanel(0)
		return nil
	case "X":
		m.spec().clearFilters()
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
	if m.vn {
		if cmd, ok := m.move(s, &m.vs.sSel, len(m.vs.results)); ok {
			return tea.Batch(cmd, m.loadMore())
		}
		cmd, _ := m.actionKey(s)
		return cmd
	}
	if cmd, ok := m.move(s, &m.sSel, len(m.results)); ok {
		return tea.Batch(cmd, m.loadMore())
	}
	cmd, _ := m.actionKey(s)
	return cmd
}

func (m *model) choose() tea.Cmd {
	if m.vn {
		return m.vnChoose()
	}
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
