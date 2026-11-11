package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/evansunde/anisync/internal/ipc"
	"github.com/evansunde/anisync/internal/listcache"
	"github.com/evansunde/anisync/internal/media"
	"github.com/evansunde/anisync/internal/mpris"
	"github.com/evansunde/anisync/internal/store"
)

type Status struct {
	Services map[string]string `json:"services"` // service -> user ("" = not logged in / disabled)
	Players  []mpris.Status    `json:"players"`
	Queue    []*store.Item     `json:"queue"`
	Pending  int               `json:"pending"`
	LastErr  string            `json:"last_error,omitempty"`
	RetryAt  time.Time         `json:"retry_at,omitempty"`
	VNActive map[string]string `json:"vn_active,omitempty"`
	AuthErr  []string          `json:"auth_errors,omitempty"`
	Now      []NowPlaying      `json:"now,omitempty"`
}

// NowPlaying is a player's current file as the daemon understands it.
type NowPlaying struct {
	mpris.Status
	File     string `json:"file,omitempty"`
	Parsed   string `json:"parsed,omitempty"` // title parsed from the file name
	Season   int    `json:"season,omitempty"`
	Episode  int    `json:"episode,omitempty"`
	Key      string `json:"key,omitempty"` // mapping key
	Match    string `json:"match,omitempty"`
	MatchID  int    `json:"match_id,omitempty"`
	MatchMal int    `json:"match_mal,omitempty"`
	Ignored  bool   `json:"ignored,omitempty"`
	Pending  bool   `json:"pending,omitempty"` // waiting for the user to pick
}

type ListReply struct {
	List  listcache.List `json:"list"`
	Error string         `json:"error,omitempty"`
}

type SearchArgs struct {
	Kind  string `json:"kind"`
	Query string `json:"query"`
}

type VNFinishArgs struct {
	Name string `json:"name"`
	Vote int    `json:"vote"`
}

type VNInfo struct {
	Folder     string        `json:"folder"`
	Key        string        `json:"key"`
	Title      string        `json:"title,omitempty"`
	VNDBID     string        `json:"vndb_id,omitempty"`
	Playtime   time.Duration `json:"playtime"`
	LastPlayed time.Time     `json:"last_played"`
}

type MappingInfo struct {
	Key string `json:"key"`
	store.Mapping
}

func (d *Daemon) handleIPC(r ipc.Request) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch r.Cmd {
	case "status":
		return d.status(), nil
	case "pending":
		d.st.Lock()
		defer d.st.Unlock()
		out := make([]*store.Pending, 0, len(d.st.S.Pending))
		for _, p := range d.st.S.Pending {
			out = append(out, p)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
		return out, nil
	case "search":
		var a SearchArgs
		if err := json.Unmarshal(r.Args, &a); err != nil {
			return nil, err
		}
		return d.search(ctx, a)
	case "resolve":
		var a ResolveArgs
		if err := json.Unmarshal(r.Args, &a); err != nil {
			return nil, err
		}
		return nil, d.Resolve(a)
	case "map":
		// map a title (pending or not) to a show; queued episodes follow
		var a ResolveArgs
		if err := json.Unmarshal(r.Args, &a); err != nil {
			return nil, err
		}
		d.st.Lock()
		_, pending := d.st.S.Pending[a.Key]
		if !pending {
			d.st.S.Mappings[a.Key] = store.Mapping{}
			delete(d.st.S.Ignored, a.Key)
		}
		d.st.Unlock()
		return nil, d.Resolve(a)
	case "list":
		return ListReply{List: d.cacheCopy()}, nil
	case "list-refresh":
		var rep ListReply
		d.syncMu.Lock()
		err := d.refreshList(ctx)
		d.syncMu.Unlock()
		if err != nil {
			rep.Error = err.Error()
		}
		rep.List = d.cacheCopy()
		return rep, nil
	case "watched-now":
		var bus string
		_ = json.Unmarshal(r.Args, &bus)
		ev, err := d.watcher.MarkWatched(bus)
		if err != nil {
			return nil, err
		}
		if !d.onWatched(ev) {
			return nil, errors.New("couldn't find an episode number in this file's name")
		}
		return firstNonEmpty(media.FromURL(ev.URL), ev.Title), nil
	case "mappings":
		d.st.Lock()
		defer d.st.Unlock()
		var out []MappingInfo
		for k, m := range d.st.S.Mappings {
			out = append(out, MappingInfo{Key: k, Mapping: m})
		}
		for k, t := range d.st.S.Ignored {
			out = append(out, MappingInfo{Key: k, Mapping: store.Mapping{Title: "(ignored) " + t}})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
		return out, nil
	case "forget":
		var q string
		_ = json.Unmarshal(r.Args, &q)
		return d.forget(q)
	case "sync":
		d.stateMu.Lock()
		d.backoff, d.listFetched = 0, time.Time{}
		d.authBroken = map[string]bool{}
		d.stateMu.Unlock()
		d.Kick()
		return nil, nil
	case "reload":
		if err := d.loadTokens(); err != nil {
			return nil, err
		}
		d.Kick()
		return nil, nil
	case "history":
		var n int
		_ = json.Unmarshal(r.Args, &n)
		d.st.Lock()
		defer d.st.Unlock()
		h := d.st.S.History
		if n > 0 && len(h) > n {
			h = h[len(h)-n:]
		}
		return h, nil
	case "undo":
		var id int64
		_ = json.Unmarshal(r.Args, &id)
		return d.undo(ctx, id)
	case "set":
		var a SetArgs
		if err := json.Unmarshal(r.Args, &a); err != nil {
			return nil, err
		}
		return d.enqueueSet(a)
	case "mal-list":
		if !d.enabled(svcMAL) {
			return nil, errors.New("not logged in to MAL")
		}
		return d.mal.List(ctx)
	case "vn-list":
		return d.vnList(), nil
	case "vn-finish":
		var a VNFinishArgs
		if err := json.Unmarshal(r.Args, &a); err != nil {
			return nil, err
		}
		return d.vnFinish(a)
	}
	return nil, fmt.Errorf("unknown command %q", r.Cmd)
}

func (d *Daemon) status() Status {
	s := Status{Services: map[string]string{}, Players: d.watcher.Snapshot()}
	s.Now = d.nowPlaying(s.Players)
	d.tokMu.Lock()
	if d.cfg.AniList.Enabled && d.tok.AniList.Token != "" {
		s.Services[svcAniList] = d.tok.AniList.User
	}
	if d.cfg.MAL.Enabled && d.mal != nil {
		s.Services[svcMAL] = d.tok.MAL.User
	}
	if d.cfg.VNDB.Enabled && d.vndb != nil {
		s.Services[svcVNDB] = d.tok.VNDB.User
	}
	d.tokMu.Unlock()
	d.st.Lock()
	s.Queue = append(s.Queue, d.st.S.Queue...)
	s.Pending = len(d.st.S.Pending)
	vnNames := map[string]string{}
	for k, e := range d.st.S.VN {
		vnNames[e.Dir] = k
	}
	d.st.Unlock()
	d.stateMu.Lock()
	s.LastErr, s.RetryAt = d.lastErr, d.retryAt
	for k, v := range d.authBroken {
		if v {
			s.AuthErr = append(s.AuthErr, k)
		}
	}
	d.stateMu.Unlock()
	if d.scanner != nil {
		s.VNActive = map[string]string{}
		for k, dur := range d.scanner.Active() {
			s.VNActive[k] = dur.Round(time.Second).String()
		}
	}
	return s
}

func (d *Daemon) search(ctx context.Context, a SearchArgs) ([]store.Candidate, error) {
	if a.Kind == "vn" {
		if d.vndb == nil {
			return nil, errors.New("vndb not configured")
		}
		res, err := d.vndb.Search(ctx, a.Query)
		if err != nil {
			return nil, err
		}
		var out []store.Candidate
		for _, v := range res {
			c := store.Candidate{VNDBID: v.ID, Title: v.Title, Alt: v.AltTitle}
			if len(v.Released) >= 4 {
				fmt.Sscanf(v.Released[:4], "%d", &c.Year)
			}
			out = append(out, c)
		}
		return out, nil
	}
	res, err := d.al.Search(ctx, a.Query)
	if err != nil {
		return nil, err
	}
	return d.candidates(a.Query, res), nil
}

// forget removes a mapping/ignore whose key or title contains q, so the next
// episode gets matched again (fixes a wrong auto-match).
func (d *Daemon) forget(q string) ([]string, error) {
	nq := media.Normalize(q)
	if nq == "" {
		return nil, errors.New("give a title")
	}
	d.st.Lock()
	defer d.st.Unlock()
	var removed []string
	for k, m := range d.st.S.Mappings {
		if strings.Contains(k, nq) || strings.Contains(media.Normalize(m.Title), nq) {
			delete(d.st.S.Mappings, k)
			removed = append(removed, fmt.Sprintf("%s → %s", k, m.Title))
		}
	}
	for k, t := range d.st.S.Ignored {
		if strings.Contains(k, nq) {
			delete(d.st.S.Ignored, k)
			removed = append(removed, fmt.Sprintf("%s (ignored %s)", k, t))
		}
	}
	return removed, d.st.Save()
}

func (d *Daemon) undo(ctx context.Context, id int64) (*store.HistoryEntry, error) {
	d.st.Lock()
	var h *store.HistoryEntry
	for i := len(d.st.S.History) - 1; i >= 0; i-- {
		e := d.st.S.History[i]
		if (id == 0 && !e.Undone && (e.Kind == "anime" || e.Kind == "set") && len(e.Before) > 0) || e.ID == id {
			h = e
			break
		}
	}
	d.st.Unlock()
	if h == nil {
		return nil, errors.New("nothing to undo")
	}
	if h.Undone {
		return nil, errors.New("already undone")
	}
	if h.Kind != "anime" && h.Kind != "set" {
		return nil, errors.New("only anime updates can be undone")
	}
	for svc, before := range h.Before {
		var err error
		switch svc {
		case svcAniList:
			if !d.enabled(svcAniList) {
				err = errors.New("not logged in")
				break
			}
			if before.Exists {
				err = d.al.Save(ctx, h.AnilistID, before)
			} else {
				m, e := d.al.Media(ctx, h.AnilistID)
				if e == nil && m.MediaListEntry != nil {
					e = d.al.Delete(ctx, m.MediaListEntry.ID)
				}
				err = e
			}
		case svcMAL:
			if !d.enabled(svcMAL) {
				err = errors.New("not logged in")
				break
			}
			if before.Exists {
				err = d.mal.Save(ctx, h.MalID, before)
			} else {
				err = d.mal.Delete(ctx, h.MalID)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", svcName(svc), err)
		}
	}
	d.st.Lock()
	h.Undone = true
	_ = d.st.Save()
	d.st.Unlock()
	d.stateMu.Lock()
	d.listFetched = time.Time{}
	d.stateMu.Unlock()
	log.Printf("undid update %d (%s ep %d)", h.ID, h.Title, h.Episode)
	return h, nil
}

func (d *Daemon) vnList() []VNInfo {
	d.st.Lock()
	defer d.st.Unlock()
	var out []VNInfo
	for k, e := range d.st.S.VN {
		m := d.st.S.Mappings[k]
		out = append(out, VNInfo{Folder: e.Dir, Key: k, Title: m.Title, VNDBID: m.VNDBID, Playtime: e.Playtime, LastPlayed: e.LastPlayed})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastPlayed.After(out[j].LastPlayed) })
	return out
}

func (d *Daemon) vnFinish(a VNFinishArgs) (string, error) {
	nq := media.Normalize(a.Name)
	d.st.Lock()
	defer d.st.Unlock()
	var matches []string
	var keys []string
	for k, e := range d.st.S.VN {
		m := d.st.S.Mappings[k]
		if nq == "" || strings.Contains(media.Normalize(e.Dir), nq) || strings.Contains(media.Normalize(m.Title), nq) || m.VNDBID == a.Name {
			matches = append(matches, e.Dir)
			keys = append(keys, k)
		}
	}
	if nq == "" && len(keys) > 1 { // default: most recently played
		var best string
		var bt time.Time
		for _, k := range keys {
			if e := d.st.S.VN[k]; e.LastPlayed.After(bt) {
				best, bt = k, e.LastPlayed
			}
		}
		keys = []string{best}
		matches = []string{d.st.S.VN[best].Dir}
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("no tracked VN matches %q (see: anisync vn list)", a.Name)
	}
	if len(keys) > 1 {
		return "", fmt.Errorf("ambiguous, matches: %s", strings.Join(matches, ", "))
	}
	d.st.S.Queue = append(d.st.S.Queue, &store.Item{ID: d.st.NewID(), Kind: "vn", Key: keys[0], Title: matches[0],
		Action: "finish", Vote: a.Vote, Created: time.Now()})
	err := d.st.Save()
	d.Kick()
	return matches[0], err
}
