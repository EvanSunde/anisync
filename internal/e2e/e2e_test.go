//go:build e2e

// End-to-end test: real D-Bus session bus, a fake MPRIS player, the real
// daemon, and mock AniList/MAL servers. Run with:
//
//	dbus-run-session -- go test -tags e2e ./internal/e2e/ -v
package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/config"
	"github.com/evansunde/anisync/internal/daemon"
	"github.com/evansunde/anisync/internal/ipc"
	"github.com/evansunde/anisync/internal/listcache"
	"github.com/evansunde/anisync/internal/mal"
	"github.com/evansunde/anisync/internal/store"
	"github.com/evansunde/anisync/internal/vndb"
	"github.com/evansunde/anisync/internal/vnlist"
)

func vndbAPI(u string) { vndb.API = u }

type entry struct {
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Score    int    `json:"score"`
}

type media struct {
	id, mal, eps int
	title        string
	sequel       int
}

var catalog = map[int]media{
	154587: {154587, 52991, 28, "Sousou no Frieren", 182255},
	182255: {182255, 59978, 10, "Sousou no Frieren 2nd Season", 0},
	1001:   {1001, 2001, 12, "Mystery Show Alpha", 0},
	1002:   {1002, 2002, 12, "Mystery Show Beta", 0},
}

type mock struct {
	mu   sync.Mutex
	down atomic.Bool
	al   map[int]*entry // anilist id -> entry
	mal  map[int]*entry // mal id -> entry
}

func (m *mock) mediaJSON(md media, withEntry bool) map[string]any {
	out := map[string]any{"id": md.id, "idMal": md.mal, "title": map[string]any{"romaji": md.title},
		"synonyms": []string{}, "format": "TV", "status": "FINISHED", "episodes": md.eps, "seasonYear": 2023}
	edges := []any{}
	if md.sequel != 0 {
		edges = append(edges, map[string]any{"relationType": "SEQUEL", "node": map[string]any{"id": md.sequel, "type": "ANIME", "format": "TV"}})
	}
	out["relations"] = map[string]any{"edges": edges}
	if withEntry {
		if e := m.al[md.id]; e != nil {
			out["mediaListEntry"] = map[string]any{"id": md.id * 10, "status": e.Status, "progress": e.Progress, "repeat": 0, "score": e.Score,
				"startedAt": map[string]any{}, "completedAt": map[string]any{}}
		}
	}
	return out
}

func (m *mock) anilist(w http.ResponseWriter, r *http.Request) {
	if m.down.Load() {
		http.Error(w, "down", 503)
		return
	}
	var req struct {
		Query string         `json:"query"`
		Vars  map[string]any `json:"variables"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	m.mu.Lock()
	defer m.mu.Unlock()
	q := req.Query
	var data any
	switch {
	case strings.Contains(q, "MediaListCollection"):
		var entries []any
		for id, e := range m.al {
			entries = append(entries, map[string]any{"status": e.Status, "progress": e.Progress, "media": m.mediaJSON(catalog[id], false)})
		}
		data = map[string]any{"MediaListCollection": map[string]any{"lists": []any{map[string]any{"entries": entries}}}}
	case strings.Contains(q, "Page("):
		s := strings.ToLower(req.Vars["s"].(string))
		var res []any
		for _, id := range []int{154587, 182255, 1001, 1002} {
			md := catalog[id]
			if strings.Contains(strings.ToLower(md.title), strings.Fields(s)[0]) {
				res = append(res, m.mediaJSON(md, false))
			}
		}
		data = map[string]any{"Page": map[string]any{"media": res}}
	case strings.Contains(q, "SaveMediaListEntry"):
		id := int(req.Vars["m"].(float64))
		sc, _ := req.Vars["sc"].(float64)
		m.al[id] = &entry{Status: req.Vars["s"].(string), Progress: int(req.Vars["p"].(float64)), Score: int(sc)}
		data = map[string]any{"SaveMediaListEntry": map[string]any{"id": id * 10}}
	case strings.Contains(q, "DeleteMediaListEntry"):
		delete(m.al, int(req.Vars["id"].(float64))/10)
		data = map[string]any{"DeleteMediaListEntry": map[string]any{"deleted": true}}
	case strings.Contains(q, "Media("):
		md, ok := catalog[int(req.Vars["id"].(float64))]
		if !ok {
			data = map[string]any{"Media": nil}
		} else {
			data = map[string]any{"Media": m.mediaJSON(md, strings.Contains(q, "mediaListEntry"))}
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (m *mock) malAPI(w http.ResponseWriter, r *http.Request) {
	if m.down.Load() {
		http.Error(w, "down", 503)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	toMal := map[string]string{"CURRENT": "watching", "COMPLETED": "completed", "PLANNING": "plan_to_watch"}
	fromMal := map[string]string{"watching": "CURRENT", "completed": "COMPLETED", "plan_to_watch": "PLANNING"}
	switch {
	case parts[0] == "users":
		var data []any
		for id, e := range m.mal {
			data = append(data, map[string]any{"node": map[string]any{"id": id}, "list_status": map[string]any{"status": toMal[e.Status], "num_episodes_watched": e.Progress}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data, "paging": map[string]any{}})
	case parts[0] == "anime" && len(parts) == 2:
		id, _ := strconv.Atoi(parts[1])
		eps := 0
		for _, md := range catalog {
			if md.mal == id {
				eps = md.eps
			}
		}
		out := map[string]any{"num_episodes": eps}
		if e := m.mal[id]; e != nil {
			out["my_list_status"] = map[string]any{"status": toMal[e.Status], "num_episodes_watched": e.Progress, "score": e.Score / 10}
		}
		json.NewEncoder(w).Encode(out)
	case parts[0] == "anime" && len(parts) == 3:
		id, _ := strconv.Atoi(parts[1])
		if r.Method == http.MethodDelete {
			delete(m.mal, id)
			return
		}
		r.ParseForm()
		p, _ := strconv.Atoi(r.Form.Get("num_watched_episodes"))
		sc, _ := strconv.Atoi(r.Form.Get("score"))
		m.mal[id] = &entry{Status: fromMal[r.Form.Get("status")], Progress: p, Score: sc * 10}
		fmt.Fprint(w, "{}")
	}
}

func (m *mock) get(svc string, id int) (entry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.al
	if svc == "mal" {
		src = m.mal
	}
	e, ok := src[id]
	if !ok {
		return entry{}, false
	}
	return *e, true
}

// ---------------------------------------------------------------- fake player

type player struct {
	conn  *dbus.Conn
	props *prop.Properties

	mu      sync.Mutex
	playing bool
	base    float64 // position at start
	since   time.Time
}

func newPlayer(t *testing.T) *player {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	p := &player{conn: conn}
	p.props, err = prop.Export(conn, "/org/mpris/MediaPlayer2", prop.Map{
		"org.mpris.MediaPlayer2.Player": {
			"PlaybackStatus": {Value: "Stopped", Emit: prop.EmitTrue},
			"Rate":           {Value: 1.0, Emit: prop.EmitTrue},
			"Metadata":       {Value: map[string]dbus.Variant{}, Emit: prop.EmitTrue},
			"Position":       {Value: int64(0), Emit: prop.EmitFalse},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RequestName("org.mpris.MediaPlayer2.mpv.instance42", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	// keep Position moving like a real player (it is read, never signalled)
	go func() {
		for range time.Tick(50 * time.Millisecond) {
			p.props.SetMust("org.mpris.MediaPlayer2.Player", "Position", int64(p.position()*1e6))
		}
	}()
	return p
}

func (p *player) position() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.playing {
		return p.base
	}
	return p.base + time.Since(p.since).Seconds()
}

func (p *player) setPos(sec float64, playing bool) {
	p.mu.Lock()
	p.base, p.since, p.playing = sec, time.Now(), playing
	p.mu.Unlock()
	p.props.SetMust("org.mpris.MediaPlayer2.Player", "Position", int64(sec*1e6))
}

func (p *player) play(path string, lengthSec float64) {
	p.setPos(0, true)
	p.props.SetMust("org.mpris.MediaPlayer2.Player", "Metadata", map[string]dbus.Variant{
		"xesam:url":    dbus.MakeVariant("file://" + path),
		"xesam:title":  dbus.MakeVariant(filepath.Base(path)),
		"mpris:length": dbus.MakeVariant(int64(lengthSec * 1e6)),
	})
	p.props.SetMust("org.mpris.MediaPlayer2.Player", "PlaybackStatus", "Playing")
}

func (p *player) seek(sec float64) {
	p.setPos(sec, true)
	p.conn.Emit("/org/mpris/MediaPlayer2", "org.mpris.MediaPlayer2.Player.Seeked", int64(sec*1e6))
}

func (p *player) stop() {
	p.setPos(p.position(), false)
	p.props.SetMust("org.mpris.MediaPlayer2.Player", "PlaybackStatus", "Stopped")
}

// ---------------------------------------------------------------- test

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestEndToEnd(t *testing.T) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Skip("needs a session bus: dbus-run-session -- go test -tags e2e ./internal/e2e/")
	}
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_RUNTIME_DIR", tmp)

	m := &mock{al: map[int]*entry{154587: {Status: "CURRENT", Progress: 4}}, mal: map[int]*entry{52991: {Status: "CURRENT", Progress: 4}}}
	alSrv := httptest.NewServer(http.HandlerFunc(m.anilist))
	malSrv := httptest.NewServer(http.HandlerFunc(m.malAPI))
	defer alSrv.Close()
	defer malSrv.Close()
	anilist.Endpoint = alSrv.URL
	mal.API = malSrv.URL

	var tok store.Tokens
	tok.AniList.Token, tok.AniList.UserID, tok.AniList.User = "tok", 1, "tester"
	tok.AniList.Expires = time.Now().AddDate(1, 0, 0)
	tok.MAL.Access, tok.MAL.Refresh, tok.MAL.User = "a", "r", "tester"
	tok.MAL.Expires = time.Now().AddDate(0, 1, 0)
	if err := store.SaveTokens(config.ConfigDir(), tok); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Players = []string{"mpv"}
	cfg.MinLength = config.Duration{Duration: 4 * time.Second}
	cfg.MAL.ClientID = "cid"
	cfg.Notify = false
	go func() {
		if err := daemon.Run(cfg); err != nil {
			t.Errorf("daemon: %v", err)
		}
	}()
	sock := config.SocketPath()
	waitFor(t, "daemon socket", 5*time.Second, func() bool { return ipc.Call(sock, "status", nil, nil) == nil })

	p := newPlayer(t)
	dir := filepath.Join(tmp, "Anime")

	// 1. normal episode → both services updated
	t.Log("1: watch Frieren 05")
	p.play(filepath.Join(dir, "[SubsPlease] Sousou no Frieren - 05 (1080p) [ABCD1234].mkv"), 6)
	waitFor(t, "anilist progress 5", 10*time.Second, func() bool { e, _ := m.get("al", 154587); return e.Progress == 5 })
	waitFor(t, "mal progress 5", 5*time.Second, func() bool { e, _ := m.get("mal", 52991); return e.Progress == 5 })

	// 2. seeking to the end does not count
	t.Log("2: open Frieren 06 and seek to the end")
	p.play(filepath.Join(dir, "[SubsPlease] Sousou no Frieren - 06 (1080p).mkv"), 10)
	time.Sleep(300 * time.Millisecond)
	p.seek(9.5)
	time.Sleep(2 * time.Second)
	p.stop()
	time.Sleep(time.Second)
	if e, _ := m.get("al", 154587); e.Progress != 5 {
		t.Fatalf("seek counted as watched: progress %d", e.Progress)
	}

	// 3. absolute numbering: ep 30 of S1 (28 eps) = S2 ep 2, not in list → added
	t.Log("3: absolute episode 30 → 2nd season ep 2")
	p.play(filepath.Join(dir, "Sousou no Frieren - 30.mkv"), 5)
	waitFor(t, "season 2 ep 2 added", 10*time.Second, func() bool { e, ok := m.get("al", 182255); return ok && e.Progress == 2 })
	waitFor(t, "mal season 2 added", 5*time.Second, func() bool { e, ok := m.get("mal", 59978); return ok && e.Progress == 2 })

	// 4. offline: the watch is queued, then flushed when back online
	t.Log("4: offline, ambiguous title")
	m.down.Store(true)
	p.play(filepath.Join(dir, "Mystery Show - 01.mkv"), 5)
	var st daemon.Status
	waitFor(t, "queued while offline", 10*time.Second, func() bool {
		ipc.Call(sock, "status", nil, &st)
		return len(st.Queue) == 1 && st.LastErr != ""
	})
	t.Logf("   status while offline: queue=%d err=%q", len(st.Queue), st.LastErr)
	m.down.Store(false)
	if err := ipc.Call(sock, "sync", nil, nil); err != nil {
		t.Fatal(err)
	}
	// "Mystery Show" matches nothing exactly → needs the user
	var pend []*store.Pending
	waitFor(t, "pending match", 10*time.Second, func() bool {
		ipc.Call(sock, "pending", nil, &pend)
		return len(pend) == 1
	})
	t.Logf("   pending: %q with %d candidates", pend[0].Query, len(pend[0].Candidates))
	c := pend[0].Candidates[0]
	for _, x := range pend[0].Candidates {
		if x.AnilistID == 1002 {
			c = x
		}
	}
	if err := ipc.Call(sock, "resolve", daemon.ResolveArgs{Key: pend[0].Key, AnilistID: c.AnilistID, MalID: c.MalID, Title: c.Title}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resolved show added", 10*time.Second, func() bool { e, ok := m.get("al", 1002); return ok && e.Progress == 1 && e.Status == "CURRENT" })
	waitFor(t, "mal added", 5*time.Second, func() bool { _, ok := m.get("mal", 2002); return ok })

	// mapping is remembered: episode 2 resolves without asking
	t.Log("5: next episode uses the remembered mapping")
	p.play(filepath.Join(dir, "Mystery Show - 02.mkv"), 5)
	waitFor(t, "ep 2 via mapping", 10*time.Second, func() bool { e, _ := m.get("al", 1002); return e.Progress == 2 })

	// 6. undo reverts the last update on both services
	t.Log("6: undo")
	var h store.HistoryEntry
	if err := ipc.Call(sock, "undo", int64(0), &h); err != nil {
		t.Fatal(err)
	}
	if e, _ := m.get("al", 1002); e.Progress != 1 {
		t.Fatalf("undo anilist: progress %d", e.Progress)
	}
	if e, _ := m.get("mal", 2002); e.Progress != 1 {
		t.Fatalf("undo mal: progress %d", e.Progress)
	}

	// 7. manual edits (what the TUI sends): add, score, progress→complete, remove, undo
	t.Log("7: TUI edits through the daemon")
	set := func(a daemon.SetArgs) {
		t.Helper()
		var id int64
		if err := ipc.Call(sock, "set", a, &id); err != nil {
			t.Fatal(err)
		}
	}
	// MAL only has whole-number scores: 8.5 is stored as 9 there.
	malScore := func(e entry) entry {
		if e.Score == 90 {
			e.Score = 85
		}
		return e
	}
	both := func(what string, f func(e entry, ok bool) bool) {
		t.Helper()
		waitFor(t, what, 10*time.Second, func() bool {
			a, aok := m.get("al", 1001)
			b, bok := m.get("mal", 2001)
			return f(a, aok) && f(malScore(b), bok)
		})
	}
	base := daemon.SetArgs{AnilistID: 1001, MalID: 2001, Title: "Mystery Show Alpha", Episodes: 12}
	a := base
	a.Status = "PLANNING"
	set(a)
	both("added as planning", func(e entry, ok bool) bool { return ok && e.Status == "PLANNING" })
	a = base
	sc := 85
	a.Score = &sc
	set(a)
	both("score 8.5", func(e entry, ok bool) bool { return e.Score == 85 && e.Status == "PLANNING" })
	a = base
	pr := 12
	a.Progress = &pr
	set(a)
	both("completed with score kept", func(e entry, ok bool) bool { return e.Status == "COMPLETED" && e.Progress == 12 && e.Score == 85 })
	a = base
	a.Delete = true
	set(a)
	both("removed", func(e entry, ok bool) bool { return !ok })
	if err := ipc.Call(sock, "undo", int64(0), nil); err != nil {
		t.Fatal(err)
	}
	both("restored by undo", func(e entry, ok bool) bool { return ok && e.Status == "COMPLETED" && e.Score == 85 })

	// 8. local list: the daemon keeps a copy on disk; offline edits show up
	// in it immediately (pending) and are confirmed once synced
	t.Log("8: local list copy + offline edit")
	var rep daemon.ListReply
	if err := ipc.Call(sock, "list-refresh", nil, &rep); err != nil || rep.Error != "" {
		t.Fatalf("list-refresh: %v %s", err, rep.Error)
	}
	if e := rep.List.Find(154587, 0); e == nil || e.Progress != 5 || e.Title != "Sousou no Frieren" {
		t.Fatalf("cached list entry: %+v", e)
	}
	if l, err := listcache.Load(); err != nil || l.Find(154587, 0) == nil {
		t.Fatalf("list not on disk: %v", err)
	}
	m.down.Store(true)
	p6 := 6
	set(daemon.SetArgs{AnilistID: 154587, MalID: 52991, Title: "Sousou no Frieren", Episodes: 28, Progress: &p6})
	ipc.Call(sock, "list", nil, &rep)
	if e := rep.List.Find(154587, 0); e == nil || e.Progress != 6 || !e.Pending {
		t.Fatalf("offline edit not in local list: %+v", e)
	}
	if l, _ := listcache.Load(); l.Find(154587, 0).Progress != 6 {
		t.Fatal("offline edit not saved to disk")
	}
	m.down.Store(false)
	ipc.Call(sock, "sync", nil, nil)
	var last string
	defer func() {
		if t.Failed() {
			t.Logf("last list state: %s", last)
		}
	}()
	waitFor(t, "edit synced and confirmed", 10*time.Second, func() bool {
		var rep daemon.ListReply // fresh: decoding into a reused value keeps stale omitempty fields
		ipc.Call(sock, "list", nil, &rep)
		e := rep.List.Find(154587, 0)
		a, _ := m.get("al", 154587)
		last = fmt.Sprintf("entry=%+v server=%+v", e, a)
		return e != nil && !e.Pending && a.Progress == 6
	})

	// 9. "mark watched now" counts the open file regardless of time watched
	t.Log("9: mark watched now")
	p.play(filepath.Join(dir, "[SubsPlease] Sousou no Frieren - 07 (1080p).mkv"), 60)
	time.Sleep(500 * time.Millisecond)
	var st2 daemon.Status
	ipc.Call(sock, "status", nil, &st2)
	if len(st2.Now) != 1 || st2.Now[0].Parsed != "Sousou no Frieren" || st2.Now[0].Episode != 7 || st2.Now[0].MatchID != 154587 {
		t.Fatalf("now playing: %+v", st2.Now)
	}
	var src string
	if err := ipc.Call(sock, "watched-now", "", &src); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ep 7 via watched-now", 10*time.Second, func() bool { e, _ := m.get("al", 154587); return e.Progress == 7 })
	p.stop()

	var hist []store.HistoryEntry
	ipc.Call(sock, "history", 10, &hist)
	for _, e := range hist {
		t.Logf("   history: %s ep %d %v undone=%v", e.Title, e.Episode, e.Result, e.Undone)
	}
	ipc.Call(sock, "status", nil, &st)
	if len(st.Queue) != 0 {
		t.Fatalf("queue not empty: %+v", st.Queue[0])
	}
}

// vndbMock is a small stateful VNDB: search, details, the user's list with
// labels, PATCH and DELETE.
type vndbMock struct {
	mu      sync.Mutex
	down    atomic.Bool
	entries map[string]map[string]any // vid -> ulist entry fields
	patches map[string][]map[string]any
}

var vnCatalog = map[string]map[string]any{
	"v20424": {"id": "v20424", "title": "Summer Pockets", "alttitle": "サマーポケッツ", "released": "2018-06-29", "rating": 84.6, "votecount": 4000,
		"length": 5, "length_minutes": 3245, "image": map[string]any{"url": "https://t.vndb.org/cv/08/75908.jpg", "sexual": 0, "violence": 0},
		"developers": []any{map[string]any{"id": "p24", "name": "Key"}}, "titles": []any{}},
	"v50957": {"id": "v50957", "title": "Summer Pockets: Xiari", "titles": []any{}},
	"v17": {"id": "v17", "title": "Ever17", "released": "2002-08-29", "rating": 87, "votecount": 9000, "length": 4, "titles": []any{},
		"image": map[string]any{"url": "https://t.vndb.org/cv/1.jpg", "sexual": 0, "violence": 0}},
}

var vnLabels = map[int]string{1: "Playing", 2: "Finished", 3: "Stalled", 4: "Dropped", 5: "Wishlist", 6: "Blacklist", 7: "Voted", 10: "Favourites"}

func (v *vndbMock) labelsOf(e map[string]any, withNames bool) []any {
	ids, _ := e["labels"].([]int)
	out := []any{}
	for _, id := range ids {
		l := map[string]any{"id": id}
		if withNames {
			l["label"] = vnLabels[id]
		}
		out = append(out, l)
	}
	return out
}

func (v *vndbMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if v.down.Load() {
		http.Error(w, "down", 503)
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var body map[string]any
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&body)
	}
	filterID := func() string {
		if f, ok := body["filters"].([]any); ok && len(f) == 3 && f[0] == "id" {
			return f[2].(string)
		}
		return ""
	}
	switch {
	case r.URL.Path == "/vn":
		if id := filterID(); id != "" {
			json.NewEncoder(w).Encode(map[string]any{"results": []any{vnCatalog[id]}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []any{vnCatalog["v20424"], vnCatalog["v50957"]}, "more": false, "count": 2})
	case r.URL.Path == "/ulist_labels":
		var ls []any
		for id, name := range vnLabels {
			n := 0
			for _, e := range v.entries {
				for _, l := range e["labels"].([]int) {
					if l == id {
						n++
					}
				}
			}
			ls = append(ls, map[string]any{"id": id, "label": name, "private": false, "count": n})
		}
		json.NewEncoder(w).Encode(map[string]any{"labels": ls})
	case r.URL.Path == "/ulist" && r.Method == http.MethodPost:
		var res []any
		if id := filterID(); id != "" {
			if e := v.entries[id]; e != nil {
				res = append(res, map[string]any{"id": id, "labels": v.labelsOf(e, false), "vote": e["vote"], "started": e["started"],
					"finished": e["finished"], "notes": e["notes"]})
			}
		} else {
			for id, e := range v.entries {
				res = append(res, map[string]any{"id": id, "labels": v.labelsOf(e, true), "vote": e["vote"], "started": e["started"],
					"finished": e["finished"], "notes": e["notes"], "lastmod": 1, "added": 1, "vn": vnCatalog[id]})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": res, "more": false})
	case strings.HasPrefix(r.URL.Path, "/ulist/"):
		id := strings.TrimPrefix(r.URL.Path, "/ulist/")
		if r.Method == http.MethodDelete {
			delete(v.entries, id)
			w.WriteHeader(204)
			return
		}
		v.patches[id] = append(v.patches[id], body)
		e := v.entries[id]
		if e == nil {
			e = map[string]any{"labels": []int{}}
			v.entries[id] = e
		}
		labels := e["labels"].([]int)
		if l, ok := body["labels_unset"].([]any); ok {
			var keep []int
			for _, x := range labels {
				drop := false
				for _, u := range l {
					drop = drop || int(u.(float64)) == x
				}
				if !drop {
					keep = append(keep, x)
				}
			}
			labels = keep
		}
		if l, ok := body["labels_set"].([]any); ok {
			for _, x := range l {
				labels = append(labels, int(x.(float64)))
			}
		}
		for _, k := range []string{"vote", "started", "finished", "notes"} {
			if val, ok := body[k]; ok {
				e[k] = val
			}
		}
		withoutVoted := []int{}
		for _, x := range labels {
			if x != 7 {
				withoutVoted = append(withoutVoted, x)
			}
		}
		if e["vote"] != nil {
			withoutVoted = append(withoutVoted, 7)
		}
		e["labels"] = withoutVoted
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

func (v *vndbMock) entry(id string) (map[string]any, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.entries[id]
	if !ok {
		return nil, false
	}
	cp := map[string]any{}
	for k, x := range e {
		cp[k] = x
	}
	sort.Ints(cp["labels"].([]int))
	return cp, true
}

func TestVisualNovel(t *testing.T) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Skip("needs a session bus")
	}
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_RUNTIME_DIR", tmp)

	vm := &vndbMock{entries: map[string]map[string]any{}, patches: map[string][]map[string]any{}}
	srv := httptest.NewServer(vm)
	defer srv.Close()
	vndbAPI(srv.URL)
	patches := func(id string) []map[string]any {
		vm.mu.Lock()
		defer vm.mu.Unlock()
		return append([]map[string]any(nil), vm.patches[id]...)
	}

	var tok store.Tokens
	tok.VNDB.Token, tok.VNDB.UserID, tok.VNDB.User = "t", "u1", "tester"
	store.SaveTokens(config.ConfigDir(), tok)

	vnDir := filepath.Join(tmp, "VN")
	game := filepath.Join(vnDir, "[Key] Summer_Pockets v1.02 (ENG)")
	os.MkdirAll(game, 0o755)
	bin := filepath.Join(game, "SummerPockets.x86_64")
	src, _ := os.ReadFile("/bin/sleep")
	os.WriteFile(bin, src, 0o755)

	cfg := config.Default()
	cfg.Notify = false
	cfg.AniList.Enabled, cfg.MAL.Enabled = false, false
	cfg.VNDB.Enabled = true
	cfg.VNDB.Dirs = []string{vnDir}
	cfg.VNDB.ScanInterval = config.Duration{Duration: 500 * time.Millisecond}
	cfg.VNDB.MinSession = config.Duration{Duration: time.Second}
	cfg.VNDB.PlaytimeNote = true
	go daemon.Run(cfg)
	sock := config.SocketPath()
	waitFor(t, "daemon", 5*time.Second, func() bool { return ipc.Call(sock, "status", nil, nil) == nil })

	cmd := exec.Command(bin, "4")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "marked playing", 10*time.Second, func() bool {
		p := patches("v20424")
		return len(p) >= 1 && fmt.Sprint(p[0]["labels_set"]) == "[1]" && p[0]["started"] != nil
	})
	// the running VN is in the status (TUI Now tab)
	waitFor(t, "running VN in status", 5*time.Second, func() bool {
		var st daemon.Status
		ipc.Call(sock, "status", nil, &st)
		return len(st.VNNow) == 1 && st.VNNow[0].VNDBID == "v20424" && st.VNNow[0].Session > 0
	})
	cmd.Wait()
	waitFor(t, "playtime note", 10*time.Second, func() bool {
		for _, p := range patches("v20424") {
			if n, ok := p["notes"].(string); ok && strings.HasPrefix(n, "Playtime: 0h") {
				return true
			}
		}
		return false
	})
	var got string
	if err := ipc.Call(sock, "vn-finish", daemon.VNFinishArgs{Name: "summer", Vote: 85}, &got); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "finished with vote", 10*time.Second, func() bool {
		for _, p := range patches("v20424") {
			if fmt.Sprint(p["labels_set"]) == "[2]" && p["vote"] == float64(85) && p["finished"] != nil {
				return true
			}
		}
		return false
	})
	var l []daemon.VNInfo
	ipc.Call(sock, "vn-list", nil, &l)
	t.Logf("vn list: %+v", l)
	if len(l) != 1 || l[0].Playtime < 2*time.Second {
		t.Fatalf("playtime not recorded: %+v", l)
	}

	// --- the local VN list (what the TUI shows)
	t.Log("vnlist-refresh: labels, entries and local playtime")
	var rep daemon.VNListReply
	if err := ipc.Call(sock, "vnlist-refresh", nil, &rep); err != nil || rep.Error != "" {
		t.Fatalf("vnlist-refresh: %v %s", err, rep.Error)
	}
	e := rep.List.Find("v20424")
	if e == nil || !e.Has(2) || e.Has(1) || e.Vote != 85 || e.Playtime < 2 || e.Title != "Summer Pockets" || e.Cover == "" {
		t.Fatalf("list entry: %+v", e)
	}
	if rep.List.LabelName(10) != "Favourites" || len(rep.List.Labels) != len(vnLabels) {
		t.Fatalf("labels: %+v", rep.List.Labels)
	}
	if fl, err := vnlist.Load(); err != nil || fl.Find("v20424") == nil {
		t.Fatalf("vnlist.json not written: %v", err)
	}

	// --- TUI edits through the daemon queue
	vnset := func(a daemon.VNSetArgs) {
		t.Helper()
		var id int64
		if err := ipc.Call(sock, "vnset", a, &id); err != nil {
			t.Fatal(err)
		}
	}
	server := func(what string, f func(e map[string]any, ok bool) bool) {
		t.Helper()
		waitFor(t, what, 10*time.Second, func() bool { e, ok := vm.entry("v17"); return f(e, ok) })
	}
	meta := &vnlist.Entry{ID: "v17", Title: "Ever17", Rating: 87}
	t.Log("add to wishlist")
	vnset(daemon.VNSetArgs{ID: "v17", Title: "Ever17", Meta: meta, VNEdit: store.VNEdit{LabelsSet: []int{5}}})
	server("wishlist", func(e map[string]any, ok bool) bool { return ok && fmt.Sprint(e["labels"]) == "[5]" })
	t.Log("vote + custom label")
	v90 := 90
	vnset(daemon.VNSetArgs{ID: "v17", Title: "Ever17", VNEdit: store.VNEdit{Vote: &v90, LabelsSet: []int{10}}})
	server("vote 9 and favourite", func(e map[string]any, ok bool) bool {
		return e["vote"] == float64(90) && fmt.Sprint(e["labels"]) == "[5 7 10]"
	})
	t.Log("undo the vote")
	var h store.HistoryEntry
	if err := ipc.Call(sock, "undo", int64(0), &h); err != nil {
		t.Fatal(err)
	}
	server("undone", func(e map[string]any, ok bool) bool { return e["vote"] == nil && fmt.Sprint(e["labels"]) == "[5]" })
	t.Log("remove, then undo the removal")
	vnset(daemon.VNSetArgs{ID: "v17", Title: "Ever17", VNEdit: store.VNEdit{Delete: true}})
	server("removed", func(e map[string]any, ok bool) bool { return !ok })
	if err := ipc.Call(sock, "undo", int64(0), &h); err != nil {
		t.Fatal(err)
	}
	server("restored", func(e map[string]any, ok bool) bool { return ok && fmt.Sprint(e["labels"]) == "[5]" })

	t.Log("offline edit: pending in the local list, applied when back")
	vm.down.Store(true)
	today := time.Now().Format("2006-01-02")
	vnset(daemon.VNSetArgs{ID: "v17", Title: "Ever17", VNEdit: store.VNEdit{LabelsSet: []int{1}, LabelsUnset: []int{5}}})
	var off daemon.VNListReply
	ipc.Call(sock, "vnlist", nil, &off)
	if e := off.List.Find("v17"); e == nil || !e.Pending || !e.Has(1) || e.Has(5) || e.Started != today {
		t.Fatalf("offline edit not in local list: %+v", e)
	}
	if fl, _ := vnlist.Load(); fl.Find("v17") == nil || !fl.Find("v17").Pending {
		t.Fatal("offline edit not saved to disk")
	}
	vm.down.Store(false)
	ipc.Call(sock, "sync", nil, nil)
	server("applied after reconnect", func(e map[string]any, ok bool) bool {
		return fmt.Sprint(e["labels"]) == "[1]" && e["started"] == today
	})
	waitFor(t, "local entry confirmed", 10*time.Second, func() bool {
		var rep daemon.VNListReply // fresh: decoding into a reused value keeps stale omitempty fields
		ipc.Call(sock, "vnlist", nil, &rep)
		e := rep.List.Find("v17")
		return e != nil && !e.Pending
	})
	// bad input is rejected up front
	bad := 5
	if err := ipc.Call(sock, "vnset", daemon.VNSetArgs{ID: "v17", VNEdit: store.VNEdit{Vote: &bad}}, nil); err == nil {
		t.Fatal("vote 0.5 accepted")
	}
	if err := ipc.Call(sock, "vnset", daemon.VNSetArgs{ID: "17"}, nil); err == nil {
		t.Fatal("bad id accepted")
	}
	var st daemon.Status
	ipc.Call(sock, "status", nil, &st)
	if len(st.Queue) != 0 {
		t.Fatalf("queue not empty: %+v", st.Queue[0])
	}
}
