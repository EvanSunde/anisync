// Package daemon wires the MPRIS watcher, VN scanner, offline queue and the
// AniList / MAL / VNDB clients together.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/config"
	"github.com/evansunde/anisync/internal/ipc"
	"github.com/evansunde/anisync/internal/listcache"
	"github.com/evansunde/anisync/internal/mal"
	"github.com/evansunde/anisync/internal/media"
	"github.com/evansunde/anisync/internal/mpris"
	"github.com/evansunde/anisync/internal/netx"
	"github.com/evansunde/anisync/internal/notify"
	"github.com/evansunde/anisync/internal/store"
	"github.com/evansunde/anisync/internal/vn"
	"github.com/evansunde/anisync/internal/vndb"
	"github.com/evansunde/anisync/internal/vnlist"
)

type Daemon struct {
	cfg   config.Config
	st    *store.Store
	notif *notify.Notifier

	tokMu sync.Mutex
	tok   store.Tokens
	al    *anilist.Client // always usable for search; Token empty if logged out
	mal   *mal.Client     // nil if not configured
	vndb  *vndb.Client    // nil if not configured

	watcher *mpris.Watcher
	scanner *vn.Scanner

	kick chan struct{}
	// syncMu serializes server syncs: queue processing and list refreshes
	// must not interleave, or a refresh can swap in a list that misses (or
	// re-marks as pending) an update that was just applied.
	syncMu sync.Mutex

	stateMu     sync.Mutex // guards the fields below
	authBroken  map[string]bool
	lastErr     string
	retryAt     time.Time
	backoff     time.Duration
	cache       *listcache.List // local copy of the user's list
	listFetched time.Time
	vncache     *vnlist.List // local copy of the user's VNDB list
	vnFetched   time.Time
	vnRetry     time.Time
}

const (
	svcAniList = "anilist"
	svcMAL     = "mal"
	svcVNDB    = "vndb"
)

func Run(cfg config.Config) error {
	st, err := store.Open(config.DataDir())
	if err != nil {
		return err
	}
	d := &Daemon{cfg: cfg, st: st, kick: make(chan struct{}, 1), authBroken: map[string]bool{}}
	if err := d.loadTokens(); err != nil {
		return err
	}
	d.loadCache()
	d.loadVNCache()

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("session bus: %w", err)
	}
	defer conn.Close()
	d.notif = notify.New(conn, cfg.Notify)
	d.watcher = mpris.New(conn, mpris.Options{
		Threshold: cfg.Threshold, Players: cfg.Players, MinLength: cfg.MinLength.Duration,
		Accept: d.accept, Coverage: d, OnWatched: func(ev mpris.Event) { d.onWatched(ev) },
	})
	sigs := make(chan *dbus.Signal, 64)
	conn.Signal(sigs)
	if err := d.watcher.Start(); err != nil {
		return fmt.Errorf("mpris: %w", err)
	}

	l, err := ipc.Serve(config.SocketPath(), d.handleIPC)
	if err != nil {
		return err
	}
	defer l.Close()
	defer os.Remove(config.SocketPath())

	stop := make(chan struct{})
	go d.watchNetwork(stop)
	go d.processLoop(stop)

	var scanDone chan struct{}
	if cfg.VNDB.Enabled && len(cfg.VNDB.Dirs) > 0 {
		d.scanner = vn.New(cfg.VNDB.Dirs)
		d.scanner.Interval = cfg.VNDB.ScanInterval.Duration
		d.scanner.MinSession = cfg.VNDB.MinSession.Duration
		d.scanner.OnStart = d.onVNStart
		d.scanner.OnStop = d.onVNStop
		scanDone = make(chan struct{})
		go func() { d.scanner.Run(stop); close(scanDone) }()
		log.Printf("vn: watching %s", strings.Join(cfg.VNDB.Dirs, ", "))
	}

	d.warnTokenExpiry()
	log.Printf("anisync running (anilist=%v mal=%v vndb=%v)", d.enabled(svcAniList), d.enabled(svcMAL), d.enabled(svcVNDB))

	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case s, ok := <-sigs:
			if !ok {
				return errors.New("session bus connection lost")
			}
			d.watcher.HandleSignal(s)
			d.notif.HandleSignal(s)
		case <-term:
			log.Printf("shutting down")
			d.watcher.Flush()
			close(stop)
			if scanDone != nil {
				<-scanDone // scanner flushes running VN sessions
			}
			d.st.Lock()
			err := d.st.Save()
			d.st.Unlock()
			return err
		}
	}
}

// ---------------------------------------------------------------- tokens

func (d *Daemon) loadTokens() error {
	t, err := store.LoadTokens(config.ConfigDir())
	if err != nil {
		return err
	}
	d.tokMu.Lock()
	defer d.tokMu.Unlock()
	d.tok = t
	d.al = &anilist.Client{Token: t.AniList.Token}
	d.mal = nil
	if d.cfg.MAL.ClientID != "" && t.MAL.Refresh != "" {
		d.mal = &mal.Client{ClientID: d.cfg.MAL.ClientID, ClientSecret: d.cfg.MAL.ClientSecret,
			Access: t.MAL.Access, Refresh: t.MAL.Refresh, Expires: t.MAL.Expires, OnRefresh: d.saveMALTokens}
	}
	d.vndb = nil
	if t.VNDB.Token != "" {
		d.vndb = &vndb.Client{Token: t.VNDB.Token}
	}
	d.stateMu.Lock()
	d.authBroken = map[string]bool{}
	d.listFetched, d.vnFetched, d.vnRetry = time.Time{}, time.Time{}, time.Time{}
	d.stateMu.Unlock()
	return nil
}

func (d *Daemon) saveMALTokens(access, refresh string, exp time.Time) {
	d.tokMu.Lock()
	defer d.tokMu.Unlock()
	d.tok.MAL.Access, d.tok.MAL.Refresh, d.tok.MAL.Expires = access, refresh, exp
	if err := store.SaveTokens(config.ConfigDir(), d.tok); err != nil {
		log.Printf("save tokens: %v", err)
	}
}

func (d *Daemon) enabled(svc string) bool {
	d.tokMu.Lock()
	defer d.tokMu.Unlock()
	switch svc {
	case svcAniList:
		return d.cfg.AniList.Enabled && d.tok.AniList.Token != ""
	case svcMAL:
		return d.cfg.MAL.Enabled && d.mal != nil
	case svcVNDB:
		return d.cfg.VNDB.Enabled && d.vndb != nil
	}
	return false
}

func (d *Daemon) warnTokenExpiry() {
	d.tokMu.Lock()
	exp := d.tok.AniList.Expires
	d.tokMu.Unlock()
	if d.enabled(svcAniList) && !exp.IsZero() && time.Until(exp) < 14*24*time.Hour {
		d.notif.Send("AniList login expires soon", "Run: anisync auth anilist", nil, nil)
	}
}

// ---------------------------------------------------------------- coverage store (for the watcher)

func (d *Daemon) GetCoverage(key string) *store.Coverage {
	d.st.Lock()
	defer d.st.Unlock()
	c := d.st.S.Coverage[key]
	if c == nil {
		return nil
	}
	cp := *c
	cp.Spans = append([][2]float64(nil), c.Spans...)
	return &cp
}

func (d *Daemon) PutCoverage(key string, c *store.Coverage) {
	cp := *c
	cp.Spans = append([][2]float64(nil), c.Spans...)
	d.st.Lock()
	defer d.st.Unlock()
	d.st.S.Coverage[key] = &cp
	if err := d.st.Save(); err != nil {
		log.Printf("save: %v", err)
	}
}

// ---------------------------------------------------------------- detection

var videoExt = map[string]bool{".mkv": true, ".mp4": true, ".avi": true, ".webm": true, ".m4v": true, ".mov": true,
	".wmv": true, ".flv": true, ".ts": true, ".m2ts": true, ".ogm": true, ".ogv": true, ".rmvb": true, ".mpg": true, ".mpeg": true, ".3gp": true}

// accept decides whether a playing track is worth tracking at all.
func (d *Daemon) accept(url, title string) bool {
	path := media.FromURL(url)
	if path == "" {
		if url != "" && strings.HasPrefix(url, "file:") {
			return false
		}
		return d.cfg.Streams && title != "" // network stream / no url
	}
	if !videoExt[strings.ToLower(filepath.Ext(path))] {
		return false
	}
	for _, s := range d.cfg.IgnorePaths {
		if s != "" && strings.Contains(path, s) {
			return false
		}
	}
	if len(d.cfg.LibraryDirs) == 0 {
		return true
	}
	for _, dir := range d.cfg.LibraryDirs {
		if strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/") {
			return true
		}
	}
	return false
}

// parseTrack turns a player's url/title into (title, season, episode).
func parseTrack(url, title string) (p media.Parsed, src string, stream bool) {
	path := media.FromURL(url)
	if path == "" {
		return media.ParseTitle(title), title, true
	}
	return media.ParsePath(path), path, false
}

// onWatched queues a watched episode; false if it isn't one.
func (d *Daemon) onWatched(ev mpris.Event) bool {
	p, src, stream := parseTrack(ev.URL, ev.Title)
	if p.Skip || p.Title == "" || (stream && p.Episode == 0) {
		log.Printf("watched but not an episode, ignoring: %s", src)
		return false
	}
	key := media.Key(p.Title, p.Season)
	d.st.Lock()
	if t, ok := d.st.S.Ignored[key]; ok {
		d.st.Unlock()
		log.Printf("ignored title %q", t)
		return false
	}
	it := &store.Item{ID: d.st.NewID(), Kind: "anime", Key: key, Title: p.Title, Season: p.Season,
		Episode: p.Episode, Source: src, Stream: stream, Created: time.Now()}
	d.st.S.Queue = append(d.st.S.Queue, it)
	_ = d.st.Save()
	d.st.Unlock()
	log.Printf("queued: %q season=%d ep=%d (%s)", p.Title, p.Season, p.Episode, src)
	d.Kick()
	return true
}

// nowPlaying explains what each player is playing and what it maps to.
func (d *Daemon) nowPlaying(players []mpris.Status) []NowPlaying {
	var out []NowPlaying
	d.st.Lock()
	defer d.st.Unlock()
	for _, pl := range players {
		n := NowPlaying{Status: pl}
		if pl.URL == "" && pl.Media == "" {
			out = append(out, n)
			continue
		}
		p, src, _ := parseTrack(pl.URL, pl.Media)
		n.File, n.Parsed, n.Season, n.Episode = src, p.Title, p.Season, p.Episode
		if p.Title != "" {
			n.Key = media.Key(p.Title, p.Season)
			if m, ok := d.st.S.Mappings[n.Key]; ok && (m.AnilistID != 0 || m.MalID != 0) {
				n.Match, n.MatchID, n.MatchMal = m.Title, m.AnilistID, m.MalID
			}
			_, n.Ignored = d.st.S.Ignored[n.Key]
			_, n.Pending = d.st.S.Pending[n.Key]
		}
		out = append(out, n)
	}
	return out
}

// ---------------------------------------------------------------- queue processing

func (d *Daemon) Kick() {
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// watchNetwork kicks the queue as soon as NetworkManager reports
// connectivity. Without NetworkManager the backoff timer still retries.
func (d *Daemon) watchNetwork(stop chan struct{}) {
	sys, err := dbus.ConnectSystemBus()
	if err != nil {
		return
	}
	defer sys.Close()
	if err := sys.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.NetworkManager"), dbus.WithMatchMember("StateChanged")); err != nil {
		return
	}
	ch := make(chan *dbus.Signal, 8)
	sys.Signal(ch)
	for {
		select {
		case <-stop:
			return
		case s := <-ch:
			if len(s.Body) > 0 {
				if state, ok := s.Body[0].(uint32); ok && state == 70 { // NM_STATE_CONNECTED_GLOBAL
					log.Printf("network: online")
					d.stateMu.Lock()
					d.backoff = 0
					d.stateMu.Unlock()
					d.Kick()
				}
			}
		}
	}
}

const listRefresh = 6 * time.Hour

func (d *Daemon) processLoop(stop chan struct{}) {
	t := time.NewTimer(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-d.kick:
		case <-t.C:
		}
		wait := d.processQueue()
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(wait)
	}
}

var (
	errBlocked = errors.New("waiting for a manual match")
	errDrop    = errors.New("dropped")
)

// processQueue works through the queue once and returns when to run again.
func (d *Daemon) processQueue() time.Duration {
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	d.stateMu.Lock()
	stale := time.Since(d.listFetched) > listRefresh
	d.stateMu.Unlock()
	if stale {
		if err := d.refreshList(ctx); err != nil && netx.IsTemporary(err) {
			return d.fail(err)
		}
	}
	d.maybeRefreshVNList(ctx)

	d.st.Lock()
	ids := make([]int64, 0, len(d.st.S.Queue))
	for _, it := range d.st.S.Queue {
		ids = append(ids, it.ID)
	}
	d.st.Unlock()

	for _, id := range ids {
		it := d.copyItem(id)
		if it == nil {
			continue
		}
		var err error
		switch it.Kind {
		case "vn":
			err = d.processVN(ctx, it)
		case "set":
			err = d.processSet(ctx, it)
		case "vnset":
			err = d.processVNSet(ctx, it)
		default:
			err = d.processAnime(ctx, it)
		}
		switch {
		case err == nil:
			d.finishItem(it)
		case errors.Is(err, errBlocked):
			d.putItem(it)
		case errors.Is(err, errDrop):
			log.Printf("dropping %q ep %d: %v", it.Title, it.Episode, err)
			d.removeItem(it.ID)
		case netx.KindOf(err) == netx.Temporary:
			it.Attempts++
			it.LastErr = err.Error()
			d.putItem(it)
			return d.fail(err)
		default:
			log.Printf("item %d (%s): %v", it.ID, it.Title, err)
			it.LastErr = err.Error()
			if netx.KindOf(err) == netx.Auth {
				d.putItem(it) // retried after re-login
				continue
			}
			d.notif.Send("anisync: update failed", fmt.Sprintf("%s: %v", firstNonEmpty(it.TargetTitle, it.Title, it.Key), err), nil, nil)
			d.removeItem(it.ID)
		}
	}
	d.stateMu.Lock()
	d.backoff, d.lastErr, d.retryAt = 0, "", time.Time{}
	wait := listRefresh - time.Since(d.listFetched)
	d.stateMu.Unlock()
	if wait < time.Minute {
		wait = time.Minute
	}
	return wait
}

func (d *Daemon) fail(err error) time.Duration {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	switch {
	case d.backoff == 0:
		d.backoff = 30 * time.Second
	case d.backoff < 15*time.Minute:
		d.backoff *= 2
		if d.backoff > 15*time.Minute {
			d.backoff = 15 * time.Minute
		}
	}
	wait := d.backoff
	var ne *netx.Error
	if errors.As(err, &ne) && ne.RetryAfter > wait {
		wait = ne.RetryAfter
	}
	d.lastErr = err.Error()
	d.retryAt = time.Now().Add(wait)
	log.Printf("offline or service unavailable (%v); retrying in %s", err, wait)
	return wait
}

func (d *Daemon) copyItem(id int64) *store.Item {
	d.st.Lock()
	defer d.st.Unlock()
	for _, it := range d.st.S.Queue {
		if it.ID == id {
			cp := *it
			cp.Done = cloneMap(it.Done)
			cp.Result = cloneMap(it.Result)
			cp.Before = cloneMap(it.Before)
			return &cp
		}
	}
	return nil
}

func (d *Daemon) putItem(it *store.Item) {
	d.st.Lock()
	defer d.st.Unlock()
	for i, x := range d.st.S.Queue {
		if x.ID == it.ID {
			d.st.S.Queue[i] = it
			break
		}
	}
	_ = d.st.Save()
}

func (d *Daemon) removeItem(id int64) {
	d.st.Lock()
	defer d.st.Unlock()
	q := d.st.S.Queue[:0]
	for _, x := range d.st.S.Queue {
		if x.ID != id {
			q = append(q, x)
		}
	}
	d.st.S.Queue = q
	_ = d.st.Save()
}

func (d *Daemon) finishItem(it *store.Item) {
	h := &store.HistoryEntry{ID: it.ID, Time: time.Now(), Kind: it.Kind, Title: firstNonEmpty(it.TargetTitle, it.Title),
		AnilistID: it.TargetID, MalID: it.TargetMal, VNDBID: it.TargetVN, Episode: it.TargetEp, Action: it.Action,
		Result: it.Result, Before: it.Before, BeforeVN: it.BeforeVN}
	d.st.Lock()
	d.st.S.History = append(d.st.S.History, h)
	d.st.Unlock()
	d.removeItem(it.ID)
	switch it.Kind {
	case "anime", "set":
		d.cacheClearPending(it.TargetID, it.TargetMal)
	case "vn", "vnset":
		d.vnCacheClearPending(it.TargetVN)
	}

	changed := false
	var parts []string
	for _, svc := range []string{svcAniList, svcMAL, svcVNDB} {
		if r, ok := it.Result[svc]; ok {
			parts = append(parts, svcName(svc)+": "+r)
			if !strings.HasPrefix(r, "skipped") {
				changed = true
			}
		}
	}
	log.Printf("done: %s — %s", h.Title, strings.Join(parts, "; "))
	if changed && d.cfg.NotifyUpdates && it.Kind != "set" && it.Kind != "vnset" { // manual edits: you already know
		summary := h.Title
		if it.Kind == "anime" {
			if it.TargetTotal > 0 {
				summary += fmt.Sprintf(" — episode %d/%d", it.TargetEp, it.TargetTotal)
			} else {
				summary += fmt.Sprintf(" — episode %d", it.TargetEp)
			}
		}
		d.notif.Send(summary, strings.Join(parts, "\n"), nil, nil)
	}
}

func svcName(s string) string {
	return map[string]string{svcAniList: "AniList", svcMAL: "MAL", svcVNDB: "VNDB"}[s]
}

// ---------------------------------------------------------------- anime

func (d *Daemon) processAnime(ctx context.Context, it *store.Item) error {
	useAL, useMAL := d.enabled(svcAniList), d.enabled(svcMAL)
	if !useAL && !useMAL {
		return errBlocked // nothing logged in yet; keep it queued
	}
	if it.TargetID == 0 {
		m, err := d.resolveAnime(ctx, it)
		if err != nil {
			return err
		}
		info, ep, err := d.target(ctx, m, it.Episode)
		if err != nil {
			return err
		}
		it.TargetID, it.TargetMal, it.TargetEp, it.TargetTotal, it.TargetTitle = info.ID, info.MalID, ep, info.Episodes, info.Title
		it.Meta, _ = json.Marshal(listcache.Entry{Title: info.Title, Episodes: info.Episodes, Format: info.Format, AirStatus: info.Status})
		if it.Done == nil {
			it.Done, it.Result, it.Before = map[string]bool{}, map[string]string{}, map[string]store.EntrySnapshot{}
		}
		d.putItem(it)
		d.overlay(it) // show it in the local list right away, even if offline
	}
	today := netx.Today()
	if useAL && !it.Done[svcAniList] && !d.isAuthBroken(svcAniList) {
		if err := d.applyAniList(ctx, it, today); err != nil {
			if netx.KindOf(err) != netx.Auth {
				return err
			}
			d.markAuthBroken(svcAniList, err)
		}
	}
	if useMAL && !it.Done[svcMAL] && !d.isAuthBroken(svcMAL) {
		if err := d.applyMAL(ctx, it, today); err != nil {
			if netx.KindOf(err) != netx.Auth {
				return err
			}
			d.markAuthBroken(svcMAL, err)
		}
	}
	if (useAL && !it.Done[svcAniList]) || (useMAL && !it.Done[svcMAL]) {
		return &netx.Error{Kind: netx.Auth, Msg: "waiting for re-login"}
	}
	return nil
}

func (d *Daemon) applyAniList(ctx context.Context, it *store.Item, today string) error {
	m, err := d.al.Media(ctx, it.TargetID)
	if err != nil {
		return err
	}
	cur := m.MediaListEntry.Snapshot()
	next, skip := plan(cur, m.Episodes, it.TargetEp, d.cfg.AddToList, d.cfg.Rewatch, today)
	if skip != "" {
		it.Result[svcAniList] = "skipped (" + skip + ")"
	} else {
		if err := d.al.Save(ctx, it.TargetID, next); err != nil {
			return err
		}
		it.Before[svcAniList] = cur
		it.Result[svcAniList] = describe(cur, next)
		d.cacheSet(metaOf(it), next, false, true)
	}
	it.Done[svcAniList] = true
	d.putItem(it)
	return nil
}

func (d *Daemon) applyMAL(ctx context.Context, it *store.Item, today string) error {
	if it.TargetMal == 0 {
		it.Result[svcMAL] = "skipped (not on MAL)"
		it.Done[svcMAL] = true
		return nil
	}
	cur, total, err := d.mal.Entry(ctx, it.TargetMal)
	if err != nil {
		return err
	}
	if total == 0 {
		total = it.TargetTotal
	}
	next, skip := plan(cur, total, it.TargetEp, d.cfg.AddToList, d.cfg.Rewatch, today)
	if skip != "" {
		it.Result[svcMAL] = "skipped (" + skip + ")"
	} else {
		if err := d.mal.Save(ctx, it.TargetMal, next); err != nil {
			return err
		}
		it.Before[svcMAL] = cur
		it.Result[svcMAL] = describe(cur, next)
		if !d.enabled(svcAniList) {
			d.cacheSet(metaOf(it), next, false, true)
		}
	}
	it.Done[svcMAL] = true
	d.putItem(it)
	return nil
}

func describe(cur, next store.EntrySnapshot) string {
	s := fmt.Sprintf("progress %d → %d", cur.Progress, next.Progress)
	if !cur.Exists {
		s = fmt.Sprintf("added, progress %d", next.Progress)
	}
	if next.Status != cur.Status && cur.Exists {
		s += ", " + strings.ToLower(next.Status)
	} else if !cur.Exists && next.Status == "COMPLETED" {
		s += ", completed"
	}
	return s
}

func (d *Daemon) isAuthBroken(svc string) bool {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	return d.authBroken[svc]
}

func (d *Daemon) markAuthBroken(svc string, err error) {
	d.stateMu.Lock()
	already := d.authBroken[svc]
	d.authBroken[svc] = true
	d.stateMu.Unlock()
	if !already {
		log.Printf("%s: auth error: %v", svc, err)
		d.notif.Send(svcName(svc)+" login expired", "Updates are queued. Run: anisync auth "+svc, nil, nil)
	}
}

// mediaInfo returns cached AniList metadata (episodes, sequel, MAL id).
func (d *Daemon) mediaInfo(ctx context.Context, id int) (*store.MediaInfo, error) {
	d.st.Lock()
	mi := d.st.S.Media[id]
	d.st.Unlock()
	if mi != nil {
		ttl := 30 * 24 * time.Hour
		if mi.Status == "RELEASING" || mi.Status == "NOT_YET_RELEASED" || mi.Episodes == 0 {
			ttl = 12 * time.Hour
		}
		if time.Since(mi.Fetched) < ttl {
			return mi, nil
		}
	}
	m, err := (&anilist.Client{}).Media(ctx, id) // anonymous: cacheable, no list entry needed
	if err != nil {
		if mi != nil && netx.IsTemporary(err) {
			return mi, nil // stale is fine
		}
		return nil, err
	}
	mi = m.Info()
	mi.Fetched = time.Now()
	d.st.Lock()
	d.st.S.Media[id] = mi
	d.st.Unlock()
	return mi, nil
}

// target applies the mapping offset and walks sequels for absolute episode
// numbers ("Show - 30" where season 1 has 24 episodes → season 2 ep 6).
func (d *Daemon) target(ctx context.Context, m store.Mapping, episode int) (*store.MediaInfo, int, error) {
	info, err := d.mediaInfo(ctx, m.AnilistID)
	if err != nil {
		return nil, 0, err
	}
	if episode == 0 {
		if info.Episodes == 1 {
			return info, 1, nil
		}
		return nil, 0, fmt.Errorf("%w: no episode number in file name", errDrop)
	}
	ep := episode + m.Offset
	for hops := 0; info.Episodes > 0 && ep > info.Episodes && info.Sequel != 0 && hops < 10; hops++ {
		ep -= info.Episodes
		if info, err = d.mediaInfo(ctx, info.Sequel); err != nil {
			return nil, 0, err
		}
	}
	if ep <= 0 {
		return nil, 0, fmt.Errorf("%w: episode %d out of range", errDrop, ep)
	}
	if info.Episodes > 0 && ep > info.Episodes {
		return nil, 0, &netx.Error{Kind: netx.Permanent, Msg: fmt.Sprintf("episode %d is beyond %s's %d episodes; fix with: anisync forget %q", episode, info.Title, info.Episodes, m.Title)}
	}
	return info, ep, nil
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return nil
	}
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}
