package daemon

import (
	"reflect"
	"testing"

	"github.com/evansunde/anisync/internal/store"
	"github.com/evansunde/anisync/internal/vndb"
)

func sp(s string) *string { return &s }
func ip(i int) *int       { return &i }

func TestApplyVNEdit(t *testing.T) {
	const d = "2026-11-10"
	none := store.VNSnapshot{}
	cur := store.VNSnapshot{Exists: true, Labels: []int{vndb.Wishlist, 12}, Notes: "Playtime: 1h 00m"}

	n, _, skip := ApplyVNEdit(none, &store.VNEdit{LabelsSet: []int{vndb.Playing}}, d)
	if skip != "" || !n.Exists || !reflect.DeepEqual(n.Labels, []int{1}) || n.Started != d {
		t.Fatalf("add as playing: %+v %q", n, skip)
	}
	// status change keeps custom labels and notes
	n, _, _ = ApplyVNEdit(cur, &store.VNEdit{LabelsSet: []int{vndb.Finished}, LabelsUnset: []int{1, 3, 4, 5}}, d)
	if !reflect.DeepEqual(n.Labels, []int{2, 12}) || n.Finished != d || n.Started != "" || n.Notes != cur.Notes {
		t.Fatalf("finish: %+v", n)
	}
	// explicit dates win over the automatic ones
	n, _, _ = ApplyVNEdit(cur, &store.VNEdit{LabelsSet: []int{vndb.Finished}, Finished: sp("2020-01-02")}, d)
	if n.Finished != "2020-01-02" {
		t.Fatalf("explicit date: %+v", n)
	}
	// vote adds the Voted label; clearing removes it
	n, _, _ = ApplyVNEdit(cur, &store.VNEdit{Vote: ip(85)}, d)
	if n.Vote != 85 || !reflect.DeepEqual(n.Labels, []int{5, 7, 12}) {
		t.Fatalf("vote: %+v", n)
	}
	n2, _, _ := ApplyVNEdit(n, &store.VNEdit{Vote: ip(0)}, d)
	if n2.Vote != 0 || !reflect.DeepEqual(n2.Labels, []int{5, 12}) {
		t.Fatalf("clear vote: %+v", n2)
	}
	if _, _, skip := ApplyVNEdit(cur, &store.VNEdit{LabelsSet: []int{vndb.Wishlist}}, d); skip != "no change" {
		t.Fatalf("no-op should skip, got %q", skip)
	}
	if _, del, _ := ApplyVNEdit(cur, &store.VNEdit{Delete: true}, d); !del {
		t.Fatal("delete")
	}
	if _, _, skip := ApplyVNEdit(none, &store.VNEdit{Delete: true}, d); skip == "" {
		t.Fatal("deleting a missing entry should skip")
	}
	// clear a date
	n, _, _ = ApplyVNEdit(store.VNSnapshot{Exists: true, Started: "2020-01-01"}, &store.VNEdit{Started: sp("")}, d)
	if n.Started != "" {
		t.Fatalf("clear date: %+v", n)
	}
}

func TestVNPatch(t *testing.T) {
	cur := store.VNSnapshot{Exists: true, Labels: []int{1, 7, 12}, Vote: 70, Started: "2025-01-01", Notes: "a"}
	next := store.VNSnapshot{Exists: true, Labels: []int{2, 12}, Vote: 0, Started: "2025-01-01", Finished: "2026-11-10", Notes: "a"}
	got := vnPatch(cur, next)
	want := map[string]any{"labels_set": []int{2}, "labels_unset": []int{1}, "vote": nil, "finished": "2026-11-10"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("patch\n got  %#v\n want %#v", got, want)
	}
	// undo direction restores everything
	back := vnPatch(next, cur)
	if !reflect.DeepEqual(back, map[string]any{"labels_set": []int{1}, "labels_unset": []int{2}, "vote": 70, "finished": nil}) {
		t.Fatalf("reverse patch %#v", back)
	}
}

func TestVNActionEdit(t *testing.T) {
	const d = "2026-11-10"
	if e, why := vnActionEdit(&store.Item{Action: "start"}, store.VNSnapshot{Exists: true, Labels: []int{2}}, d); e != nil || why == "" {
		t.Fatal("start on a finished VN should be skipped")
	}
	e, _ := vnActionEdit(&store.Item{Action: "finish", Vote: 85}, store.VNSnapshot{Exists: true, Labels: []int{1}}, d)
	n, _, _ := ApplyVNEdit(store.VNSnapshot{Exists: true, Labels: []int{1}}, e, d)
	if !reflect.DeepEqual(n.Labels, []int{2, 7}) || n.Vote != 85 || n.Started != d || n.Finished != d {
		t.Fatalf("finish: %+v", n)
	}
}
