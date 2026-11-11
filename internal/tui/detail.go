package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/kitty"
)

// dlink is a clickable thing in the detail panel.
type dlink struct {
	kind   string // genre | tag | media | vtag | vn
	value  string
	vid    string // VNDB id (vtag, vn)
	id     int
	format string
	line   int
}

// activate follows a link: genres/tags start a search (add=true adds to
// the current filters), shows open in the detail panel.
func (m *model) activate(l dlink, add bool) tea.Cmd {
	switch l.kind {
	case "vtag", "vn":
		return m.vnActivate(l, add)
	case "genre", "tag":
		f := anilist.Filter{Sort: []string{"POPULARITY_DESC"}}
		if add {
			f = m.f
		}
		if l.kind == "genre" {
			f.Genres = appendUnique(f.Genres, l.value)
		} else {
			f.Tags = appendUnique(f.Tags, l.value)
		}
		m.f = f
		m.tab = tabSearch
		m.closeDetail()
		m.flash(fmt.Sprintf("searching %s %q (ctrl/alt-click or A adds to current filters)", l.kind, l.value), false)
		return m.runSearch()
	case "media":
		if cur := m.current(); cur != nil && cur.AnilistID == l.id {
			return nil
		}
		m.peek = append(m.peek, row{AnilistID: l.id, Title: l.value, Format: l.format})
		if m.wide() {
			m.focusDetail = true
		} else {
			m.full = true
		}
		return m.selChanged()
	}
	return nil
}

func appendUnique(l []string, s string) []string {
	for _, x := range l {
		if x == s {
			return l
		}
	}
	return append(l, s)
}

// detailBlock renders the detail panel for r (w×h, already scrolled).
func (m *model) detailBlock(r row, w, h int) block {
	var b block
	d := m.details[r.Key()]
	focused := m.focusDetail || m.full
	var links []dlink
	link := func(kind, value, label string, id int, format string, st lipgloss.Style) seg {
		i := len(links)
		links = append(links, dlink{kind: kind, value: value, id: id, format: format})
		if focused && i == m.link {
			st = sLinkOn
		}
		return seg{text: st.Render(label), tag: i + 1, click: func(ms tea.MouseMsg) tea.Cmd {
			m.link = i
			return m.activate(links[i], ms.Ctrl || ms.Alt || ms.Button == tea.MouseButtonRight)
		}}
	}

	// breadcrumb when browsing related shows
	if n := len(m.peek); n > 0 {
		from := "list"
		if n > 1 {
			from = m.peek[n-2].Title
		} else if base := m.baseRow(); base != nil {
			from = base.Title
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
	for _, t := range []string{r.English, r.Native} {
		if t != "" && !strings.EqualFold(t, r.Title) && !containsFold(alt, t) {
			alt = append(alt, t)
		}
	}
	if len(alt) > 0 {
		info = append(info, sDim.Render(strings.Join(alt, " · ")))
	}
	var meta []string
	meta = append(meta, strings.TrimSpace(formatBadge(r.Format)))
	if r.Episodes > 0 {
		e := fmt.Sprintf("%d eps", r.Episodes)
		if r.Duration > 0 {
			e += fmt.Sprintf(" × %d min", r.Duration)
		}
		meta = append(meta, e)
	}
	if r.Season != "" && r.Year > 0 {
		meta = append(meta, strings.Title(strings.ToLower(r.Season))+" "+fmt.Sprint(r.Year))
	} else if r.Year > 0 {
		meta = append(meta, fmt.Sprint(r.Year))
	}
	if a := airName[r.AirStatus]; a != "" {
		st := sDim
		if r.AirStatus == "RELEASING" {
			st = sOK
		}
		meta = append(meta, st.Render(a))
	}
	info = append(info, strings.Join(meta, sDim.Render(" · ")))
	var studio []string
	if r.Studio != "" {
		studio = append(studio, r.Studio)
	}
	if d != nil && d.Source != "" {
		studio = append(studio, "from "+strings.ToLower(strings.ReplaceAll(d.Source, "_", " ")))
	}
	if d != nil {
		if span := dateSpan(d.StartDate, d.EndDate); span != "" {
			studio = append(studio, span)
		}
	}
	if len(studio) > 0 {
		info = append(info, sDim.Render(strings.Join(studio, " · ")))
	}
	var scores []string
	if r.Score > 0 {
		scores = append(scores, scoreStyle(r.Score).Bold(true).Render(fmt.Sprintf("★ %d%%", r.Score)))
	}
	if d != nil && d.MeanScore > 0 && d.MeanScore != r.Score {
		scores = append(scores, sDim.Render(fmt.Sprintf("mean %d%%", d.MeanScore)))
	}
	if r.Popularity > 0 {
		scores = append(scores, sDim.Render(humanInt(r.Popularity)+" users"))
	}
	if d != nil && d.Favourites > 0 {
		scores = append(scores, sDim.Render("♥ "+humanInt(d.Favourites)))
	}
	if len(scores) > 0 {
		info = append(info, strings.Join(scores, sDim.Render(" · ")))
	}
	if d != nil {
		for i, rk := range d.Rankings {
			if i == 3 {
				break
			}
			ctx := rk.Context
			if !rk.AllTime && rk.Year > 0 {
				if rk.Season != "" {
					ctx += " " + strings.Title(strings.ToLower(rk.Season))
				}
				ctx += fmt.Sprintf(" %d", rk.Year)
			}
			icon, st := "★", sWarn
			if rk.Type == "POPULAR" {
				icon, st = "♥", lipgloss.NewStyle().Foreground(cAccent2)
			}
			info = append(info, st.Render(fmt.Sprintf("%s #%d", icon, rk.Rank))+" "+sDim.Render(ctx))
		}
	} else if r.Rank > 0 {
		info = append(info, sWarn.Render(fmt.Sprintf("★ #%d", r.Rank))+sDim.Render(" highest rated all time"))
	}
	info = append(info, "")
	if r.Status != "" {
		frac := 0.0
		if r.Episodes > 0 {
			frac = float64(r.Progress) / float64(r.Episodes)
		}
		mine := statusStyle(r.Status).Bold(true).Render("● "+statusName(r.Status)) +
			fmt.Sprintf("  %d/%s ", r.Progress, epsStr(r.Episodes)) + bar(frac, 12, statusColor[r.Status])
		if r.MyScore > 0 {
			mine += sWarn.Render(fmt.Sprintf("  ★ %.1f", float64(r.MyScore)/10))
		}
		if r.Pending {
			mine += sDim.Render("  ⇅ syncing")
		}
		info = append(info, mine)
	} else {
		info = append(info, sDim.Render("not on your list — ")+sKey.Render("s")+sDim.Render(" to add"))
	}
	if r.NextEp > 0 && r.NextAt > 0 {
		info = append(info, fmt.Sprintf("episode %d airs in %s", r.NextEp, sAccent.Render(until(r.NextAt))))
	}
	if n := r.Behind(); n > 0 {
		info = append(info, sWarn.Render(fmt.Sprintf("%d aired episode%s to catch up on", n, plural(n))))
	}

	// cover + info side by side
	if m.cov.enabled {
		cw := m.cov.cols
		var img []string
		if m.cov.get(r.Key()) == coverReady {
			img = kitty.Placeholder(r.Key(), cw, m.cov.rows)
		} else {
			for i := 0; i < m.cov.rows; i++ {
				img = append(img, strings.Repeat(" ", cw))
			}
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

	// --- clickable genres and tags
	if len(r.Genres) > 0 {
		var segs []seg
		for _, g := range r.Genres {
			segs = append(segs, link("genre", g, g, 0, "", genreStyle(g)))
		}
		b.add("")
		wrapSegs(&b, sDim.Render("genres  "), segs, sDim.Render(" · "), w)
	}
	if d == nil {
		if e := m.detailErr[r.Key()]; e != "" {
			b.add("", sErr.Render(e))
		} else {
			b.add("", sDim.Render("loading details…"))
		}
	} else {
		var segs []seg
		for _, t := range d.Tags {
			if t.Spoiler || t.Rank < 50 || len(segs) == 12 {
				continue
			}
			segs = append(segs, link("tag", t.Name, t.Name, 0, "", sLink).withSuffix(sDim.Render(fmt.Sprintf(" %d%%", t.Rank))))
		}
		if len(segs) > 0 {
			wrapSegs(&b, sDim.Render("tags    "), segs, sDim.Render(" · "), w)
		}
		if desc := anilist.PlainDescription(d.Description); desc != "" {
			b.add("")
			b.add(strings.Split(lipgloss.NewStyle().Width(w).Render(desc), "\n")...)
		}
		var rel []dlink
		var relLabels []string
		for _, e := range d.Relations.Edges {
			if e.Node.Type != "ANIME" {
				continue
			}
			switch e.RelationType {
			case "PREQUEL", "SEQUEL", "PARENT", "SIDE_STORY", "SPIN_OFF", "ALTERNATIVE", "SUMMARY":
				rel = append(rel, dlink{id: e.Node.ID, value: e.Node.Title.Best(), format: e.Node.Format})
				relLabels = append(relLabels, strings.ToLower(strings.ReplaceAll(e.RelationType, "_", " ")))
			}
		}
		if len(rel) > 0 {
			b.add("", sHead.Render("Related"))
			for i, l := range rel {
				lbl := fmt.Sprintf("%-11s", relLabels[i])
				b.addSegs(txt(" "+sDim.Render(lbl)+" "), link("media", l.value, l.value, l.id, l.format, sLink),
					txt(" "+sDim.Render(formatName[l.format])))
			}
		}
		var recs []seg
		for _, n := range d.Recommendations.Nodes {
			if n.Media != nil {
				recs = append(recs, link("media", n.Media.Title.Best(), n.Media.Title.Best(), n.Media.ID, n.Media.Format, sLink))
			}
		}
		if len(recs) > 0 {
			b.add("", sHead.Render("If you liked this"))
			for _, s := range recs {
				b.addSegs(txt(" "), s)
			}
		}
	}

	return m.finishDetail(b, links, h, w)
}

// finishDetail records link positions for keyboard navigation, keeps the
// selected link visible and returns the scrolled window.
func (m *model) finishDetail(b block, links []dlink, h, w int) block {
	focused := m.focusDetail || m.full
	for _, h := range b.hits {
		if h.tag > 0 && h.tag-1 < len(links) {
			links[h.tag-1].line = h.y
		}
	}
	m.links = links
	if m.link >= len(links) {
		m.link = 0
	}
	// keep the selected link visible; clamp scrolling
	if focused && m.link < len(links) {
		ln := links[m.link].line
		if ln < m.dScroll {
			m.dScroll = max(0, ln-1)
		} else if ln >= m.dScroll+h {
			m.dScroll = ln - h + 2
		}
	}
	m.dScroll = max(0, min(m.dScroll, len(b.lines)-h))
	return b.window(m.dScroll, h, w)
}

func (s seg) withSuffix(suffix string) seg { s.text += suffix; return s }

// window returns lines [off, off+h) with hits shifted accordingly.
func (b block) window(off, h, w int) block {
	var out block
	for i := off; i < len(b.lines) && i < off+h; i++ {
		out.lines = append(out.lines, fit(b.lines[i], w))
	}
	for _, x := range b.hits {
		if x.y >= off && x.y < off+h {
			x.y -= off
			out.hits = append(out.hits, x)
		}
	}
	return out
}

// baseRow is the selection under the peek stack.
func (m *model) baseRow() *row {
	p := m.peek
	m.peek = nil
	r := m.current()
	m.peek = p
	return r
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func dateSpan(a, b anilist.FuzzyDate) string {
	f := func(d anilist.FuzzyDate) string {
		if d.Year == nil {
			return ""
		}
		if d.Month == nil {
			return fmt.Sprint(*d.Year)
		}
		return time.Month(*d.Month).String()[:3] + " " + fmt.Sprint(*d.Year)
	}
	s, e := f(a), f(b)
	switch {
	case s == "":
		return ""
	case e == "" || e == s:
		return s
	}
	return s + " → " + e
}

func containsFold(l []string, s string) bool {
	for _, x := range l {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
