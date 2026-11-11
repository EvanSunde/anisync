package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/evansunde/anisync/internal/kitty"
	"github.com/evansunde/anisync/internal/media"
	"github.com/evansunde/anisync/internal/query"
	"github.com/evansunde/anisync/internal/vndb"
	"github.com/evansunde/anisync/internal/vnlist"
)

var labelColor = map[int]lipgloss.Color{vndb.Playing: "10", vndb.Finished: "12", vndb.Stalled: "11", vndb.Dropped: "9",
	vndb.Wishlist: "14", vndb.Blacklist: "8", vndb.Voted: "13"}

func labelStyle(id int) lipgloss.Style {
	if c, ok := labelColor[id]; ok {
		return lipgloss.NewStyle().Foreground(c)
	}
	return lipgloss.NewStyle().Foreground(cAccent2)
}

// vnRating renders VNDB's 10-100 rating on the 10-point scale.
func vnRating(r float64) string {
	if r <= 0 {
		return sDim.Render("  – ")
	}
	return scoreStyle(int(r)).Render(fmt.Sprintf("%4.2f", r/10))
}

func vnLength(r vnrow) string {
	switch {
	case r.LengthMin > 0:
		return fmt.Sprintf("~%dh", (r.LengthMin+30)/60)
	case r.Length > 0:
		return vndb.LengthNames[r.Length]
	}
	return ""
}

// ---------------------------------------------------------------- list

func (m *model) vnListBlock(w, h int) block {
	vs := &m.vs
	var b block
	counts := m.vnCounts()
	var segs []seg
	for _, sec := range m.vnSections() {
		sec := sec
		n := counts[sec.id]
		if sec.id == 0 {
			n = len(vs.list)
		}
		st := sDim
		if sec.id == vs.secID {
			st = labelStyle(sec.id).Bold(true).Underline(true)
		}
		segs = append(segs, seg{text: st.Render(fmt.Sprintf("%s %d", sec.name, n)), click: onClick(func() tea.Cmd {
			vs.secID, vs.lSel = sec.id, 0
			return m.selChanged()
		})})
	}
	wrapSegs(&b, " ", segs, "  ", w)

	var state string
	switch {
	case vs.token == "" && len(vs.list) == 0:
		state = sWarn.Render("not logged in to VNDB — run: anisync auth vndb")
	case vs.syncing && !vs.loaded:
		state = sDim.Render("loading…")
	case vs.syncing:
		state = sDim.Render("⟳ syncing…")
	case vs.err != "" && vs.loaded && len(vs.list) > 0:
		state = sWarn.Render(syncErrLabel(vs.err)+" — local copy") + sDim.Render(", synced "+ago(vs.fetched))
	case vs.err != "":
		state = sErr.Render(vs.err)
	default:
		state = sDim.Render("synced " + ago(vs.fetched))
	}
	if m.filtering || m.filter.Value() != "" {
		b.addSegs(txt(m.filter.View()))
	} else {
		b.addSegs(txt(" "+state+sDim.Render(" · ")), seg{text: sDim.Render("sort: ") + sAccent.Render(vnSortModes[vs.sortMode]),
			click: func(ms tea.MouseMsg) tea.Cmd {
				d := 1
				if ms.Button == tea.MouseButtonRight {
					d = len(vnSortModes) - 1
				}
				vs.sortMode = (vs.sortMode + d) % len(vnSortModes)
				return m.selChanged()
			}}, txt(sDim.Render(" · / filter")))
	}
	v := m.vnVisible()
	if len(v) == 0 {
		if vs.loaded {
			b.add("", sDim.Render("  nothing here"))
		}
		return b
	}
	rows := h - len(b.lines)
	off := 0
	if vs.lSel >= rows {
		off = vs.lSel - rows + 1
	}
	for i := off; i < len(v) && i < off+rows; i++ {
		i := i
		b.addSegs(seg{text: m.vnListRow(v[i], i == vs.lSel, w), click: onClick(func() tea.Cmd {
			if vs.lSel == i {
				return m.openDetail()
			}
			vs.lSel = i
			m.closeDetail()
			return m.selChanged()
		})})
	}
	return b
}

func (m *model) vnStatusBadge(r *vnrow, width int) string {
	st := r.Status()
	if st == 0 {
		for _, l := range r.Labels {
			if l != vndb.Voted {
				st = l
				break
			}
		}
	}
	if st == 0 {
		return strings.Repeat(" ", width)
	}
	return labelStyle(st).Render(fit("● "+m.vnLabelName(st), width))
}

func (m *model) vnListRow(r *vnrow, selected bool, w int) string {
	var right []string
	if m.vs.secID == 0 || r.Status() != m.vs.secID {
		right = append(right, m.vnStatusBadge(r, 11))
	}
	if r.Vote > 0 {
		right = append(right, sWarn.Render(fmt.Sprintf("★%4.1f", float64(r.Vote)/10)))
	} else {
		right = append(right, "     ")
	}
	right = append(right, vnRating(r.Rating))
	if w >= 80 {
		right = append(right, sDim.Render(fmt.Sprintf("%-6s", fit(vnLength(*r), 6))))
	}
	pt := fmtPlaytime(r.Playtime)
	if pt != "" {
		pt = "⏱" + pt
	}
	right = append(right, sAccent.Render(fmt.Sprintf("%8s", pt)))
	if w >= 100 {
		var dates string
		switch {
		case r.Started != "" && r.Finished != "":
			dates = r.Started + "→" + r.Finished[5:]
		case r.Finished != "":
			dates = "done " + r.Finished
		case r.Started != "":
			dates = "from " + r.Started
		}
		right = append(right, sDim.Render(fmt.Sprintf("%-16s", fit(dates, 16))))
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
	line := marker + fitTitle(r.Title, w-lipgloss.Width(rs)-4) + "  " + rs
	if selected {
		return highlight(line, w)
	}
	return line
}

// ---------------------------------------------------------------- now

func (m *model) vnNowBlock(w, h int) block {
	var b block
	if m.daemonErr {
		b.add("", sErr.Render("  the daemon isn't running — start it: ")+sKey.Render("systemctl --user start anisync"))
		return b
	}
	if m.st != nil && m.st.Services["vndb"] == "" {
		b.add("", sWarn.Render("  VN tracking is off."),
			sDim.Render("  Set [vndb] enabled = true and dirs = [\"~/Games/VN\"] in the config, run ")+sKey.Render("anisync auth vndb")+
				sDim.Render(", then restart the daemon."), "")
	}
	rs := m.vnRunning()
	b.add(sHead.Render(" Playing now"))
	if len(rs) == 0 {
		b.add(sDim.Render("   no visual novel is running (from your [vndb] dirs)"))
	}
	for i := range rs {
		n := rs[i]
		gut := "   "
		if i == m.vs.nSel {
			gut = sAccent.Render(" ┃ ")
		}
		pick := func(f func() tea.Cmd) func() tea.Cmd { return func() tea.Cmd { m.vs.nSel = i; return f() } }
		b.addSegs(txt(gut+sOK.Render("▶ ")+sBold.Render(fit(n.Folder, max(10, w-40)))+"  "),
			button("f finish", pick(m.vnAskFinish)), txt(" "),
			button("m change match", pick(func() tea.Cmd { return m.vnStartResolve(n.Key, cleanFolder(n.Folder)) })))
		mt := sDim.Render("match     ")
		switch {
		case n.Ignored:
			mt += sDim.Render("ignored")
		case n.Pending:
			mt += sWarn.Render("waiting for you to pick — press m")
		case n.VNDBID != "":
			mt += sAccent.Render(n.Title) + sDim.Render(" "+n.VNDBID)
			if e := m.vnEntry(n.VNDBID); e != nil {
				mt += "  " + m.vnStatusBadge(e, 12)
			} else {
				mt += sDim.Render("  not on your list yet")
			}
		default:
			mt += sDim.Render("not matched yet (matched once the session counts)")
		}
		b.addSegs(seg{text: gut + mt, click: onClick(func() tea.Cmd { m.vs.nSel = i; return m.selChanged() })})
		b.add(gut+sDim.Render("playtime  ")+fmtPlaytime(int64(n.Session.Seconds()))+sDim.Render(" this session · ")+
			fmtPlaytime(int64(n.Playtime.Seconds()))+sDim.Render(" total"), "")
	}
	if ps := m.vnPending(); len(ps) > 0 {
		b.add(sHead.Render(fmt.Sprintf(" Needs a match (%d)", len(ps))))
		for j, p := range ps {
			i := len(rs) + j
			p := p
			gut := "   "
			if i == m.vs.nSel {
				gut = sAccent.Render(" ┃ ")
			}
			guess := ""
			if len(p.Candidates) > 0 {
				guess = sDim.Render("  best guess: " + p.Candidates[0].Title)
			}
			sel := func(f func() tea.Cmd) func() tea.Cmd { return func() tea.Cmd { m.vs.nSel = i; return f() } }
			b.addSegs(txt(gut+p.Query+"  "), button("pick", sel(func() tea.Cmd { return m.vnStartResolve(p.Key, cleanFolder(p.Query)) })),
				txt(" "), button("ignore", sel(func() tea.Cmd { return ignoreCmd(p.Key, p.Query) })), txt(guess))
		}
		b.add("")
	}
	var q []string
	if m.st != nil {
		for _, it := range m.st.Queue {
			if it.Kind != "vn" && it.Kind != "vnset" {
				continue
			}
			what := it.Action
			if it.Kind == "vnset" {
				what = "list edit"
			}
			line := "   " + firstNonEmpty(it.TargetTitle, it.Title) + "  " + sDim.Render(what)
			if it.LastErr != "" {
				line += sDim.Render("  (" + it.LastErr + ")")
			}
			q = append(q, fit(line, w))
		}
	}
	if len(q) > 0 {
		b.add(sHead.Render(fmt.Sprintf(" Queued VNDB updates (%d)", len(q))))
		if m.st.LastErr != "" {
			b.add(sWarn.Render("   offline or service unavailable — retrying " + retryIn(m.st)))
		}
		b.add(q...)
	} else if m.st != nil {
		b.add(sDim.Render(" Nothing queued for VNDB."))
	}
	return b
}

func cleanFolder(s string) string { return media.CleanVNFolder(filepath.Base(s)) }

// ---------------------------------------------------------------- search

func (m *model) vnSearchBlock(w, h int) block {
	vs := &m.vs
	var b block
	in := vs.input
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
		info = sWarn.Render(fmt.Sprintf(" Pick the visual novel for “%s”", m.resolving.Query)) + sDim.Render("  enter on a result · esc cancels")
	case vs.loading && len(vs.results) == 0:
		info = sDim.Render(" searching VNDB…")
	case len(vs.warns) > 0:
		info = sErr.Render(" " + strings.Join(vs.warns, "; "))
	case vs.more:
		info = sDim.Render(fmt.Sprintf(" showing %d of %d · more load as you scroll", len(vs.results), vs.total))
	default:
		info = sDim.Render(fmt.Sprintf(" %d result%s", len(vs.results), plural(len(vs.results))))
	}
	b.add(info)
	rows := h - len(b.lines)
	off := 0
	if vs.sSel >= rows {
		off = vs.sSel - rows + 1
	}
	for i := off; i < len(vs.results) && i < off+rows; i++ {
		i := i
		b.addSegs(seg{text: m.vnResultRow(i, w), click: onClick(func() tea.Cmd {
			if vs.sSel == i {
				if m.resolving != nil {
					return m.choose()
				}
				return m.openDetail()
			}
			vs.sSel = i
			m.closeDetail()
			return tea.Batch(m.selChanged(), m.loadMore())
		})})
	}
	if vs.loading && len(vs.results) > 0 && len(b.lines) < h {
		b.add(sDim.Render("   loading more…"))
	}
	return b
}

func (m *model) vnResultRow(i, w int) string {
	r := m.vnEnrich(m.vs.results[i])
	var right []string
	right = append(right, sDim.Render(fmt.Sprintf("%4s", yearStr(r.Year()))))
	if w >= 80 {
		right = append(right, sDim.Render(fmt.Sprintf("%-6s", fit(vnLength(r), 6))))
	}
	right = append(right, vnRating(r.Rating))
	if w >= 90 {
		right = append(right, sDim.Render(fmt.Sprintf("%6s", humanInt(r.Votes))))
	}
	right = append(right, m.vnStatusBadge(&r, 11))
	rs := strings.Join(right, " ")
	num := sDim.Render(fmt.Sprintf("%3d ", i+1))
	marker := "  "
	if i == m.vs.sSel {
		marker = sAccent.Render("▸ ")
	}
	title := r.Title
	if w >= 100 && len(r.Devs) > 0 {
		title += sDim.Render("  " + r.Devs[0])
	}
	line := num + marker + fitTitle(title, w-lipgloss.Width(rs)-8) + " " + rs
	if i == m.vs.sSel {
		return highlight(line, w)
	}
	return line
}

func yearStr(y int) string {
	if y == 0 {
		return "TBA"
	}
	return fmt.Sprint(y)
}

// ---------------------------------------------------------------- detail

var relationName = map[string]string{"seq": "sequel", "preq": "prequel", "set": "same setting", "alt": "alternative",
	"char": "shares characters", "side": "side story", "par": "parent story", "ser": "same series", "fan": "fandisc", "orig": "original"}

var platformName = func() map[string]string {
	m := map[string]string{}
	for _, c := range query.VNPlatforms {
		m[c.Value] = c.Label
	}
	return m
}()

// vnActivate follows a VN detail link: tags start a search (add=true adds to
// the current filters), visual novels open in the detail panel.
func (m *model) vnActivate(l dlink, add bool) tea.Cmd {
	switch l.kind {
	case "vtag":
		f := query.VNFilter{Sort: "rating", VotesMin: 50}
		if add {
			f = m.vs.f
		}
		if m.vs.vocab == nil {
			m.vs.vocab = query.NewVNVocab(nil)
		}
		m.vs.vocab.Add(query.VNTag{ID: l.vid, Name: l.value})
		f.Tags = appendUnique(f.Tags, l.value)
		m.vs.f = f
		m.tab = tabSearch
		m.closeDetail()
		m.flash(fmt.Sprintf("searching tag %q (ctrl/alt-click or A adds to current filters)", l.value), false)
		return m.vnRunSearch()
	case "vn":
		if cur := m.vnCurrent(); cur != nil && cur.ID == l.vid {
			return nil
		}
		m.vs.peek = append(m.vs.peek, vnrow{ID: l.vid, Title: l.value})
		if m.wide() {
			m.focusDetail = true
		} else {
			m.full = true
		}
		return m.selChanged()
	}
	return nil
}

func (m *model) vnDetailBlock(r vnrow, w, h int) block {
	var b block
	d := m.vs.details[r.Key()]
	if d != nil { // the detail has complete display data
		full := vnlist.FromVN(d)
		full.OnList, full.Labels, full.Vote, full.Started, full.Finished = r.OnList, r.Labels, r.Vote, r.Started, r.Finished
		full.Notes, full.Pending, full.Playtime, full.Updated = r.Notes, r.Pending, r.Playtime, r.Updated
		r = full
	}
	focused := m.focusDetail || m.full
	var links []dlink
	link := func(l dlink, label string, st lipgloss.Style) seg {
		i := len(links)
		links = append(links, l)
		if focused && i == m.link {
			st = sLinkOn
		}
		return seg{text: st.Render(label), tag: i + 1, click: func(ms tea.MouseMsg) tea.Cmd {
			m.link = i
			return m.activate(links[i], ms.Ctrl || ms.Alt || ms.Button == tea.MouseButtonRight)
		}}
	}
	if n := len(m.vs.peek); n > 0 {
		from := "list"
		if n > 1 {
			from = m.vs.peek[n-2].Title
		} else {
			p := m.vs.peek
			m.vs.peek = nil
			if base := m.vnCurrent(); base != nil {
				from = base.Title
			}
			m.vs.peek = p
		}
		b.addSegs(seg{text: sAccent.Render("← back"), click: onClick(func() tea.Cmd {
			c, _ := m.detailKey("esc")
			return c
		})}, txt(sDim.Render("  from "+from)))
	}

	// --- info next to the cover
	var info []string
	info = append(info, sHead.Render(r.Title))
	var alt []string
	if r.Alt != "" && r.Alt != r.Title {
		alt = append(alt, r.Alt)
	}
	if d != nil {
		for _, t := range d.Titles {
			if t.Main && t.Title != r.Title && !containsFold(alt, t.Title) {
				alt = append(alt, t.Title)
			}
		}
	}
	if len(alt) > 0 {
		info = append(info, sDim.Render(strings.Join(alt, " · ")))
	}
	var meta []string
	if r.Released != "" {
		meta = append(meta, r.Released)
	}
	if l := vnLength(r); l != "" {
		if r.Length > 0 {
			l = vndb.LengthNames[r.Length] + " (" + strings.TrimPrefix(vnLength(r), "~") + ")"
			if r.LengthMin == 0 {
				l = vndb.LengthNames[r.Length] + " (" + vndb.LengthHint[r.Length] + ")"
			}
		}
		meta = append(meta, l)
	}
	if d != nil && d.OLang != "" {
		meta = append(meta, "orig. "+d.OLang)
	}
	if len(meta) > 0 {
		info = append(info, strings.Join(meta, sDim.Render(" · ")))
	}
	if len(r.Devs) > 0 {
		info = append(info, sDim.Render("by ")+strings.Join(r.Devs, ", "))
	}
	if r.Rating > 0 {
		info = append(info, scoreStyle(int(r.Rating)).Bold(true).Render(fmt.Sprintf("★ %.2f", r.Rating/10))+
			sDim.Render(fmt.Sprintf(" · %s votes", humanInt(r.Votes))))
	}
	if d != nil && len(d.Platforms) > 0 {
		var ps []string
		for _, p := range d.Platforms {
			ps = append(ps, firstNonEmpty(platformName[p], p))
		}
		info = append(info, sDim.Render(strings.Join(ps, ", ")))
	}
	if d != nil && len(d.Languages) > 0 {
		ls := d.Languages
		more := ""
		if len(ls) > 8 {
			ls, more = ls[:8], fmt.Sprintf(" +%d", len(d.Languages)-8)
		}
		info = append(info, sDim.Render("languages "+strings.Join(ls, " ")+more))
	}
	info = append(info, "")
	if r.OnList {
		var lb []string
		for _, l := range r.Labels {
			if l != vndb.Voted {
				lb = append(lb, labelStyle(l).Bold(true).Render("● "+m.vnLabelName(l)))
			}
		}
		mine := strings.Join(lb, " ")
		if mine == "" {
			mine = sDim.Render("on your list")
		}
		if r.Vote > 0 {
			mine += sWarn.Render(fmt.Sprintf("  ★ %.1f", float64(r.Vote)/10))
		}
		if r.Pending {
			mine += sDim.Render("  ⇅ syncing")
		}
		info = append(info, mine)
		var dates []string
		if r.Started != "" {
			dates = append(dates, "started "+r.Started)
		}
		if r.Finished != "" {
			dates = append(dates, "finished "+r.Finished)
		}
		if len(dates) > 0 {
			info = append(info, sDim.Render(strings.Join(dates, " · ")))
		}
	} else {
		info = append(info, sDim.Render("not on your list — ")+sKey.Render("s")+sDim.Render(" to add"))
	}
	if pt := fmtPlaytime(r.Playtime); pt != "" {
		info = append(info, sAccent.Render("⏱ "+pt)+sDim.Render(" played here"))
	}

	if m.cov.enabled && m.vs.covers != "off" {
		cw := m.cov.cols
		var img []string
		switch {
		case !m.vnCoverShown(r):
			img = coverBox(cw, m.cov.rows, "cover", "hidden", "(vn_covers)")
		case m.cov.get(r.Key()) == coverReady:
			img = kitty.Placeholder(r.Key(), cw, m.cov.rows)
		default:
			img = coverBox(cw, m.cov.rows)
		}
		iw := w - cw - 2
		for i := 0; i < max(len(img), len(info)); i++ {
			a := strings.Repeat(" ", cw)
			if i < len(img) {
				a = img[i]
			}
			var t string
			if i < len(info) {
				t = fit(info[i], iw)
			}
			b.add(a + "  " + t)
		}
	} else {
		for _, l := range info {
			b.add(fit(l, w))
		}
	}

	if d == nil {
		if e := m.vs.detailErr[r.Key()]; e != "" {
			b.add("", sErr.Render(e))
		} else {
			b.add("", sDim.Render("loading details…"))
		}
	} else {
		tags := append([]vndb.Tag(nil), d.Tags...)
		sort.SliceStable(tags, func(i, j int) bool { return tags[i].Rating > tags[j].Rating })
		var segs []seg
		for _, t := range tags {
			if t.Spoiler > 0 || t.Rating < 1.5 || len(segs) == 15 || (t.Category == "ero" && m.vs.covers != "all") {
				continue
			}
			segs = append(segs, link(dlink{kind: "vtag", value: t.Name, vid: t.ID}, t.Name, sLink).
				withSuffix(sDim.Render(fmt.Sprintf(" %.1f", t.Rating))))
		}
		if len(segs) > 0 {
			b.add("")
			wrapSegs(&b, sDim.Render("tags  "), segs, sDim.Render(" · "), w)
		}
		if desc := vndb.PlainDescription(d.Description); desc != "" {
			b.add("")
			b.add(strings.Split(lipgloss.NewStyle().Width(w).Render(desc), "\n")...)
		}
		if len(d.Relations) > 0 {
			b.add("", sHead.Render("Related"))
			for _, rel := range d.Relations {
				lbl := firstNonEmpty(relationName[rel.Relation], rel.Relation)
				extra := ""
				if !rel.Official {
					extra = sDim.Render(" (unofficial)")
				}
				b.addSegs(txt(" "+sDim.Render(fmt.Sprintf("%-17s", lbl))+" "), link(dlink{kind: "vn", value: rel.Title, vid: rel.ID}, rel.Title, sLink), txt(extra))
			}
		}
	}
	if r.Notes != "" {
		b.add("", sHead.Render("Your notes"))
		b.add(strings.Split(lipgloss.NewStyle().Width(w).Render(r.Notes), "\n")...)
	}
	return m.finishDetail(b, links, h, w)
}

// coverBox is a placeholder the size of a cover, with centered text.
func coverBox(w, h int, text ...string) []string {
	out := make([]string, h)
	top := (h - len(text)) / 2
	for i := range out {
		s := ""
		if j := i - top; j >= 0 && j < len(text) {
			s = text[j]
		}
		pad := max(0, (w-lipgloss.Width(s))/2)
		out[i] = sDim.Render(fit(strings.Repeat(" ", pad)+s, w))
	}
	if len(text) > 0 && h > 1 {
		out[0] = sDim.Render(fit("┌"+strings.Repeat("─", max(0, w-2))+"┐", w))
		out[h-1] = sDim.Render(fit("└"+strings.Repeat("─", max(0, w-2))+"┘", w))
	}
	return out
}
