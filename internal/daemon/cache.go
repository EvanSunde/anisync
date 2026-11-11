package daemon

import (
	"encoding/json"
	"log"
	"time"

	"github.com/evansunde/anisync/internal/listcache"
	"github.com/evansunde/anisync/internal/netx"
	"github.com/evansunde/anisync/internal/store"
	"github.com/evansunde/anisync/internal/vnlist"
)

// The daemon owns listcache: refreshed from the server, and every queued
// change is applied to it right away (Pending) so the list is correct
// offline. Lock: stateMu.

func (d *Daemon) loadCache() {
	l, err := listcache.Load()
	if err != nil {
		log.Printf("list cache: %v", err)
	}
	d.stateMu.Lock()
	d.cache = l
	d.stateMu.Unlock()
}

func snapOf(e *listcache.Entry) store.EntrySnapshot {
	if e == nil || e.Status == "" {
		return store.EntrySnapshot{}
	}
	return store.EntrySnapshot{Exists: true, Status: e.Status, Progress: e.Progress, Score: e.MyScore, Repeat: e.Repeat}
}

// cacheUpdate applies fn to the cached entry of a show and saves the file.
func (d *Daemon) cacheUpdate(meta listcache.Entry, pending bool, fn func(cur store.EntrySnapshot) (next store.EntrySnapshot, del, ok bool)) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.cache == nil {
		return
	}
	cur := snapOf(d.cache.Find(meta.AnilistID, meta.MalID))
	next, del, ok := fn(cur)
	if !ok {
		return
	}
	if del {
		d.cache.Remove(meta.AnilistID, meta.MalID)
	} else {
		e := d.cache.Upsert(meta)
		e.Status, e.Progress, e.MyScore, e.Repeat = next.Status, next.Progress, next.Score, next.Repeat
		e.Updated = time.Now().Unix()
		e.Pending = pending
	}
	if err := d.cache.Save(); err != nil {
		log.Printf("list cache: %v", err)
	}
}

func (d *Daemon) cacheSet(meta listcache.Entry, next store.EntrySnapshot, del, pending bool) {
	d.cacheUpdate(meta, pending, func(store.EntrySnapshot) (store.EntrySnapshot, bool, bool) { return next, del, true })
}

func (d *Daemon) cacheClearPending(anilistID, malID int) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.cache == nil {
		return
	}
	if e := d.cache.Find(anilistID, malID); e != nil && e.Pending {
		e.Pending = false
		_ = d.cache.Save()
	}
}

func metaOf(it *store.Item) listcache.Entry {
	var m listcache.Entry
	if len(it.Meta) > 0 && json.Unmarshal(it.Meta, &m) == nil && m.Title != "" {
		m.AnilistID, m.MalID = it.TargetID, it.TargetMal
		if m.Episodes == 0 {
			m.Episodes = it.TargetTotal
		}
		return m
	}
	return listcache.Entry{AnilistID: it.TargetID, MalID: it.TargetMal, Title: it.TargetTitle, Episodes: it.TargetTotal}
}

// overlay applies a queued item's expected effect to the cache.
func (d *Daemon) overlay(it *store.Item) {
	today := netx.Today()
	switch {
	case it.Kind == "set":
		d.cacheUpdate(metaOf(it), true, func(cur store.EntrySnapshot) (store.EntrySnapshot, bool, bool) {
			next, del, skip := applySet(cur, it.TargetTotal, it, today)
			return next, del, skip == "" || del
		})
	case it.Kind == "anime" && it.TargetID != 0:
		d.cacheUpdate(metaOf(it), true, func(cur store.EntrySnapshot) (store.EntrySnapshot, bool, bool) {
			next, skip := plan(cur, it.TargetTotal, it.TargetEp, d.cfg.AddToList, d.cfg.Rewatch, today)
			return next, false, skip == ""
		})
	case it.Kind == "vnset":
		d.vnCacheUpdate(vnMetaOf(it), true, func(cur store.VNSnapshot) (store.VNSnapshot, bool, bool) {
			next, del, skip := ApplyVNEdit(cur, it.VNEdit, today)
			return next, del, skip == "" || del
		})
	case it.Kind == "vn" && (it.Action == "start" || it.Action == "finish"):
		vid, title := d.vnTarget(it)
		if vid == "" {
			return
		}
		d.vnCacheUpdate(vnlist.Entry{ID: vid, Title: title}, true, func(cur store.VNSnapshot) (store.VNSnapshot, bool, bool) {
			e, _ := vnActionEdit(it, cur, today)
			if e == nil {
				return cur, false, false
			}
			next, _, skip := ApplyVNEdit(cur, e, today)
			return next, false, skip == ""
		})
	}
}

// overlayQueue re-applies everything still queued (after a server refresh).
func (d *Daemon) overlayQueue() {
	d.st.Lock()
	items := make([]*store.Item, 0, len(d.st.S.Queue))
	for _, it := range d.st.S.Queue {
		cp := *it
		items = append(items, &cp)
	}
	d.st.Unlock()
	for _, it := range items {
		d.overlay(it)
	}
}

func (d *Daemon) cacheCopy() listcache.List {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.cache == nil {
		return listcache.List{}
	}
	cp := listcache.List{Fetched: d.cache.Fetched, Source: d.cache.Source, Entries: make([]*listcache.Entry, len(d.cache.Entries))}
	for i, e := range d.cache.Entries {
		x := *e
		cp.Entries[i] = &x
	}
	return cp
}
