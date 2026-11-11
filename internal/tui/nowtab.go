package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/daemon"
	"github.com/evansunde/anisync/internal/store"
)

// players with something loaded
func (m *model) nowPlayers() []daemon.NowPlaying {
	if m.st == nil {
		return nil
	}
	var out []daemon.NowPlaying
	for _, n := range m.st.Now {
		if n.File != "" || n.Title != "" {
			out = append(out, n)
		}
	}
	return out
}

func (m *model) nowCount() int { return len(m.nowPlayers()) + len(m.pending) }

// nowSelected returns the selected player (nil if a pending title is selected).
func (m *model) nowSelected() *daemon.NowPlaying {
	ps := m.nowPlayers()
	if m.nSel < len(ps) {
		return &ps[m.nSel]
	}
	return nil
}

func (m *model) nowPending() *store.Pending {
	i := m.nSel - len(m.nowPlayers())
	if i >= 0 && i < len(m.pending) {
		return m.pending[i]
	}
	return nil
}

// startResolve opens a search to pick the show for a file title.
func (m *model) startResolve(key, q string, ep int) tea.Cmd {
	m.resolving = &resolveTarget{Key: key, Query: q, Episode: ep}
	m.tab = tabSearch
	m.closeDetail()
	m.f = anilist.Filter{Search: q}
	return m.runSearch()
}

func (m *model) nowKey(s string) tea.Cmd {
	if cmd, ok := m.move(s, &m.nSel, m.nowCount()); ok {
		return cmd
	}
	p, pend := m.nowSelected(), m.nowPending()
	switch s {
	case "u", "W":
		if p != nil {
			return m.markWatched(p)
		}
	case "m":
		if p != nil {
			return m.changeMatch(p)
		}
		if pend != nil {
			return m.resolvePending(pend)
		}
	case "x":
		if pend != nil {
			return ignoreCmd(pend.Key, pend.Query)
		}
	case "enter":
		if pend != nil {
			return m.resolvePending(pend)
		}
		if p != nil && p.MatchID != 0 {
			return m.openDetail()
		}
		if p != nil {
			return m.changeMatch(p)
		}
	case "ctrl+r":
		return tea.Batch(statusCmd(), pendingCmd())
	}
	cmd, _ := m.actionKey(s)
	return cmd
}

func (m *model) markWatched(p *daemon.NowPlaying) tea.Cmd {
	if m.daemonErr {
		m.flash("daemon not running", true)
		return nil
	}
	return watchedNowCmd(p.Bus)
}

func (m *model) changeMatch(p *daemon.NowPlaying) tea.Cmd {
	if p.Parsed == "" {
		m.flash("couldn't read a title from this file name", true)
		return nil
	}
	q := p.Parsed
	if p.Season > 1 {
		q += fmt.Sprintf(" season %d", p.Season)
	}
	return m.startResolve(p.Key, q, p.Episode)
}

func (m *model) resolvePending(p *store.Pending) tea.Cmd {
	if p.Kind != "anime" {
		m.flash("visual novels: use `anisync resolve` (VNDB search)", true)
		return nil
	}
	q := reSeasonSuffix(p.Query)
	return m.startResolve(p.Key, q, 0)
}

func reSeasonSuffix(q string) string {
	if i := strings.LastIndex(q, " (season "); i > 0 && strings.HasSuffix(q, ")") {
		return q[:i] + " season " + strings.TrimSuffix(q[i+len(" (season "):], ")")
	}
	return q
}

func button(label string, f func() tea.Cmd) seg {
	return seg{text: sButton.Render(label), click: onClick(f)}
}

func (m *model) nowBlock(w, h int) block {
	var b block
	if m.daemonErr {
		b.add("", sErr.Render("  the daemon isn't running — start it: ")+sKey.Render("systemctl --user start anisync"))
		return b
	}
	ps := m.nowPlayers()
	b.add(sHead.Render(" Now playing"))
	if len(ps) == 0 {
		b.add(sDim.Render("   nothing is playing (players: mpv with mpv-mpris, VLC, Celluloid, Haruna, …)"))
	}
	for i := range ps {
		p := ps[i]
		sel := i == m.nSel
		gut := "   "
		if sel {
			gut = sAccent.Render(" ┃ ")
		}
		pick := func(f func() tea.Cmd) func() tea.Cmd {
			return func() tea.Cmd { m.nSel = i; return f() }
		}
		icon := "⏸"
		if p.State == "Playing" {
			icon = sOK.Render("▶")
		}
		head := []seg{txt(gut + icon + " " + sBold.Render(p.Player) + sDim.Render(" · "+strings.ToLower(p.State)) + "  ")}
		head = append(head, button("u mark watched", pick(func() tea.Cmd { return m.markWatched(&p) })), txt(" "),
			button("m change match", pick(func() tea.Cmd { return m.changeMatch(&p) })))
		b.addSegs(head...)
		file := p.File
		if strings.HasPrefix(file, "/") {
			file = filepath.Base(file)
		}
		b.addSegs(seg{text: gut + fit(file, w-4), click: onClick(func() tea.Cmd { m.nSel = i; return m.selChanged() })})
		det := sDim.Render("detected  ")
		switch {
		case p.Parsed == "":
			det += sWarn.Render("no title in this file name")
		case p.Episode > 0:
			det += p.Parsed + sDim.Render(fmt.Sprintf(" · episode %d", p.Episode))
		default:
			det += p.Parsed + sDim.Render(" · no episode number")
		}
		if p.Season > 1 {
			det += sDim.Render(fmt.Sprintf(" · season %d", p.Season))
		}
		b.add(gut + det)
		mt := sDim.Render("match     ")
		switch {
		case p.Ignored:
			mt += sDim.Render("ignored title")
		case p.Pending:
			mt += sWarn.Render("waiting for you to pick — press m")
		case p.MatchID != 0:
			mt += sAccent.Render(p.Match)
			if e := m.listEntry(&row{AnilistID: p.MatchID, MalID: p.MatchMal}); e != nil {
				mt += "  " + statusStyle(e.Status).Render(fmt.Sprintf("● %s %d/%s", statusName(e.Status), e.Progress, epsStr(e.Episodes)))
			} else {
				mt += sDim.Render("  not on your list yet")
			}
		default:
			mt += sDim.Render("not matched yet (matched when it counts)")
		}
		b.add(gut + mt)
		thr := m.threshold()
		var prog string
		switch {
		case !p.Tracked:
			prog = sDim.Render("not tracked (too short, not a video, or filtered by your config)")
		case p.Counted:
			prog = bar(1, 24, cOK) + sOK.Render("  ✓ counted")
		default:
			prog = bar(p.Watched, 24, cAccent) + fmt.Sprintf("  %.0f%%", p.Watched*100) + sDim.Render(fmt.Sprintf(" watched · counts at %.0f%%", thr*100))
		}
		b.add(gut+prog, "")
	}

	if len(m.pending) > 0 {
		b.add(sHead.Render(fmt.Sprintf(" Needs a match (%d)", len(m.pending))))
		for j, p := range m.pending {
			i := len(ps) + j
			p := p
			gut := "   "
			if i == m.nSel {
				gut = sAccent.Render(" ┃ ")
			}
			kind := ""
			if p.Kind == "vn" {
				kind = sDim.Render(" [VN]")
			}
			guess := ""
			if len(p.Candidates) > 0 {
				guess = sDim.Render("  best guess: " + p.Candidates[0].Title)
			}
			sel := func(f func() tea.Cmd) func() tea.Cmd { return func() tea.Cmd { m.nSel = i; return f() } }
			b.addSegs(txt(gut+p.Query+kind+"  "), button("pick", sel(func() tea.Cmd { return m.resolvePending(p) })), txt(" "),
				button("ignore", sel(func() tea.Cmd { return ignoreCmd(p.Key, p.Query) })), txt(guess))
		}
		b.add("")
	}

	if m.st != nil && len(m.st.Queue) > 0 {
		head := fmt.Sprintf(" Queued updates (%d)", len(m.st.Queue))
		b.add(sHead.Render(head))
		if m.st.LastErr != "" {
			b.add(sWarn.Render("   offline or service unavailable — retrying " + retryIn(m.st)))
		}
		for _, it := range m.st.Queue {
			t := it.TargetTitle
			if t == "" {
				t = it.Title
			}
			what := ""
			switch it.Kind {
			case "anime":
				ep := it.Episode
				if it.TargetEp > 0 {
					ep = it.TargetEp
				}
				what = fmt.Sprintf("episode %d", ep)
				if it.TargetID == 0 {
					what += " · not matched yet"
				}
			case "set":
				what = "list edit"
			case "vn":
				what = it.Action
			}
			line := "   " + t + "  " + sDim.Render(what)
			if it.LastErr != "" {
				line += sDim.Render("  (" + it.LastErr + ")")
			}
			b.add(fit(line, w))
		}
	} else if m.st != nil {
		b.add(sDim.Render(" Nothing queued — everything is synced."))
	}
	return b
}

func retryIn(st *daemon.Status) string {
	if st.RetryAt.IsZero() {
		return "soon"
	}
	return "in " + until(st.RetryAt.Unix())
}

func (m *model) threshold() float64 {
	if m.thr > 0 {
		return m.thr
	}
	return 0.8
}
