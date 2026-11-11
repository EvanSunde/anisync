package tui

import (
	"math"
	"os"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/unix"

	"github.com/evansunde/anisync/internal/kitty"
)

var toPNG = kitty.ToPNG

// ttyOut serializes writes to the terminal so image data sent from a
// background command never interleaves with Bubble Tea's frame output. It
// implements term.File so Bubble Tea still treats it as a TTY.
type ttyOut struct {
	mu sync.Mutex
	f  *os.File
}

func (t *ttyOut) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.f.Write(b)
}
func (t *ttyOut) Read(b []byte) (int, error) { return t.f.Read(b) }
func (t *ttyOut) Close() error               { return nil }
func (t *ttyOut) Fd() uintptr                { return t.f.Fd() }

type covers struct {
	enabled    bool
	out        *ttyOut
	cols, rows int

	mu      sync.Mutex
	state   map[uint32]int // 0 none, 1 loading, 2 ready, 3 failed
	pending map[uint32]bool
}

const (
	coverNone = iota
	coverLoading
	coverReady
	coverFailed
)

func newCovers(out *ttyOut, enabled bool) *covers {
	c := &covers{enabled: enabled, out: out, rows: 12, state: map[uint32]int{}}
	// Size the box to the cover's 2:3 aspect using the real cell size.
	ratio := 2.1 // cell height / width, typical fallback
	if ws, err := unix.IoctlGetWinsize(int(out.Fd()), unix.TIOCGWINSZ); err == nil && ws.Xpixel > 0 && ws.Col > 0 && ws.Row > 0 {
		cw := float64(ws.Xpixel) / float64(ws.Col)
		ch := float64(ws.Ypixel) / float64(ws.Row)
		if cw > 0 && ch > 0 {
			ratio = ch / cw
		}
	}
	c.cols = int(math.Round(float64(c.rows) * ratio * 0.70))
	return c
}

func (c *covers) get(id uint32) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state[id]
}

// request starts loading a cover; returns nil if not needed.
func (c *covers) request(id uint32, url string) tea.Cmd {
	if !c.enabled || id == 0 {
		return nil
	}
	c.mu.Lock()
	if c.state[id] != coverNone {
		c.mu.Unlock()
		return nil
	}
	c.state[id] = coverLoading
	c.mu.Unlock()
	return func() tea.Msg {
		data, err := fetchCover(id, url)
		if err == nil {
			err = kitty.Transmit(c.out, id, data, c.cols, c.rows)
		}
		c.mu.Lock()
		if err != nil {
			c.state[id] = coverFailed
		} else {
			c.state[id] = coverReady
		}
		c.mu.Unlock()
		return coverMsg{id: id, err: err}
	}
}

// cleanup frees all transmitted images.
func (c *covers) cleanup() {
	if !c.enabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, s := range c.state {
		if s == coverReady {
			kitty.Delete(c.out, id)
		}
	}
}
