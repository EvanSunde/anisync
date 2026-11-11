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
	if queue {
		e.Started = true
		d.st.S.Queue = append(d.st.S.Queue, &store.Item{ID: d.st.NewID(), Kind: "vn", Key: k, Title: folder, Action: "start", Created: time.Now()})
	}
	_ = d.st.Save()
	d.st.Unlock()
	log.Printf("vn: playing %s", folder)
	if queue {
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
	d.tokMu.Lock()
	uid := d.tok.VNDB.UserID
	d.tokMu.Unlock()
	cur, err := d.vndb.Entry(ctx, uid, m.VNDBID)
	if err != nil {
		return d.vnErr(err)
	}
	today := netx.Today()
	patch := map[string]any{}
	switch it.Action {
	case "start":
		if cur.Has(vndb.Finished) {
			it.Result[svcVNDB] = "skipped (already finished)"
			return nil
		}
		if !cur.Has(vndb.Playing) {
			patch["labels_set"] = []int{vndb.Playing}
			patch["labels_unset"] = []int{vndb.Wishlist, vndb.Stalled}
		}
		if cur == nil || cur.Started == nil || *cur.Started == "" {
			patch["started"] = today
		}
		it.Result[svcVNDB] = "marked playing"
	case "finish":
		patch["labels_set"] = []int{vndb.Finished}
		patch["labels_unset"] = []int{vndb.Playing, vndb.Stalled, vndb.Dropped, vndb.Wishlist}
		if cur == nil || cur.Finished == nil || *cur.Finished == "" {
			patch["finished"] = today
		}
		if cur == nil || cur.Started == nil || *cur.Started == "" {
			patch["started"] = today
		}
		if it.Vote >= 10 && it.Vote <= 100 {
			patch["vote"] = it.Vote
		}
		it.Result[svcVNDB] = "marked finished"
		if it.Vote > 0 {
			it.Result[svcVNDB] += fmt.Sprintf(", vote %.1f", float64(it.Vote)/10)
		}
	case "playtime":
		d.st.Lock()
		var pt time.Duration
		if e := d.st.S.VN[it.Key]; e != nil {
			pt = e.Playtime
		}
		d.st.Unlock()
		line := fmt.Sprintf("Playtime: %dh %02dm", int(pt.Hours()), int(pt.Minutes())%60)
		notes := ""
		if cur != nil && cur.Notes != nil {
			notes = *cur.Notes
		}
		if playtimeLine.MatchString(notes) {
			notes = playtimeLine.ReplaceAllString(notes, line)
		} else if notes == "" {
			notes = line
		} else {
			notes = strings.TrimRight(notes, "\n") + "\n" + line
		}
		patch["notes"] = notes
		it.Result[svcVNDB] = "skipped (" + strings.ToLower(line) + ")" // quiet: no notification
	}
	if len(patch) > 0 {
		if err := d.vndb.Patch(ctx, m.VNDBID, patch); err != nil {
			return d.vnErr(err)
		}
	}
	return nil
}

func (d *Daemon) vnErr(err error) error {
	if netx.KindOf(err) == netx.Auth {
		d.markAuthBroken(svcVNDB, err)
	}
	return err
}
