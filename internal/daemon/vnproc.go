package daemon

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/evansunde/anisync/internal/media"
	"github.com/evansunde/anisync/internal/netx"
	"github.com/evansunde/anisync/internal/store"
	"github.com/evansunde/anisync/internal/vndb"
	"github.com/evansunde/anisync/internal/vnlist"
)

func (d *Daemon) onVNStart(key, folder string) {
	k := media.VNKey(folder)
	d.st.Lock()
	e := d.st.S.VN[k]
	if e == nil {
		e = &store.VNEntry{Key: k, Dir: folder}
		d.st.S.VN[k] = e
	}
	e.LastPlayed = time.Now()
	queue := !e.Started
	var it *store.Item
	if queue {
		e.Started = true
		it = &store.Item{ID: d.st.NewID(), Kind: "vn", Key: k, Title: folder, Action: "start", Created: time.Now()}
		d.st.S.Queue = append(d.st.S.Queue, it)
	}
	_ = d.st.Save()
	d.st.Unlock()
	log.Printf("vn: playing %s", folder)
	if queue {
		cp := *it
		d.overlay(&cp)
		d.Kick()
	}
}

func (d *Daemon) onVNStop(key, folder string, dur time.Duration) {
	k := media.VNKey(folder)
	d.st.Lock()
	e := d.st.S.VN[k]
	if e == nil {
		e = &store.VNEntry{Key: k, Dir: folder}
		d.st.S.VN[k] = e
	}
	e.Playtime += dur
	e.LastPlayed = time.Now()
	queued := false
	if d.cfg.VNDB.PlaytimeNote {
		for _, it := range d.st.S.Queue {
			if it.Kind == "vn" && it.Key == k && it.Action == "playtime" {
				queued = true
			}
		}
		if !queued {
			d.st.S.Queue = append(d.st.S.Queue, &store.Item{ID: d.st.NewID(), Kind: "vn", Key: k, Title: folder, Action: "playtime", Created: time.Now()})
		}
	}
	_ = d.st.Save()
	d.st.Unlock()
	log.Printf("vn: stopped %s after %s (total %s)", folder, dur.Round(time.Second), e.Playtime.Round(time.Second))
	d.vnCachePlaytime()
	if d.cfg.VNDB.PlaytimeNote && !queued {
		d.Kick()
	}
}

func (d *Daemon) resolveVN(ctx context.Context, it *store.Item) (store.Mapping, error) {
	d.st.Lock()
	m, ok := d.st.S.Mappings[it.Key]
	_, pending := d.st.S.Pending[it.Key]
	_, ignored := d.st.S.Ignored[it.Key]
	d.st.Unlock()
	switch {
	case ignored:
		return m, errDrop
	case ok:
		return m, nil
	case pending:
		return m, errBlocked
	}
	q := media.CleanVNFolder(it.Title)
	res, err := d.vndb.Search(ctx, q)
	if err != nil {
		return m, err
	}
	n := media.MatchForm(q)
	var exact []vndb.VN
	for _, v := range res {
		for _, t := range v.AllTitles() {
			if media.MatchForm(t) == n {
				exact = append(exact, v)
				break
			}
		}
	}
	if len(exact) == 1 {
		m = store.Mapping{VNDBID: exact[0].ID, Title: exact[0].Title}
		d.saveMapping(it.Key, m)
		log.Printf("vn: matched %q → %s (%s)", it.Title, m.Title, m.VNDBID)
		return m, nil
	}
	var cands []store.Candidate
	for _, v := range res {
		c := store.Candidate{VNDBID: v.ID, Title: v.Title, Alt: v.AltTitle, Score: bestSim(q, v.AllTitles())}
		if len(v.Released) >= 4 {
			fmt.Sscanf(v.Released[:4], "%d", &c.Year)
		}
		cands = append(cands, c)
	}
	d.addPending(it, cands)
	return m, errBlocked
}

var playtimeLine = regexp.MustCompile(`(?m)^Playtime: .*$`)

func (d *Daemon) processVN(ctx context.Context, it *store.Item) error {
	if !d.enabled(svcVNDB) {
		return errBlocked
	}
	if d.isAuthBroken(svcVNDB) {
		return &netx.Error{Kind: netx.Auth, Msg: "waiting for re-login"}
	}
	m, err := d.resolveVN(ctx, it)
	if err != nil {
		return err
	}
	it.TargetVN, it.TargetTitle = m.VNDBID, m.Title
	if it.Result == nil {
		it.Result = map[string]string{}
	}
	u, err := d.vndb.Entry(ctx, d.vnUser(), m.VNDBID)
	if err != nil {
		return d.vnErr(err)
	}
	cur := vnSnap(u)
	today := netx.Today()
	var e *store.VNEdit
	var result string
	switch it.Action {
	case "start", "finish":
		var skip string
		if e, skip = vnActionEdit(it, cur, today); e == nil {
			it.Result[svcVNDB] = "skipped (" + skip + ")"
			return nil
		}
		result = "marked playing"
		if it.Action == "finish" {
			result = "marked finished"
			if it.Vote > 0 {
				result += fmt.Sprintf(", vote %.1f", float64(it.Vote)/10)
			}
		}
	case "playtime":
		d.st.Lock()
		var pt time.Duration
		if e := d.st.S.VN[it.Key]; e != nil {
			pt = e.Playtime
		}
		d.st.Unlock()
		line := fmt.Sprintf("Playtime: %dh %02dm", int(pt.Hours()), int(pt.Minutes())%60)
		notes := cur.Notes
		if playtimeLine.MatchString(notes) {
			notes = playtimeLine.ReplaceAllString(notes, line)
		} else if notes == "" {
			notes = line
		} else {
			notes = strings.TrimRight(notes, "\n") + "\n" + line
		}
		e = &store.VNEdit{Notes: &notes}
		result = "skipped (" + strings.ToLower(line) + ")" // quiet: no notification
	default:
		return fmt.Errorf("%w: unknown vn action %q", errDrop, it.Action)
	}
	next, _, skip := ApplyVNEdit(cur, e, today)
	if skip != "" {
		it.Result[svcVNDB] = "skipped (" + skip + ")"
		return nil
	}
	if err := d.vndb.Patch(ctx, m.VNDBID, vnPatch(cur, next)); err != nil {
		return d.vnErr(err)
	}
	if it.Action != "playtime" {
		it.BeforeVN = &cur // undoable
	}
	it.Result[svcVNDB] = result
	d.vnCacheSet(vnlist.Entry{ID: m.VNDBID, Title: m.Title}, next, false, true)
	return nil
}

func (d *Daemon) vnErr(err error) error {
	if netx.KindOf(err) == netx.Auth {
		d.markAuthBroken(svcVNDB, err)
	}
	return err
}
