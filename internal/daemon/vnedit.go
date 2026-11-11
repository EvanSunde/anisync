package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/evansunde/anisync/internal/netx"
	"github.com/evansunde/anisync/internal/store"
	"github.com/evansunde/anisync/internal/vndb"
	"github.com/evansunde/anisync/internal/vnlist"
)

// VNSetArgs is a manual VNDB list edit (from the TUI).
type VNSetArgs struct {
	ID    string `json:"id"` // v123
	Title string `json:"title"`
	store.VNEdit
	// Display data for the local list, optional.
	Meta *vnlist.Entry `json:"meta,omitempty"`
}

var (
	reVNID = regexp.MustCompile(`^v\d+$`)
	reDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

func errVNDBOff() error {
	return fmt.Errorf("VNDB isn't set up: set [vndb] enabled = true and run: anisync auth vndb")
}

func (d *Daemon) enqueueVNSet(a VNSetArgs) (int64, error) {
	if !reVNID.MatchString(a.ID) {
		return 0, fmt.Errorf("bad VNDB id %q", a.ID)
	}
	if !d.enabled(svcVNDB) {
		return 0, errVNDBOff()
	}
	e := a.VNEdit
	e.LabelsSet, e.LabelsUnset = withoutVoted(e.LabelsSet), withoutVoted(e.LabelsUnset)
	if e.Vote != nil && *e.Vote != 0 && (*e.Vote < 10 || *e.Vote > 100) {
		return 0, fmt.Errorf("vote must be 1-10")
	}
	for _, p := range []*string{e.Started, e.Finished} {
		if p != nil && *p != "" && !reDate.MatchString(*p) {
			return 0, fmt.Errorf("bad date %q (use YYYY-MM-DD)", *p)
		}
	}
	it := &store.Item{Kind: "vnset", Key: "vnset:" + a.ID, Title: a.Title, TargetVN: a.ID, TargetTitle: a.Title,
		VNEdit: &e, Created: time.Now()}
	if a.Meta != nil {
		it.Meta, _ = json.Marshal(a.Meta)
	}
	d.st.Lock()
	it.ID = d.st.NewID()
	d.st.S.Queue = append(d.st.S.Queue, it)
	err := d.st.Save()
	d.st.Unlock()
	d.overlay(it)
	d.Kick()
	return it.ID, err
}

// Voted (label 7) is managed by VNDB itself.
func withoutVoted(l []int) []int {
	var out []int
	for _, x := range l {
		if x != vndb.Voted && x > 0 && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func vnSnap(u *vndb.UEntry) store.VNSnapshot {
	if u == nil {
		return store.VNSnapshot{}
	}
	s := store.VNSnapshot{Exists: true}
	for _, l := range u.Labels {
		s.Labels = append(s.Labels, l.ID)
	}
	sort.Ints(s.Labels)
	if u.Vote != nil {
		s.Vote = *u.Vote
	}
	if u.Started != nil {
		s.Started = *u.Started
	}
	if u.Finished != nil {
		s.Finished = *u.Finished
	}
	if u.Notes != nil {
		s.Notes = *u.Notes
	}
	return s
}

func vnEqual(a, b store.VNSnapshot) bool {
	return a.Exists == b.Exists && slices.Equal(a.Labels, b.Labels) && a.Vote == b.Vote && a.Started == b.Started &&
		a.Finished == b.Finished && a.Notes == b.Notes
}

// ApplyVNEdit computes the entry after an edit. Adding Playing fills in the
// start date and adding Finished the finish date, unless the edit sets them.
func ApplyVNEdit(cur store.VNSnapshot, e *store.VNEdit, today string) (next store.VNSnapshot, del bool, skip string) {
	if e.Delete {
		if !cur.Exists {
			return cur, false, "not in list"
		}
		return cur, true, ""
	}
	next = cur
	next.Exists = true
	next.Labels = nil
	for _, l := range cur.Labels {
		if !slices.Contains(e.LabelsUnset, l) {
			next.Labels = append(next.Labels, l)
		}
	}
	added := map[int]bool{}
	for _, l := range e.LabelsSet {
		if !slices.Contains(next.Labels, l) {
			next.Labels = append(next.Labels, l)
			added[l] = true
		}
	}
	if e.Vote != nil {
		next.Vote = *e.Vote
	}
	if e.Started != nil {
		next.Started = *e.Started
	} else if added[vndb.Playing] && next.Started == "" {
		next.Started = today
	}
	if e.Finished != nil {
		next.Finished = *e.Finished
	} else if added[vndb.Finished] && next.Finished == "" {
		next.Finished = today
	}
	if e.Notes != nil {
		next.Notes = *e.Notes
	}
	// VNDB keeps label 7 in step with the vote
	next.Labels = withoutVoted(next.Labels)
	if next.Vote > 0 {
		next.Labels = append(next.Labels, vndb.Voted)
	}
	sort.Ints(next.Labels)
	if vnEqual(next, cur) {
		return cur, false, "no change"
	}
	return next, false, ""
}

// vnPatch is the PATCH /ulist body that turns cur into next.
func vnPatch(cur, next store.VNSnapshot) map[string]any {
	p := map[string]any{}
	var set, unset []int
	for _, l := range next.Labels {
		if l != vndb.Voted && !slices.Contains(cur.Labels, l) {
			set = append(set, l)
		}
	}
	for _, l := range cur.Labels {
		if l != vndb.Voted && !slices.Contains(next.Labels, l) {
			unset = append(unset, l)
		}
	}
	if len(set) > 0 {
		p["labels_set"] = set
	}
	if len(unset) > 0 {
		p["labels_unset"] = unset
	}
	opt := func(k, a, b string) {
		if a != b {
			if b == "" {
				p[k] = nil
			} else {
				p[k] = b
			}
		}
	}
	if cur.Vote != next.Vote {
		if next.Vote == 0 {
			p["vote"] = nil
		} else {
			p["vote"] = next.Vote
		}
	}
	opt("started", cur.Started, next.Started)
	opt("finished", cur.Finished, next.Finished)
	if cur.Notes != next.Notes {
		p["notes"] = next.Notes
	}
	return p
}

func describeVN(cur, next store.VNSnapshot, del bool, name func(int) string) string {
	if del {
		return "removed from list"
	}
	var parts []string
	if !cur.Exists {
		parts = append(parts, "added")
	}
	for _, l := range next.Labels {
		if l != vndb.Voted && !slices.Contains(cur.Labels, l) {
			parts = append(parts, "+"+name(l))
		}
	}
	for _, l := range cur.Labels {
		if l != vndb.Voted && !slices.Contains(next.Labels, l) {
			parts = append(parts, "−"+name(l))
		}
	}
	if cur.Vote != next.Vote {
		if next.Vote == 0 {
			parts = append(parts, "vote cleared")
		} else {
			parts = append(parts, fmt.Sprintf("vote %.1f", float64(next.Vote)/10))
		}
	}
	if cur.Started != next.Started {
		parts = append(parts, "started "+orDash(next.Started))
	}
	if cur.Finished != next.Finished {
		parts = append(parts, "finished "+orDash(next.Finished))
	}
	if cur.Notes != next.Notes {
		parts = append(parts, "notes")
	}
	return strings.Join(parts, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// vnUser returns the logged-in VNDB user id.
func (d *Daemon) vnUser() string {
	d.tokMu.Lock()
	defer d.tokMu.Unlock()
	return d.tok.VNDB.UserID
}

func (d *Daemon) processVNSet(ctx context.Context, it *store.Item) error {
	if !d.enabled(svcVNDB) {
		return errBlocked
	}
	if d.isAuthBroken(svcVNDB) {
		return &netx.Error{Kind: netx.Auth, Msg: "waiting for re-login"}
	}
	if it.Result == nil {
		it.Result = map[string]string{}
	}
	u, err := d.vndb.Entry(ctx, d.vnUser(), it.TargetVN)
	if err != nil {
		return d.vnErr(err)
	}
	cur := vnSnap(u)
	next, del, skip := ApplyVNEdit(cur, it.VNEdit, netx.Today())
	switch {
	case skip != "":
		it.Result[svcVNDB] = "skipped (" + skip + ")"
		return nil
	case del:
		err = d.vndb.Delete(ctx, it.TargetVN)
	default:
		err = d.vndb.Patch(ctx, it.TargetVN, vnPatch(cur, next))
	}
	if err != nil {
		return d.vnErr(err)
	}
	it.BeforeVN = &cur
	it.Result[svcVNDB] = describeVN(cur, next, del, d.vnLabelName)
	d.vnCacheSet(vnMetaOf(it), next, del, true)
	return nil
}

// undoVN restores a VNDB entry to its snapshot.
func (d *Daemon) undoVN(ctx context.Context, vid string, before store.VNSnapshot) error {
	if !d.enabled(svcVNDB) {
		return errVNDBOff()
	}
	u, err := d.vndb.Entry(ctx, d.vnUser(), vid)
	if err != nil {
		return err
	}
	cur := vnSnap(u)
	switch {
	case !before.Exists && cur.Exists:
		err = d.vndb.Delete(ctx, vid)
	case before.Exists:
		err = d.vndb.Patch(ctx, vid, vnPatch(cur, before))
	}
	if err == nil {
		d.vnCacheSet(vnlist.Entry{ID: vid}, before, !before.Exists, false)
	}
	return err
}
