package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/evansunde/anisync/internal/listcache"
	"github.com/evansunde/anisync/internal/netx"
	"github.com/evansunde/anisync/internal/store"
)

// SetArgs is a manual list edit (from the TUI). Nil fields stay unchanged.
type SetArgs struct {
	AnilistID int    `json:"anilist_id"`
	MalID     int    `json:"mal_id"`
	Title     string `json:"title"`
	Episodes  int    `json:"episodes,omitempty"`
	Status    string `json:"status,omitempty"` // AniList vocabulary
	Progress  *int   `json:"progress,omitempty"`
	Score     *int   `json:"score,omitempty"` // 0-100
	Delete    bool   `json:"delete,omitempty"`
	// Display data for the local list (title, cover, format…), optional.
	Meta *listcache.Entry `json:"meta,omitempty"`
}

var validStatus = map[string]bool{"CURRENT": true, "PLANNING": true, "COMPLETED": true, "DROPPED": true, "PAUSED": true, "REPEATING": true}

func (d *Daemon) enqueueSet(a SetArgs) (int64, error) {
	if a.AnilistID == 0 && a.MalID == 0 {
		return 0, fmt.Errorf("no anime id")
	}
	if a.Status != "" && !validStatus[a.Status] {
		return 0, fmt.Errorf("invalid status %q", a.Status)
	}
	if !d.enabled(svcAniList) && !d.enabled(svcMAL) {
		return 0, fmt.Errorf("not logged in to AniList or MAL (run: anisync auth anilist|mal)")
	}
	it := &store.Item{Kind: "set", TargetID: a.AnilistID, TargetMal: a.MalID, TargetTitle: a.Title, TargetTotal: a.Episodes,
		SetStatus: a.Status, SetProgress: -1, SetScore: -1, SetDelete: a.Delete, Created: time.Now()}
	if a.Progress != nil {
		it.SetProgress = max(*a.Progress, 0)
	}
	if a.Score != nil {
		it.SetScore = min(max(*a.Score, 0), 100)
	}
	if a.Meta != nil {
		it.Meta, _ = json.Marshal(a.Meta)
	}
	d.st.Lock()
	it.ID = d.st.NewID()
	it.Key = fmt.Sprintf("set:%d:%d", a.AnilistID, a.MalID)
	d.st.S.Queue = append(d.st.S.Queue, it)
	err := d.st.Save()
	d.st.Unlock()
	d.overlay(it) // the local list shows the change immediately
	d.Kick()
	return it.ID, err
}

// applySet computes the entry after a manual edit.
func applySet(cur store.EntrySnapshot, total int, it *store.Item, today string) (next store.EntrySnapshot, del bool, skip string) {
	if it.SetDelete {
		if !cur.Exists {
			return cur, false, "not in list"
		}
		return cur, true, ""
	}
	next = cur
	if !cur.Exists {
		next = store.EntrySnapshot{Exists: true, Status: "PLANNING"}
	}
	if it.SetScore >= 0 {
		next.Score = it.SetScore
	}
	if it.SetProgress >= 0 {
		next.Progress = it.SetProgress
		if total > 0 && next.Progress > total {
			next.Progress = total
		}
	}
	switch {
	case it.SetStatus != "":
		if it.SetStatus == "REPEATING" && cur.Status != "REPEATING" && it.SetProgress < 0 {
			next.Progress = 0
		}
		next.Status = it.SetStatus
	case it.SetProgress >= 0 && total > 0 && next.Progress >= total && next.Status != "COMPLETED":
		if next.Status == "REPEATING" {
			next.Repeat++
		}
		next.Status = "COMPLETED"
	case it.SetProgress > 0 && (next.Status == "PLANNING" || next.Status == "PAUSED" || next.Status == "DROPPED"):
		next.Status = "CURRENT"
	}
	switch next.Status {
	case "CURRENT", "REPEATING":
		if next.Started == "" {
			next.Started = today
		}
	case "COMPLETED":
		if total > 0 && next.Progress < total {
			next.Progress = total
		}
		if next.Completed == "" {
			next.Completed = today
		}
	}
	if next == cur {
		return cur, false, "no change"
	}
	return next, false, ""
}

func (d *Daemon) processSet(ctx context.Context, it *store.Item) error {
	if it.Done == nil {
		it.Done, it.Result, it.Before = map[string]bool{}, map[string]string{}, map[string]store.EntrySnapshot{}
	}
	today := netx.Today()
	if d.enabled(svcAniList) && !it.Done[svcAniList] && it.TargetID != 0 && !d.isAuthBroken(svcAniList) {
		m, err := d.al.Media(ctx, it.TargetID)
		if err != nil {
			return d.authCheck(svcAniList, err)
		}
		cur := m.MediaListEntry.Snapshot()
		next, del, skip := applySet(cur, m.Episodes, it, today)
		switch {
		case skip != "":
			it.Result[svcAniList] = "skipped (" + skip + ")"
		case del:
			err = d.al.Delete(ctx, cur.EntryID)
		default:
			err = d.al.Save(ctx, it.TargetID, next)
		}
		if err != nil {
			return d.authCheck(svcAniList, err)
		}
		if skip == "" {
			it.Before[svcAniList] = cur
			it.Result[svcAniList] = describeSet(cur, next, del)
			d.cacheSet(metaOf(it), next, del, true)
		}
		it.Done[svcAniList] = true
		d.putItem(it)
	}
	if d.enabled(svcMAL) && !it.Done[svcMAL] && !d.isAuthBroken(svcMAL) {
		if it.TargetMal == 0 {
			it.Result[svcMAL] = "skipped (not on MAL)"
		} else {
			cur, total, err := d.mal.Entry(ctx, it.TargetMal)
			if err != nil {
				return d.authCheck(svcMAL, err)
			}
			if total == 0 {
				total = it.TargetTotal
			}
			next, del, skip := applySet(cur, total, it, today)
			switch {
			case skip != "":
				it.Result[svcMAL] = "skipped (" + skip + ")"
			case del:
				err = d.mal.Delete(ctx, it.TargetMal)
			default:
				err = d.mal.Save(ctx, it.TargetMal, next)
			}
			if err != nil {
				return d.authCheck(svcMAL, err)
			}
			if skip == "" {
				it.Before[svcMAL] = cur
				it.Result[svcMAL] = strings.Replace(describeSet(cur, next, del),
					fmt.Sprintf("score %.1f", float64(next.Score)/10), fmt.Sprintf("score %d", (next.Score+5)/10), 1) // MAL: whole numbers
			}
		}
		it.Done[svcMAL] = true
		d.putItem(it)
	}
	if (d.enabled(svcAniList) && it.TargetID != 0 && !it.Done[svcAniList]) || (d.enabled(svcMAL) && !it.Done[svcMAL]) {
		return &netx.Error{Kind: netx.Auth, Msg: "waiting for re-login"}
	}
	return nil
}

func (d *Daemon) authCheck(svc string, err error) error {
	if netx.KindOf(err) == netx.Auth {
		d.markAuthBroken(svc, err)
	}
	return err
}

func describeSet(cur, next store.EntrySnapshot, del bool) string {
	if del {
		return "removed from list"
	}
	var parts []string
	if !cur.Exists {
		parts = append(parts, "added as "+strings.ToLower(next.Status))
	} else if cur.Status != next.Status {
		parts = append(parts, strings.ToLower(next.Status))
	}
	if cur.Progress != next.Progress {
		parts = append(parts, fmt.Sprintf("progress %d → %d", cur.Progress, next.Progress))
	}
	if cur.Score != next.Score {
		parts = append(parts, fmt.Sprintf("score %.1f", float64(next.Score)/10))
	}
	return strings.Join(parts, ", ")
}
