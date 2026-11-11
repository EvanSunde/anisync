package mpris

import (
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/evansunde/anisync/internal/store"
)

type memCov map[string]*store.Coverage

func (m memCov) GetCoverage(k string) *store.Coverage {
	if c, ok := m[k]; ok {
		cp := *c
		cp.Spans = append([][2]float64(nil), c.Spans...)
		return &cp
	}
	return nil
}
func (m memCov) PutCoverage(k string, c *store.Coverage) {
	cp := *c
	cp.Spans = append([][2]float64(nil), c.Spans...)
	m[k] = &cp
}

func md(url string, lengthSec int64) map[string]dbus.Variant {
	return map[string]dbus.Variant{"Metadata": dbus.MakeVariant(map[string]dbus.Variant{
		"xesam:url":    dbus.MakeVariant(url),
		"mpris:length": dbus.MakeVariant(lengthSec * 1_000_000),
	})}
}
func st(s string) map[string]dbus.Variant {
	return map[string]dbus.Variant{"PlaybackStatus": dbus.MakeVariant(s)}
}

// fakePos is what the simulated player reports for Position.
type fakePos struct {
	pos float64
	ok  bool
}

func newTest(cov memCov) (*Watcher, *player, chan Event, *fakePos) {
	ev := make(chan Event, 4)
	fp := &fakePos{ok: true}
	w := &Watcher{opt: Options{Threshold: 0.8, MinLength: 3 * time.Minute, Coverage: cov,
		OnWatched: func(e Event) { ev <- e }}, players: map[string]*player{}}
	w.queryPos = func(*player) (float64, bool) { return fp.pos, fp.ok }
	p := &player{name: "org.mpris.MediaPlayer2.mpv", owner: ":1.1", rate: 1, status: "Stopped"}
	w.players[p.owner] = p
	return w, p, ev, fp
}

func got(ev chan Event) bool {
	select {
	case <-ev:
		return true
	case <-time.After(50 * time.Millisecond):
		return false
	}
}

func near(t *testing.T, what string, got, want float64) {
	t.Helper()
	if got < want-1 || got > want+1 {
		t.Fatalf("%s: got %.1f want %.1f", what, got, want)
	}
}

func TestSeekDoesNotCount(t *testing.T) {
	w, p, ev, fp := newTest(memCov{})
	t0 := time.Now()
	w.applyProps(p, md("file:///a/Show - 01.mkv", 1440), t0)
	w.applyProps(p, st("Playing"), t0)
	// watch 2 minutes, then seek to 23:00, watch 30s
	w.onSeek(p, 1380, t0.Add(120*time.Second))
	fp.pos = 1410
	w.applyProps(p, st("Paused"), t0.Add(150*time.Second))
	if got(ev) {
		t.Fatal("seek to end must not count as watched")
	}
	near(t, "covered", p.covered(t0.Add(150*time.Second)), 150)
}

func TestWatchedFiresOnceAndResumes(t *testing.T) {
	cov := memCov{}
	w, p, ev, _ := newTest(cov)
	t0 := time.Now()
	url := "file:///a/Show - 02.mkv"
	w.applyProps(p, md(url, 1440), t0)
	w.applyProps(p, st("Playing"), t0)
	// watch half, close player
	w.applyProps(p, st("Stopped"), t0.Add(720*time.Second))
	if got(ev) {
		t.Fatal("too early")
	}
	// next day: new player instance resumes at 12:00 (coverage persisted)
	w2, p2, ev2, fp2 := newTest(cov)
	fp2.pos = 720
	t1 := t0.Add(24 * time.Hour)
	w2.applyProps(p2, md(url, 1440), t1)
	w2.applyProps(p2, st("Playing"), t1)
	fp2.pos = 720 + 400
	w2.tick2(t1.Add(400 * time.Second)) // 1120 < 1152
	if got(ev2) {
		t.Fatal("fired before 80%")
	}
	fp2.pos = 720 + 440
	w2.tick2(t1.Add(440 * time.Second)) // 1160 >= 1152
	if !got(ev2) {
		t.Fatal("expected watched event after resume")
	}
	w2.applyProps(p2, st("Paused"), t1.Add(500*time.Second))
	w2.applyProps(p2, st("Playing"), t1.Add(510*time.Second))
	w2.tick2(t1.Add(900 * time.Second))
	if got(ev2) {
		t.Fatal("fired twice")
	}
}

// tick2 is tick() with an explicit clock.
func (w *Watcher) tick2(now time.Time) {
	for _, p := range w.players {
		if p.playing && p.cov != nil && !p.cov.Counted {
			w.reconcile(p, now)
			w.check(p, now)
		}
	}
}

func TestShortVideoIgnored(t *testing.T) {
	w, p, ev, fp := newTest(memCov{})
	t0 := time.Now()
	w.applyProps(p, md("file:///a/NCOP.mkv", 90), t0)
	w.applyProps(p, st("Playing"), t0)
	fp.pos = 90
	w.tick2(t0.Add(90 * time.Second))
	if got(ev) {
		t.Fatal("short video counted")
	}
}

func TestRateChange(t *testing.T) {
	w, p, ev, fp := newTest(memCov{})
	t0 := time.Now()
	w.applyProps(p, md("file:///a/Show - 03.mkv", 1000), t0)
	w.applyProps(p, st("Playing"), t0)
	fp.pos = 100
	w.applyProps(p, map[string]dbus.Variant{"Rate": dbus.MakeVariant(2.0)}, t0.Add(100*time.Second))
	// 100s at 1x + 350s at 2x = 800s of content
	fp.pos = 800
	w.tick2(t0.Add(450 * time.Second))
	if !got(ev) {
		t.Fatalf("expected fire at 2x, covered=%v", p.covered(t0.Add(450*time.Second)))
	}
}

// The player's reported position wins over wall-clock time: if playback
// stalled (buffering, frozen decoder), only what actually played counts.
func TestStallUsesPlayerPosition(t *testing.T) {
	w, p, _, fp := newTest(memCov{})
	t0 := time.Now()
	w.applyProps(p, md("file:///a/Show - 04.mkv", 1440), t0)
	w.applyProps(p, st("Playing"), t0)
	fp.pos = 30 // 60s of wall time, but the player only advanced 30s
	w.tick2(t0.Add(60 * time.Second))
	near(t, "after stall", p.cov.Total(), 30)
	fp.pos = 60
	w.tick2(t0.Add(90 * time.Second))
	near(t, "after recovery", p.cov.Total(), 60)
}

// A jump the player didn't announce with Seeked credits nothing for the
// uncertain interval, then tracking continues from the new position.
func TestUnannouncedJump(t *testing.T) {
	w, p, _, fp := newTest(memCov{})
	t0 := time.Now()
	w.applyProps(p, md("file:///a/Show - 05.mkv", 1440), t0)
	w.applyProps(p, st("Playing"), t0)
	fp.pos = 30
	w.tick2(t0.Add(30 * time.Second))
	fp.pos = 1200 // jumped without a Seeked signal
	w.tick2(t0.Add(60 * time.Second))
	near(t, "after jump", p.cov.Total(), 30)
	fp.pos = 1230
	w.tick2(t0.Add(90 * time.Second))
	near(t, "continues after jump", p.cov.Total(), 60)
}

// Players that don't report Position fall back to the clock.
func TestFallbackWithoutPosition(t *testing.T) {
	w, p, _, fp := newTest(memCov{})
	fp.ok = false
	t0 := time.Now()
	w.applyProps(p, md("file:///a/Show - 06.mkv", 1440), t0)
	w.applyProps(p, st("Playing"), t0)
	w.tick2(t0.Add(45 * time.Second))
	w.applyProps(p, st("Paused"), t0.Add(100*time.Second))
	near(t, "clock fallback", p.cov.Total(), 100)
	// resume without a report: continues from the last known position
	w.applyProps(p, st("Playing"), t0.Add(200*time.Second))
	w.applyProps(p, st("Paused"), t0.Add(250*time.Second))
	if len(p.cov.Spans) != 1 {
		t.Fatalf("resume should extend the same span, got %v", p.cov.Spans)
	}
	near(t, "after resume", p.cov.Total(), 150)
}
