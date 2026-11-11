package daemon

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"time"

	"github.com/evansunde/anisync/internal/store"
	"github.com/evansunde/anisync/internal/vndb"
	"github.com/evansunde/anisync/internal/vnlist"
)

// The daemon owns vnlist (the local copy of your VNDB list) the same way it
// owns listcache: refreshed from VNDB, queued edits applied right away
// (Pending), playtime filled in from the VN store. Lock: stateMu.

func (d *Daemon) loadVNCache() {
	l, err := vnlist.Load()
	if err != nil {
		log.Printf("vn list cache: %v", err)
	}
	d.stateMu.Lock()
	d.vncache = l
	d.stateMu.Unlock()
}

func (d *Daemon) refreshVNList(ctx context.Context) error {
	if !d.enabled(svcVNDB) {
		return errVNDBOff()
	}
	uid := d.vnUser()
	labels, err := d.vndb.Labels(ctx, uid)
	if err != nil {
		return d.vnErr(err)
	}
	items, err := d.vndb.UList(ctx, uid)
	if err != nil {
		return d.vnErr(err)
	}
	pt := d.vnPlaytimes()
	l := &vnlist.List{Fetched: time.Now(), User: uid, Labels: labels, Entries: vnlist.FromUList(items)}
	for _, e := range l.Entries {
		e.Playtime = pt[e.ID]
	}
	d.stateMu.Lock()
	d.vncache = l
	d.vnFetched, d.vnRetry = time.Now(), time.Time{}
	err = l.Save()
	d.stateMu.Unlock()
	if err != nil {
		log.Printf("vn list cache: %v", err)
	}
	d.overlayQueue()
	log.Printf("vn list refreshed (%d entries, %d labels)", len(l.Entries), len(labels))
	return nil
}

// maybeRefreshVNList refreshes the VN list when it's stale. Errors never
// block the queue; a failed refresh is retried after a while.
func (d *Daemon) maybeRefreshVNList(ctx context.Context) {
	if !d.enabled(svcVNDB) {
		return
	}
	d.stateMu.Lock()
	due := time.Since(d.vnFetched) > listRefresh && time.Now().After(d.vnRetry)
	d.stateMu.Unlock()
	if !due {
		return
	}
	if err := d.refreshVNList(ctx); err != nil {
		log.Printf("vn list refresh: %v", err)
		d.stateMu.Lock()
		d.vnRetry = time.Now().Add(10 * time.Minute)
		d.stateMu.Unlock()
	}
}

// vnPlaytimes sums tracked playtime per VNDB id (seconds).
func (d *Daemon) vnPlaytimes() map[string]int64 {
	d.st.Lock()
	defer d.st.Unlock()
	out := map[string]int64{}
	for k, e := range d.st.S.VN {
		if m, ok := d.st.S.Mappings[k]; ok && m.VNDBID != "" {
			out[m.VNDBID] += int64(e.Playtime / time.Second)
		}
	}
	return out
}

// vnCachePlaytime refreshes playtime in the cached list (after a session).
func (d *Daemon) vnCachePlaytime() {
	pt := d.vnPlaytimes()
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.vncache == nil {
		return
	}
	for _, e := range d.vncache.Entries {
		e.Playtime = pt[e.ID]
	}
	if err := d.vncache.Save(); err != nil {
		log.Printf("vn list cache: %v", err)
	}
}

func snapOfVN(e *vnlist.Entry) store.VNSnapshot {
	if e == nil || !e.OnList {
		return store.VNSnapshot{}
	}
	return store.VNSnapshot{Exists: true, Labels: append([]int(nil), e.Labels...), Vote: e.Vote, Started: e.Started,
		Finished: e.Finished, Notes: e.Notes}
}

func (d *Daemon) vnCacheUpdate(meta vnlist.Entry, pending bool, fn func(cur store.VNSnapshot) (next store.VNSnapshot, del, ok bool)) {
	if meta.ID == "" {
		return
	}
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.vncache == nil {
		return
	}
	next, del, ok := fn(snapOfVN(d.vncache.Find(meta.ID)))
	if !ok {
		return
	}
	if del {
		d.vncache.Remove(meta.ID)
	} else {
		e := d.vncache.Upsert(meta)
		e.OnList = true
		e.Labels, e.Vote, e.Started, e.Finished, e.Notes = next.Labels, next.Vote, next.Started, next.Finished, next.Notes
		e.Updated = time.Now().Unix()
		if e.Added == 0 {
			e.Added = e.Updated
		}
		e.Pending = pending
	}
	if err := d.vncache.Save(); err != nil {
		log.Printf("vn list cache: %v", err)
	}
}

func (d *Daemon) vnCacheSet(meta vnlist.Entry, next store.VNSnapshot, del, pending bool) {
	d.vnCacheUpdate(meta, pending, func(store.VNSnapshot) (store.VNSnapshot, bool, bool) { return next, del, true })
}

func (d *Daemon) vnCacheClearPending(id string) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.vncache == nil {
		return
	}
	if e := d.vncache.Find(id); e != nil && e.Pending {
		e.Pending = false
		_ = d.vncache.Save()
	}
}

func (d *Daemon) vnCacheCopy() vnlist.List {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.vncache == nil {
		return vnlist.List{}
	}
	cp := *d.vncache
	cp.Labels = append([]vndb.Label(nil), d.vncache.Labels...)
	cp.Entries = make([]*vnlist.Entry, len(d.vncache.Entries))
	for i, e := range d.vncache.Entries {
		x := *e
		x.Labels = append([]int(nil), e.Labels...)
		cp.Entries[i] = &x
	}
	return cp
}

func (d *Daemon) vnLabelName(id int) string {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.vncache != nil {
		return d.vncache.LabelName(id)
	}
	return vnlist.DefaultLabels[id]
}

func vnMetaOf(it *store.Item) vnlist.Entry {
	var m vnlist.Entry
	if len(it.Meta) > 0 && json.Unmarshal(it.Meta, &m) == nil && m.Title != "" {
		m.ID = it.TargetVN
		m.OnList, m.Labels, m.Vote, m.Started, m.Finished, m.Notes, m.Pending = false, nil, 0, "", "", "", false
		return m
	}
	return vnlist.Entry{ID: it.TargetVN, Title: it.TargetTitle}
}

// vnTarget returns the VNDB id a "vn" queue item refers to ("" = not matched yet).
func (d *Daemon) vnTarget(it *store.Item) (string, string) {
	if it.TargetVN != "" {
		return it.TargetVN, it.TargetTitle
	}
	d.st.Lock()
	defer d.st.Unlock()
	m := d.st.S.Mappings[it.Key]
	return m.VNDBID, m.Title
}

// vnActionEdit is the list change for an automatic "vn" item (start/finish);
// nil = nothing to change (with the reason).
func vnActionEdit(it *store.Item, cur store.VNSnapshot, today string) (*store.VNEdit, string) {
	has := func(l int) bool {
		return sort.SearchInts(cur.Labels, l) < len(cur.Labels) && cur.Labels[sort.SearchInts(cur.Labels, l)] == l
	}
	e := &store.VNEdit{}
	switch it.Action {
	case "start":
		if has(vndb.Finished) {
			return nil, "already finished"
		}
		e.LabelsSet = []int{vndb.Playing}
		e.LabelsUnset = []int{vndb.Wishlist, vndb.Stalled}
	case "finish":
		e.LabelsSet = []int{vndb.Finished}
		e.LabelsUnset = []int{vndb.Playing, vndb.Stalled, vndb.Dropped, vndb.Wishlist}
		if cur.Started == "" {
			e.Started = &today
		}
		if it.Vote >= 10 && it.Vote <= 100 {
			v := it.Vote
			e.Vote = &v
		}
	default:
		return nil, "nothing to do"
	}
	return e, ""
}
