package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func sortRows(rs []*row, mode int) {
	switch mode {
	case 1:
		sort.SliceStable(rs, func(i, j int) bool { return strings.ToLower(rs[i].Title) < strings.ToLower(rs[j].Title) })
	case 2:
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].MyScore > rs[j].MyScore })
	case 3:
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Score > rs[j].Score })
	case 4:
		sort.SliceStable(rs, func(i, j int) bool {
			a, b := rs[i].NextAt, rs[j].NextAt
			if (a == 0) != (b == 0) {
				return a != 0
			}
			return a < b
		})
	default:
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Updated > rs[j].Updated })
	}
}

func (m *model) listBlock(w, h int) block {
	var b block
	counts := map[string]int{}
	for _, r := range m.list {
		counts[r.Status]++
	}
	var segs []seg
	for i, s := range sections {
		n := counts[s]
		if s == "" {
			n = len(m.list)
		}
		label := fmt.Sprintf("%s %d", statusName(s), n)
		st := sDim
		if i == m.section {
			st = statusStyle(s).Bold(true).Underline(true)
		}
		i := i
		segs = append(segs, seg{text: st.Render(label), click: onClick(func() tea.Cmd {
			m.section, m.lSel = i, 0
			return m.selChanged()
		})})
	}
	wrapSegs(&b, " ", segs, "  ", w)

	// status line: sync state, sort (clickable), filter
	var state string
	switch {
	case m.listSyncing && !m.listLoaded:
		state = sDim.Render("loading…")
	case m.listSyncing:
		state = sDim.Render("⟳ syncing…")
	case m.listErr != "" && m.listLoaded:
		state = sWarn.Render(syncErrLabel(m.listErr)+" — local copy") + sDim.Render(", synced "+ago(m.listFetched))
	case m.listErr != "":
		state = sErr.Render(m.listErr)
	default:
		state = sDim.Render("synced " + ago(m.listFetched))
	}
	if m.filtering || m.filter.Value() != "" {
		b.addSegs(txt(m.filter.View()))
	} else {
		b.addSegs(txt(" "+state+sDim.Render(" · ")), seg{text: sDim.Render("sort: ") + sAccent.Render(sortModes[m.sortMode]),
			click: func(ms tea.MouseMsg) tea.Cmd {
				d := 1
				if ms.Button == tea.MouseButtonRight {
					d = len(sortModes) - 1
				}
				m.sortMode = (m.sortMode + d) % len(sortModes)
				return m.selChanged()
			}}, txt(sDim.Render(" · / filter")))
	}

	v := m.visibleList()
	if len(v) == 0 {
		if m.listLoaded {
			b.add("", sDim.Render("  nothing here"))
		}
		return b
	}
	rows := h - len(b.lines)
	off := 0
	if m.lSel >= rows {
		off = m.lSel - rows + 1
	}
	for i := off; i < len(v) && i < off+rows; i++ {
		i := i
		b.addSegs(seg{text: m.listRow(v[i], i == m.lSel, w), click: onClick(func() tea.Cmd {
			if m.lSel == i {
				return m.openDetail()
			}
			m.lSel = i
			m.closeDetail()
			return m.selChanged()
		})})
	}
	return b
}

func (m *model) listRow(r *row, selected bool, w int) string {
	var right []string
	frac := 0.0
	if r.Episodes > 0 {
		frac = float64(r.Progress) / float64(r.Episodes)
	}
	right = append(right, bar(frac, 8, statusColor[r.Status])+fmt.Sprintf(" %7s", fmt.Sprintf("%d/%s", r.Progress, epsStr(r.Episodes))))
	if r.MyScore > 0 {
		right = append(right, sWarn.Render(fmt.Sprintf("★%4.1f", float64(r.MyScore)/10)))
	} else {
		right = append(right, "     ")
	}
	var extra string
	switch {
	case r.Behind() > 0:
		extra = sWarn.Render(fmt.Sprintf("+%d new", r.Behind()))
		if r.NextAt > 0 {
			extra += sDim.Render(" · " + until(r.NextAt))
		}
	case r.NextEp > 0 && r.NextAt > 0:
		extra = sDim.Render(fmt.Sprintf("ep%d in %s", r.NextEp, until(r.NextAt)))
	}
	if w >= 70 {
		right = append(right, fit(extra, 18))
	}
	if r.Pending {
		right = append(right, sAccent.Render("⇅"))
	} else {
		right = append(right, " ")
	}
	rs := strings.Join(right, " ")
	marker := "  "
	if selected {
		marker = sAccent.Render("▸ ")
	}
	title := r.Title
	tw := w - lipgloss.Width(rs) - 4
	if sections[m.section] == "" {
		title = statusStyle(r.Status).Render("● ") + title
	}
	line := marker + fitTitle(title, tw) + "  " + rs
	if selected {
		return highlight(line, w)
	}
	return line
}

// syncErrLabel shortens a sync error for the status line.
func syncErrLabel(e string) string {
	l := strings.ToLower(e)
	switch {
	case strings.Contains(l, "not logged in"):
		return "not logged in"
	case strings.Contains(l, "dial") || strings.Contains(l, "no such host") || strings.Contains(l, "timeout") ||
		strings.Contains(l, "unreachable") || strings.Contains(l, "connection refused") || strings.Contains(l, "http 5"):
		return "offline"
	case strings.Contains(l, "http 401") || strings.Contains(l, "invalid token"):
		return "login expired"
	}
	return "sync failed"
}
