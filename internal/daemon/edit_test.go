package daemon

import (
	"testing"

	"github.com/evansunde/anisync/internal/store"
)

func TestApplySet(t *testing.T) {
	const d = "2026-11-10"
	it := func(status string, prog, score int, del bool) *store.Item {
		return &store.Item{SetStatus: status, SetProgress: prog, SetScore: score, SetDelete: del}
	}
	none := store.EntrySnapshot{}
	cur := store.EntrySnapshot{Exists: true, Status: "CURRENT", Progress: 3, Score: 70, Started: "2026-01-01"}

	n, _, skip := applySet(none, 12, it("", -1, -1, false), d)
	if skip != "" || n.Status != "PLANNING" || !n.Exists {
		t.Fatalf("add to planning: %+v %q", n, skip)
	}
	n, _, _ = applySet(none, 12, it("CURRENT", -1, -1, false), d)
	if n.Status != "CURRENT" || n.Started != d {
		t.Fatalf("add watching: %+v", n)
	}
	n, _, _ = applySet(cur, 12, it("", 4, -1, false), d)
	if n.Progress != 4 || n.Status != "CURRENT" || n.Score != 70 {
		t.Fatalf("progress keeps score: %+v", n)
	}
	n, _, _ = applySet(cur, 12, it("", 12, -1, false), d)
	if n.Status != "COMPLETED" || n.Completed != d {
		t.Fatalf("progress to end completes: %+v", n)
	}
	n, _, _ = applySet(cur, 12, it("COMPLETED", -1, -1, false), d)
	if n.Progress != 12 {
		t.Fatalf("mark completed fills progress: %+v", n)
	}
	n, _, _ = applySet(cur, 12, it("", -1, 85, false), d)
	if n.Score != 85 || n.Progress != 3 {
		t.Fatalf("score only: %+v", n)
	}
	n, _, _ = applySet(store.EntrySnapshot{Exists: true, Status: "PLANNING"}, 12, it("", 1, -1, false), d)
	if n.Status != "CURRENT" {
		t.Fatalf("planning + progress → watching: %+v", n)
	}
	n, _, _ = applySet(cur, 0, it("", 99, -1, false), d)
	if n.Progress != 99 || n.Status != "CURRENT" {
		t.Fatalf("unknown total: %+v", n)
	}
	n, _, _ = applySet(store.EntrySnapshot{Exists: true, Status: "COMPLETED", Progress: 12}, 12, it("REPEATING", -1, -1, false), d)
	if n.Status != "REPEATING" || n.Progress != 0 {
		t.Fatalf("rewatch resets progress: %+v", n)
	}
	if _, del, _ := applySet(cur, 12, it("", -1, -1, true), d); !del {
		t.Fatal("delete")
	}
	if _, _, skip := applySet(none, 12, it("", -1, -1, true), d); skip == "" {
		t.Fatal("delete missing should skip")
	}
	if _, _, skip := applySet(cur, 12, it("CURRENT", -1, -1, false), d); skip != "no change" {
		t.Fatalf("no-op should skip, got %q", skip)
	}
}
