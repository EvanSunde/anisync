package daemon

import (
	"testing"

	"github.com/evansunde/anisync/internal/store"
)

func TestPlan(t *testing.T) {
	const d = "2026-11-10"
	none := store.EntrySnapshot{}
	cases := []struct {
		name       string
		cur        store.EntrySnapshot
		total, ep  int
		add, rw    bool
		wantStatus string
		wantProg   int
		wantSkip   bool
	}{
		{"add missing", none, 12, 1, true, false, "CURRENT", 1, false},
		{"no add", none, 12, 1, false, false, "", 0, true},
		{"single ep movie completes", none, 1, 1, true, false, "COMPLETED", 1, false},
		{"planning -> current", store.EntrySnapshot{Exists: true, Status: "PLANNING"}, 12, 1, true, false, "CURRENT", 1, false},
		{"progress", store.EntrySnapshot{Exists: true, Status: "CURRENT", Progress: 4}, 12, 5, true, false, "CURRENT", 5, false},
		{"rewatch of old ep", store.EntrySnapshot{Exists: true, Status: "CURRENT", Progress: 6}, 12, 5, true, false, "", 0, true},
		{"finale completes", store.EntrySnapshot{Exists: true, Status: "CURRENT", Progress: 11}, 12, 12, true, false, "COMPLETED", 12, false},
		{"airing unknown total", store.EntrySnapshot{Exists: true, Status: "CURRENT", Progress: 1100}, 0, 1101, true, false, "CURRENT", 1101, false},
		{"completed untouched", store.EntrySnapshot{Exists: true, Status: "COMPLETED", Progress: 12}, 12, 1, true, false, "", 0, true},
		{"rewatch starts", store.EntrySnapshot{Exists: true, Status: "COMPLETED", Progress: 12}, 12, 1, true, true, "REPEATING", 1, false},
		{"rewatch finishes", store.EntrySnapshot{Exists: true, Status: "REPEATING", Progress: 11}, 12, 12, true, true, "COMPLETED", 12, false},
		{"dropped resumes", store.EntrySnapshot{Exists: true, Status: "DROPPED", Progress: 3}, 12, 4, true, false, "CURRENT", 4, false},
	}
	for _, c := range cases {
		next, skip := plan(c.cur, c.total, c.ep, c.add, c.rw, d)
		if (skip != "") != c.wantSkip {
			t.Errorf("%s: skip=%q", c.name, skip)
			continue
		}
		if c.wantSkip {
			continue
		}
		if next.Status != c.wantStatus || next.Progress != c.wantProg {
			t.Errorf("%s: got %s/%d", c.name, next.Status, next.Progress)
		}
	}
	n, _ := plan(store.EntrySnapshot{Exists: true, Status: "REPEATING", Progress: 11, Repeat: 1}, 12, 12, true, true, d)
	if n.Repeat != 2 {
		t.Errorf("repeat count not incremented")
	}
	n, _ = plan(none, 12, 3, true, false, d)
	if n.Started != d {
		t.Errorf("start date not set")
	}
}
