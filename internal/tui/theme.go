package tui

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Colors use the terminal's 16-color palette (so they follow your kitty
// theme) plus a few 256-color greys for surfaces.
var (
	cAccent  = lipgloss.Color("12")
	cAccent2 = lipgloss.Color("13")
	cDim     = lipgloss.Color("8")
	cText    = lipgloss.Color("15")
	cErr     = lipgloss.Color("9")
	cOK      = lipgloss.Color("10")
	cWarn    = lipgloss.Color("11")
	cSurface = lipgloss.Color("236")
	cSel     = lipgloss.Color("237")
	cChip    = lipgloss.Color("238")

	sAccent = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sBold   = lipgloss.NewStyle().Bold(true)
	sErr    = lipgloss.NewStyle().Foreground(cErr)
	sOK     = lipgloss.NewStyle().Foreground(cOK)
	sWarn   = lipgloss.NewStyle().Foreground(cWarn)
	sHead   = lipgloss.NewStyle().Foreground(cAccent2).Bold(true)
	sTabOn  = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(cAccent).Bold(true).Padding(0, 1)
	sTabOff = lipgloss.NewStyle().Foreground(cDim).Padding(0, 1)
	sModeOn = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(cAccent2).Bold(true).Padding(0, 1)
	sKey    = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sLink   = lipgloss.NewStyle().Foreground(cAccent).Underline(true)
	sLinkOn = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(cAccent).Bold(true)
	sButton = lipgloss.NewStyle().Foreground(cText).Background(cChip).Padding(0, 1)
)

var statusColor = map[string]lipgloss.Color{"CURRENT": "10", "REPEATING": "13", "PLANNING": "14", "COMPLETED": "12", "PAUSED": "11", "DROPPED": "9"}

func statusName(s string) string {
	return map[string]string{"CURRENT": "Watching", "REPEATING": "Rewatching", "PLANNING": "Planning",
		"COMPLETED": "Completed", "PAUSED": "Paused", "DROPPED": "Dropped", "": "All"}[s]
}

func statusStyle(s string) lipgloss.Style {
	if c, ok := statusColor[s]; ok {
		return lipgloss.NewStyle().Foreground(c)
	}
	return lipgloss.NewStyle().Foreground(cAccent)
}

var formatName = map[string]string{"TV": "TV", "TV_SHORT": "Short", "MOVIE": "Movie", "SPECIAL": "Special", "OVA": "OVA",
	"ONA": "ONA", "MUSIC": "Music", "UNKNOWN": "?"}

var formatColor = map[string]lipgloss.Color{"TV": "12", "TV_SHORT": "14", "MOVIE": "13", "SPECIAL": "11", "OVA": "6", "ONA": "6", "MUSIC": "5"}

var airName = map[string]string{"FINISHED": "Finished", "RELEASING": "Airing", "NOT_YET_RELEASED": "Upcoming",
	"CANCELLED": "Cancelled", "HIATUS": "Hiatus", "finished_airing": "Finished", "currently_airing": "Airing", "not_yet_aired": "Upcoming"}

func formatBadge(f string) string {
	n := formatName[f]
	if n == "" {
		n = f
	}
	c, ok := formatColor[f]
	if !ok {
		c = cDim
	}
	return lipgloss.NewStyle().Foreground(c).Render(fmt.Sprintf("%-5s", n))
}

// scoreStyle colors an average score: green ≥ 80, yellow ≥ 70, orange ≥ 60.
func scoreStyle(s int) lipgloss.Style {
	switch {
	case s >= 80:
		return lipgloss.NewStyle().Foreground(cOK)
	case s >= 70:
		return lipgloss.NewStyle().Foreground(cWarn)
	case s >= 60:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	case s > 0:
		return lipgloss.NewStyle().Foreground(cErr)
	}
	return sDim
}

var genrePalette = []lipgloss.Color{"13", "14", "11", "10", "12", "9", "6", "5"}

func genreStyle(g string) lipgloss.Style {
	h := fnv.New32a()
	h.Write([]byte(g))
	return lipgloss.NewStyle().Foreground(genrePalette[h.Sum32()%uint32(len(genrePalette))])
}

func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// fitTitle truncates in the middle so the distinguishing end of a title
// ("…3rd Season", "…Part 2") stays visible, then pads to w.
func fitTitle(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return fit(s, w)
	}
	tail := min(16, w/3)
	if tail < 4 {
		return fit(s, w)
	}
	r := []rune(s)
	end := string(r[max(0, len(r)-tail):])
	for lipgloss.Width(end) > tail && len([]rune(end)) > 1 {
		end = string([]rune(end)[1:])
	}
	head := ansi.Truncate(s, w-lipgloss.Width(end)-1, "")
	return fit(head+"…"+end, w)
}

func until(unix int64) string {
	d := time.Until(time.Unix(unix, 0))
	if d < 0 {
		return "aired"
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero():
		return "never"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func humanInt(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func epsStr(n int) string {
	if n == 0 {
		return "?"
	}
	return fmt.Sprint(n)
}

// bar renders a progress bar of width w.
func bar(frac float64, w int, c lipgloss.Color) string {
	frac = max(0, min(1, frac))
	full := int(frac*float64(w) + 0.5)
	return lipgloss.NewStyle().Foreground(c).Render(strings.Repeat("━", full)) + sDim.Render(strings.Repeat("━", w-full))
}

// ---------------------------------------------------------------- click map

// hit is a clickable screen region (rebuilt on every render).
type hit struct {
	x0, x1, y int
	tag       int // >0: detail link index+1
	click     func(m tea.MouseMsg) tea.Cmd
}

// area is a scrollable screen region.
type area struct {
	x0, y0, x1, y1 int
	wheel          func(dir int) tea.Cmd
}

// block is rendered lines plus the click targets inside them (relative).
type block struct {
	lines []string
	hits  []hit
}

func (b *block) add(lines ...string) { b.lines = append(b.lines, lines...) }

// addClick appends a line built from segments; segments with an action get
// a hit region. Returns the line index.
func (b *block) addSegs(segs ...seg) int {
	y := len(b.lines)
	var sb strings.Builder
	x := 0
	for _, s := range segs {
		w := lipgloss.Width(s.text)
		if s.click != nil {
			b.hits = append(b.hits, hit{x0: x, x1: x + w, y: y, tag: s.tag, click: s.click})
		}
		sb.WriteString(s.text)
		x += w
	}
	b.lines = append(b.lines, sb.String())
	return y
}

type seg struct {
	text  string
	click func(m tea.MouseMsg) tea.Cmd
	tag   int
	glue  bool // stays on the same line as the previous segment, no separator
}

func txt(s string) seg { return seg{text: s} }

// wrapSegs lays out segments into lines of width w (for chips, genres, tags).
// Glued segments move with the segment before them.
func wrapSegs(b *block, prefix string, segs []seg, sep string, w int) {
	pw := lipgloss.Width(prefix)
	line := []seg{txt(prefix)}
	x := pw
	for i := 0; i < len(segs); {
		j := i + 1
		gw := lipgloss.Width(segs[i].text)
		for j < len(segs) && segs[j].glue {
			gw += lipgloss.Width(segs[j].text)
			j++
		}
		if len(line) > 1 && x+lipgloss.Width(sep)+gw > w {
			b.addSegs(line...)
			line = []seg{txt(strings.Repeat(" ", pw))}
			x = pw
		} else if len(line) > 1 {
			line = append(line, txt(sep))
			x += lipgloss.Width(sep)
		}
		line = append(line, segs[i:j]...)
		x += gw
		i = j
	}
	b.addSegs(line...)
}

// highlight paints a background across a whole styled line (inner resets
// would otherwise clear it after the first colored segment).
func highlight(line string, w int) string {
	bg := "\x1b[48;5;237m"
	line = fit(line, w)
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+bg)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+bg)
	line = strings.ReplaceAll(line, "\x1b[49m", bg)
	return bg + line + "\x1b[0m"
}

func onClick(f func() tea.Cmd) func(tea.MouseMsg) tea.Cmd {
	return func(tea.MouseMsg) tea.Cmd { return f() }
}
