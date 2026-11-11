package daemon

import (
	"fmt"

	"github.com/evansunde/anisync/internal/store"
)

// plan decides the new list entry after watching episode ep. Statuses use
// AniList's vocabulary (MAL is mapped in its client). A non-empty skip
// reason means "leave the entry alone".
func plan(cur store.EntrySnapshot, total, ep int, addMissing, rewatch bool, today string) (next store.EntrySnapshot, skip string) {
	complete := func(e *store.EntrySnapshot) {
		if total > 0 && e.Progress >= total {
			e.Progress = total
			e.Status = "COMPLETED"
			if e.Completed == "" {
				e.Completed = today
			}
		}
	}
	if !cur.Exists {
		if !addMissing {
			return cur, "not in list"
		}
		next = store.EntrySnapshot{Exists: true, Status: "CURRENT", Progress: ep, Started: today}
		complete(&next)
		return next, ""
	}
	next = cur
	switch cur.Status {
	case "COMPLETED":
		if rewatch && ep == 1 && total != 1 {
			next.Status, next.Progress = "REPEATING", 1
			return next, ""
		}
		return cur, "already completed"
	case "REPEATING":
		if ep <= cur.Progress {
			return cur, fmt.Sprintf("already at %d", cur.Progress)
		}
		next.Progress = ep
		if total > 0 && ep >= total {
			next.Progress, next.Status = total, "COMPLETED"
			next.Repeat++
		}
		return next, ""
	default: // CURRENT, PLANNING, PAUSED, DROPPED
		if ep <= cur.Progress {
			return cur, fmt.Sprintf("already at %d", cur.Progress)
		}
		next.Progress, next.Status = ep, "CURRENT"
		if next.Started == "" {
			next.Started = today
		}
		complete(&next)
		return next, ""
	}
}
