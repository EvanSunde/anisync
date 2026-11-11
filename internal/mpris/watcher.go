// Package mpris watches MPRIS media players on the session bus and reports
// when a file has been actually watched past the threshold.
//
// It is fully event driven: it listens to NameOwnerChanged, PropertiesChanged
// and Seeked signals and never polls. Watched time is tracked as covered
// position spans, so seeking past a part doesn't count it, and coverage of a
// file persists across sessions (resume tomorrow, still counts).
package mpris

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/evansunde/anisync/internal/store"
)

const (
	busPrefix   = "org.mpris.MediaPlayer2."
	objPath     = dbus.ObjectPath("/org/mpris/MediaPlayer2")
	playerIface = "org.mpris.MediaPlayer2.Player"
	propsIface  = "org.freedesktop.DBus.Properties"
)

type Event struct {
	Player string
	URL    string
	Title  string
	Length time.Duration
}

type CoverageStore interface {
	GetCoverage(key string) *store.Coverage // returns a copy or nil
	PutCoverage(key string, c *store.Coverage)
}

type Options struct {
	Threshold float64
	Players   []string
	MinLength time.Duration
	// Accept filters tracks (library dirs, ignore list, streams).
	Accept    func(url, title string) bool
	Coverage  CoverageStore
	OnWatched func(Event)
}

type player struct {
	name   string // well-known bus name
	owner  string // unique name
	status string
	rate   float64

	key, url, title string
	length          float64 // seconds
	cov             *store.Coverage

	playing bool
	openPos float64
	openAt  time.Time
	lastPos float64 // last known position, for resuming without a report
}

func (p *player) pos(now time.Time) float64 {
	x := p.openPos + now.Sub(p.openAt).Seconds()*p.rate
	if p.length > 0 && x > p.length {
		x = p.length
	}
	if x < 0 {
		x = 0
	}
	return x
}

func (p *player) covered(now time.Time) float64 {
	if p.cov == nil {
		return 0
	}
	if p.playing {
		return p.cov.Total([2]float64{p.openPos, p.pos(now)})
	}
	return p.cov.Total()
}

type Watcher struct {
	opt  Options
	conn *dbus.Conn

	mu      sync.Mutex
	players map[string]*player // by owner
	timer   *time.Timer
	// queryPos asks the player for its current position in seconds (D-Bus
	// call); ok=false when the player doesn't answer or doesn't support it.
	queryPos    func(p *player) (pos float64, ok bool)
	queryLength func(p *player) float64
}

func New(conn *dbus.Conn, opt Options) *Watcher {
	w := &Watcher{opt: opt, conn: conn, players: map[string]*player{}}
	w.queryPos = w.dbusPosition
	w.queryLength = w.dbusLength
	return w
}

func (w *Watcher) wanted(name string) bool {
	if !strings.HasPrefix(name, busPrefix) {
		return false
	}
	n := strings.ToLower(strings.TrimPrefix(name, busPrefix))
	if strings.Contains(n, "playerctld") { // proxy for other players: would double count
		return false
	}
	for _, s := range w.opt.Players {
		if s != "" && strings.Contains(n, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// Start subscribes to signals and picks up already-running players.
// Signals must then be fed through HandleSignal.
func (w *Watcher) Start() error {
	matches := [][]dbus.MatchOption{
		{dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg0Namespace("org.mpris.MediaPlayer2")},
		{dbus.WithMatchObjectPath(objPath), dbus.WithMatchInterface(propsIface), dbus.WithMatchMember("PropertiesChanged")},
		{dbus.WithMatchObjectPath(objPath), dbus.WithMatchInterface(playerIface), dbus.WithMatchMember("Seeked")},
	}
	for _, m := range matches {
		if err := w.conn.AddMatchSignal(m...); err != nil {
			return err
		}
	}
	var names []string
	if err := w.conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
		return err
	}
	for _, n := range names {
		if !w.wanted(n) {
			continue
		}
		var owner string
		if err := w.conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, n).Store(&owner); err == nil {
			w.addPlayer(n, owner)
		}
	}
	return nil
}

func (w *Watcher) call(dest, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return w.conn.Object(dest, objPath).CallWithContext(ctx, method, 0, args...)
}

func (w *Watcher) dbusPosition(p *player) (float64, bool) {
	var v dbus.Variant
	if err := w.call(p.owner, propsIface+".Get", playerIface, "Position").Store(&v); err != nil {
		return 0, false
	}
	return float64(toInt64(v.Value())) / 1e6, true
}

// dbusLength re-reads mpris:length for players that publish it late.
func (w *Watcher) dbusLength(p *player) float64 {
	var v dbus.Variant
	if err := w.call(p.owner, propsIface+".Get", playerIface, "Metadata").Store(&v); err != nil {
		return 0
	}
	md, _ := v.Value().(map[string]dbus.Variant)
	return float64(toInt64(variant(md, "mpris:length"))) / 1e6
}

func (w *Watcher) addPlayer(name, owner string) {
	var props map[string]dbus.Variant
	if err := w.call(owner, propsIface+".GetAll", playerIface).Store(&props); err != nil {
		log.Printf("mpris: %s: %v", name, err)
		props = map[string]dbus.Variant{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	p := &player{name: name, owner: owner, rate: 1, status: "Stopped"}
	w.players[owner] = p
	log.Printf("mpris: player appeared: %s", name)
	w.applyProps(p, props, time.Now())
	w.schedule(time.Now())
}

// HandleSignal processes one D-Bus signal (from the shared connection).
func (w *Watcher) HandleSignal(s *dbus.Signal) {
	now := time.Now()
	switch s.Name {
	case "org.freedesktop.DBus.NameOwnerChanged":
		if len(s.Body) < 3 {
			return
		}
		name, _ := s.Body[0].(string)
		oldOwner, _ := s.Body[1].(string)
		newOwner, _ := s.Body[2].(string)
		if !w.wanted(name) {
			return
		}
		if oldOwner != "" {
			w.removePlayer(oldOwner, now)
		}
		if newOwner != "" {
			go w.addPlayer(name, newOwner) // D-Bus calls: don't block the signal loop
		}
	case propsIface + ".PropertiesChanged":
		if s.Path != objPath || len(s.Body) < 2 {
			return
		}
		if iface, _ := s.Body[0].(string); iface != playerIface {
			return
		}
		changed, _ := s.Body[1].(map[string]dbus.Variant)
		w.mu.Lock()
		if p := w.players[s.Sender]; p != nil {
			w.applyProps(p, changed, now)
			w.schedule(now)
		}
		w.mu.Unlock()
	case playerIface + ".Seeked":
		if s.Path != objPath || len(s.Body) < 1 {
			return
		}
		w.mu.Lock()
		if p := w.players[s.Sender]; p != nil {
			w.onSeek(p, float64(toInt64(s.Body[0]))/1e6, now)
			w.schedule(now)
		}
		w.mu.Unlock()
	}
}

func (w *Watcher) removePlayer(owner string, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p := w.players[owner]; p != nil {
		w.closeSpan(p, now)
		w.check(p, now)
		w.persist(p)
		delete(w.players, owner)
		log.Printf("mpris: player gone: %s", p.name)
	}
	w.schedule(now)
}

// applyProps handles a (partial) property map. Lock held.
func (w *Watcher) applyProps(p *player, props map[string]dbus.Variant, now time.Time) {
	if v, ok := props["Metadata"]; ok {
		md, _ := v.Value().(map[string]dbus.Variant)
		w.onMetadata(p, md, now)
	}
	if v, ok := props["Rate"]; ok {
		if r, ok := v.Value().(float64); ok && r > 0 {
			if p.playing {
				w.reconcile(p, now)
				p.rate = r
				w.openSpan(p, p.lastPos, now)
			} else {
				p.rate = r
			}
		}
	}
	if v, ok := props["PlaybackStatus"]; ok {
		st, _ := v.Value().(string)
		w.onStatus(p, st, now)
	}
}

func (w *Watcher) onMetadata(p *player, md map[string]dbus.Variant, now time.Time) {
	url, _ := variant(md, "xesam:url").(string)
	title, _ := variant(md, "xesam:title").(string)
	length := float64(toInt64(variant(md, "mpris:length"))) / 1e6
	key := url
	if key == "" && title != "" {
		key = "title:" + title
	}
	if key == p.key {
		if length > 0 && p.length == 0 {
			p.length = length
			if p.cov != nil {
				p.cov.Length = length
			}
		}
		if title != "" {
			p.title = title
		}
		return
	}
	// track changed: finish the old one
	wasPlaying := p.playing
	w.closeSpan(p, now)
	w.check(p, now)
	w.persist(p)

	p.key, p.url, p.title, p.length, p.cov = key, url, title, length, nil
	if key == "" {
		return
	}
	if c := w.opt.Coverage.GetCoverage(key); c != nil {
		p.cov = c
	} else {
		p.cov = &store.Coverage{}
	}
	p.cov.Length = length
	if wasPlaying || p.status == "Playing" {
		pos, _ := w.queryPos(p) // new file: unknown position means start
		w.openSpan(p, pos, now)
	}
}

func (w *Watcher) onStatus(p *player, st string, now time.Time) {
	if st == "" || st == p.status {
		return
	}
	p.status = st
	if st == "Playing" {
		if !p.playing && p.key != "" {
			pos, ok := w.queryPos(p)
			if !ok {
				pos = p.lastPos // resume where we last knew it was
			}
			w.openSpan(p, pos, now)
		}
		return
	}
	if st == "Paused" && p.playing {
		w.reconcile(p, now) // paused position is reliable; stopped players often report 0
	}
	w.closeSpan(p, now)
	w.check(p, now)
	w.persist(p)
}

func (w *Watcher) onSeek(p *player, pos float64, now time.Time) {
	if !p.playing {
		return
	}
	w.closeSpan(p, now)
	w.check(p, now)
	w.openSpan(p, pos, now)
}

func (w *Watcher) openSpan(p *player, pos float64, now time.Time) {
	if p.length > 0 && pos > p.length {
		pos = 0
	}
	p.playing, p.openPos, p.openAt = true, pos, now
}

// closeSpan ends the open span at the extrapolated position. Used when the
// player can't be asked (track already changed, seek already happened);
// the extrapolation is at most ReconcileEvery old.
func (w *Watcher) closeSpan(p *player, now time.Time) {
	if !p.playing {
		return
	}
	end := p.pos(now)
	if p.cov != nil {
		p.cov.Add(p.openPos, end)
	}
	p.lastPos = end
	p.playing = false
}

// posSlack is the tolerance when comparing the player's reported position
// with the extrapolated one.
const posSlack = 3.0

// reconcile asks the player where it really is and anchors the open span to
// that: stalls and buffering count only as far as the player actually got.
// A jump the player didn't announce (no Seeked signal) credits nothing for
// the uncertain interval. Falls back to the clock when the player doesn't
// report a position. The span stays open.
func (w *Watcher) reconcile(p *player, now time.Time) {
	if !p.playing {
		return
	}
	if p.length <= 0 && w.queryLength != nil {
		if l := w.queryLength(p); l > 0 {
			p.length = l
			if p.cov != nil {
				p.cov.Length = l
			}
		}
	}
	est := p.pos(now)
	end, next := est, est
	if rep, ok := w.queryPos(p); ok {
		next = rep
		if rep >= p.openPos-posSlack && rep <= est+posSlack {
			end = max(rep, p.openPos) // normal case: trust the player
		} else {
			end = p.openPos // unannounced jump: don't guess
		}
	}
	if p.cov != nil {
		p.cov.Add(p.openPos, end)
	}
	p.lastPos = next
	w.openSpan(p, next, now)
}

func (w *Watcher) persist(p *player) {
	if p.cov != nil && p.key != "" && len(p.cov.Spans) > 0 {
		p.cov.Updated = time.Now()
		w.opt.Coverage.PutCoverage(p.key, p.cov)
	}
}

func (w *Watcher) eligible(p *player) bool {
	if p.key == "" || p.length <= 0 || p.length < w.opt.MinLength.Seconds() {
		return false
	}
	return w.opt.Accept == nil || w.opt.Accept(p.url, p.title)
}

// check fires the watched event once the threshold is covered.
func (w *Watcher) check(p *player, now time.Time) {
	if p.cov == nil || p.cov.Counted || !w.eligible(p) {
		return
	}
	if p.covered(now) >= w.opt.Threshold*p.length {
		p.cov.Counted = true
		// fold the open span in so the persisted coverage is accurate
		if p.playing {
			pos := p.pos(now)
			p.cov.Add(p.openPos, pos)
			w.openSpan(p, pos, now)
		}
		w.persist(p)
		ev := Event{Player: p.name, URL: p.url, Title: p.title, Length: time.Duration(p.length * float64(time.Second))}
		log.Printf("mpris: watched %s (%.0f%%)", firstNonEmpty(p.url, p.title), 100*p.covered(now)/p.length)
		if w.opt.OnWatched != nil {
			go w.opt.OnWatched(ev)
		}
	}
}

// ReconcileEvery is how often a playing, not yet counted file's position is
// re-read from the player: one D-Bus property read. Nothing else polls.
var ReconcileEvery = 30 * time.Second

// schedule arms a single timer for the earliest moment any playing player
// could reach the threshold, or its next position check. Lock held.
func (w *Watcher) schedule(now time.Time) {
	var next time.Duration = -1
	for _, p := range w.players {
		if !p.playing || p.cov == nil || p.cov.Counted {
			continue
		}
		d := ReconcileEvery
		if w.eligible(p) {
			need := w.opt.Threshold*p.length - p.covered(now)
			if t := time.Duration(need/p.rate*float64(time.Second)) + 250*time.Millisecond; t < d {
				d = t
			}
			if need <= 0 {
				d = 0
			}
		} else if p.length > 0 {
			continue // filtered (too short / not a video): nothing to track
		}
		if next < 0 || d < next {
			next = d
		}
	}
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	if next >= 0 {
		w.timer = time.AfterFunc(next, w.tick)
	}
}

func (w *Watcher) tick() {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	for _, p := range w.players {
		if p.playing && p.cov != nil && !p.cov.Counted {
			w.reconcile(p, now) // decide on the player's own position
			w.check(p, now)
		}
	}
	w.schedule(now)
}

// Flush persists coverage of all players (on shutdown).
func (w *Watcher) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	for _, p := range w.players {
		if p.playing {
			w.reconcile(p, now)
		}
		w.persist(p)
	}
}

type Status struct {
	Player  string  `json:"player"`
	Bus     string  `json:"bus"`
	URL     string  `json:"url,omitempty"`
	Media   string  `json:"media_title,omitempty"`
	Title   string  `json:"title"`
	State   string  `json:"state"`
	Watched float64 `json:"watched"` // fraction
	Counted bool    `json:"counted"`
	Tracked bool    `json:"tracked"`
}

func (w *Watcher) Snapshot() []Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	var out []Status
	for _, p := range w.players {
		s := Status{Player: strings.TrimPrefix(p.name, busPrefix), Bus: p.name, URL: p.url, Media: p.title,
			Title: firstNonEmpty(p.url, p.title), State: p.status, Tracked: w.eligible(p)}
		if p.length > 0 {
			s.Watched = p.covered(now) / p.length
		}
		if p.cov != nil {
			s.Counted = p.cov.Counted
		}
		out = append(out, s)
	}
	return out
}

// MarkWatched counts the file a player has open right now, regardless of
// how much was watched (manual "I watched this"). bus is the player's bus
// name or a substring of it ("" = the only/first playing player).
func (w *Watcher) MarkWatched(bus string) (Event, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var pick *player
	for _, p := range w.players {
		if p.key == "" {
			continue
		}
		if bus != "" && p.name != bus && !strings.Contains(p.name, bus) {
			continue
		}
		if pick == nil || (p.status == "Playing" && pick.status != "Playing") {
			pick = p
		}
	}
	if pick == nil {
		return Event{}, errors.New("nothing is playing")
	}
	now := time.Now()
	if pick.playing {
		w.reconcile(pick, now)
	}
	if pick.cov == nil {
		pick.cov = &store.Coverage{}
	}
	pick.cov.Counted = true
	w.persist(pick)
	ev := Event{Player: pick.name, URL: pick.url, Title: pick.title, Length: time.Duration(pick.length * float64(time.Second))}
	log.Printf("mpris: marked watched manually: %s", firstNonEmpty(pick.url, pick.title))
	w.schedule(now)
	return ev, nil
}

func variant(m map[string]dbus.Variant, k string) any {
	if v, ok := m[k]; ok {
		return v.Value()
	}
	return nil
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case uint64:
		return int64(x)
	case int32:
		return int64(x)
	case uint32:
		return int64(x)
	case float64:
		return int64(x)
	case dbus.Variant:
		return toInt64(x.Value())
	}
	return 0
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}
