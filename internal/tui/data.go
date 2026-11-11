package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/config"
	"github.com/evansunde/anisync/internal/daemon"
	"github.com/evansunde/anisync/internal/ipc"
	"github.com/evansunde/anisync/internal/listcache"
	"github.com/evansunde/anisync/internal/netx"
	"github.com/evansunde/anisync/internal/query"
	"github.com/evansunde/anisync/internal/store"
)

// row is one show as displayed (search result, list entry, related show).
type row = listcache.Entry

// ---------------------------------------------------------------- messages

type searchMsg struct {
	seq  int
	page int
	res  anilist.Page
	err  error
}
type detailMsg struct {
	key uint32
	m   *anilist.Media
	err error
}
type listMsg struct {
	list   *listcache.List
	err    string
	synced bool // fresh from the server (vs. the local copy)
}
type statusMsg struct {
	st  *daemon.Status
	err error
}
type pendingMsg struct {
	p   []*store.Pending
	err error
}
type vocabMsg struct{ v *query.Vocab }
type coverMsg struct {
	id  uint32
	err error
}
type actionMsg struct {
	text   string
	err    error
	reload bool // re-read the list afterwards
}
type tickMsg time.Time
type debounceMsg struct{ id uint32 }
type liveSearchMsg struct {
	seq int
	vn  bool
}

func ctx20() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 20*time.Second)
}

func call(cmd string, args, out any) error { return ipc.Call(config.SocketPath(), cmd, args, out) }

func daemonErr(err error) error {
	if errors.Is(err, ipc.ErrNotRunning) {
		return errors.New("daemon not running — start it to edit (systemctl --user start anisync)")
	}
	return err
}

// ---------------------------------------------------------------- commands

func (m *model) searchCmd(f anilist.Filter, page, seq int) tea.Cmd {
	al := m.al
	return func() tea.Msg {
		if f.OnList != nil && al.Token == "" {
			f.OnList = nil
		}
		ctx, cancel := ctx20()
		defer cancel()
		res, err := al.Browse(ctx, f, page)
		return searchMsg{seq: seq, page: page, res: res, err: err}
	}
}

func (m *model) detailCmd(r row) tea.Cmd {
	al := m.al
	key := r.Key()
	return func() tea.Msg {
		ctx, cancel := ctx20()
		defer cancel()
		id := r.AnilistID
		if id == 0 && r.MalID != 0 {
			mm, err := al.ByMalID(ctx, r.MalID)
			if err != nil {
				return detailMsg{key: key, err: err}
			}
			id = mm.ID
		}
		d, err := al.Detail(ctx, id)
		return detailMsg{key: key, m: d, err: err}
	}
}

// localListCmd reads the on-disk list (instant, offline).
func localListCmd() tea.Cmd {
	return func() tea.Msg {
		l, err := listcache.Load()
		if err != nil {
			return listMsg{err: err.Error()}
		}
		return listMsg{list: l}
	}
}

// daemonListCmd gets the daemon's copy (includes queued changes), no network.
func daemonListCmd() tea.Cmd {
	return func() tea.Msg {
		var rep daemon.ListReply
		if err := call("list", nil, &rep); err != nil {
			return nil
		}
		return listMsg{list: &rep.List}
	}
}

// syncListCmd refreshes the list from the server: through the daemon if it
// runs (it keeps the cache), else directly.
func (m *model) syncListCmd() tea.Cmd {
	al, uid := m.al, m.userID
	return func() tea.Msg {
		var rep daemon.ListReply
		err := call("list-refresh", nil, &rep)
		if err == nil {
			return listMsg{list: &rep.List, err: rep.Error, synced: rep.Error == ""}
		}
		if !errors.Is(err, ipc.ErrNotRunning) {
			return listMsg{err: err.Error()}
		}
		if al.Token == "" {
			return listMsg{err: "not logged in to AniList, and the daemon (for MAL) isn't running"}
		}
		ctx, cancel := ctx20()
		defer cancel()
		items, err := al.ListFull(ctx, uid)
		if err != nil {
			return listMsg{err: err.Error()}
		}
		l := &listcache.List{Fetched: time.Now(), Source: "anilist", Entries: listcache.FromAniList(items)}
		_ = l.Save()
		return listMsg{list: l, synced: true}
	}
}

func statusCmd() tea.Cmd {
	return func() tea.Msg {
		var st daemon.Status
		if err := call("status", nil, &st); err != nil {
			return statusMsg{err: err}
		}
		return statusMsg{st: &st}
	}
}

func pendingCmd() tea.Cmd {
	return func() tea.Msg {
		var p []*store.Pending
		err := call("pending", nil, &p)
		return pendingMsg{p: p, err: err}
	}
}

func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func debounce(id uint32) tea.Cmd {
	return tea.Tick(180*time.Millisecond, func(time.Time) tea.Msg { return debounceMsg{id: id} })
}

func liveSearch(seq int, vn bool) tea.Cmd {
	return tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg { return liveSearchMsg{seq: seq, vn: vn} })
}

func setCmd(a daemon.SetArgs, desc string) tea.Cmd {
	return func() tea.Msg {
		var id int64
		if err := call("set", a, &id); err != nil {
			return actionMsg{err: daemonErr(err)}
		}
		return actionMsg{text: desc, reload: true}
	}
}

func mapCmd(a daemon.ResolveArgs, desc string) tea.Cmd {
	return func() tea.Msg {
		if err := call("map", a, nil); err != nil {
			return actionMsg{err: daemonErr(err)}
		}
		return actionMsg{text: desc, reload: true}
	}
}

func ignoreCmd(key, title string) tea.Cmd {
	return func() tea.Msg {
		if err := call("resolve", daemon.ResolveArgs{Key: key, Ignore: true}, nil); err != nil {
			return actionMsg{err: daemonErr(err)}
		}
		return actionMsg{text: "ignoring “" + title + "” from now on"}
	}
}

func watchedNowCmd(bus string) tea.Cmd {
	return func() tea.Msg {
		var src string
		if err := call("watched-now", bus, &src); err != nil {
			return actionMsg{err: daemonErr(err)}
		}
		return actionMsg{text: "marked as watched: " + filepath.Base(src) + " (queued)", reload: true}
	}
}

// ---------------------------------------------------------------- caches

func cacheDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "anisync")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "anisync")
}

// vocabCmd loads genre and tag names (cached for a week).
func vocabCmd() tea.Cmd {
	return func() tea.Msg {
		path := filepath.Join(cacheDir(), "collections.json")
		var c struct {
			Genres, Tags []string
			Fetched      time.Time
		}
		if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &c) == nil && time.Since(c.Fetched) < 7*24*time.Hour {
			return vocabMsg{query.NewVocab(c.Genres, c.Tags)}
		}
		ctx, cancel := ctx20()
		defer cancel()
		g, t, err := (&anilist.Client{}).Collections(ctx)
		if err != nil {
			if len(c.Genres) > 0 {
				return vocabMsg{query.NewVocab(c.Genres, c.Tags)}
			}
			return vocabMsg{nil}
		}
		c.Genres, c.Tags, c.Fetched = g, t, time.Now()
		if b, err := json.Marshal(c); err == nil {
			os.MkdirAll(filepath.Dir(path), 0o755)
			os.WriteFile(path, b, 0o644)
		}
		return vocabMsg{query.NewVocab(g, t)}
	}
}

func coverPath(id uint32) string {
	return filepath.Join(cacheDir(), "covers", fmt.Sprintf("%d.png", id))
}

func coverCached(id uint32) bool {
	_, err := os.Stat(coverPath(id))
	return err == nil
}

// fetchCover downloads (or loads from cache) a cover as PNG.
func fetchCover(id uint32, url string) ([]byte, error) {
	path := coverPath(id)
	if b, err := os.ReadFile(path); err == nil {
		return b, nil
	}
	if url == "" {
		return nil, errors.New("no cover")
	}
	ctx, cancel := ctx20()
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", netx.UserAgent)
	resp, err := netx.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("cover: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	pngData, err := toPNG(raw)
	if err != nil {
		return nil, err
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, pngData, 0o644)
	return pngData, nil
}
