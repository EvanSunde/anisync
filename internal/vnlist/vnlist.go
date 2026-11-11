// Package vnlist is the on-disk copy of your VNDB list (vnlist.json). Like
// listcache for anime, the daemon owns it: it refreshes it from VNDB and
// applies queued edits right away (Pending), so the TUI works offline.
package vnlist

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/evansunde/anisync/internal/config"
	"github.com/evansunde/anisync/internal/vndb"
)

// Entry is one VN: display data plus your list entry (OnList false = not on
// your list, e.g. a search result).
type Entry struct {
	ID        string   `json:"id"` // v123
	Title     string   `json:"title"`
	Alt       string   `json:"alt,omitempty"`
	Released  string   `json:"released,omitempty"`
	Cover     string   `json:"cover,omitempty"`
	Sexual    float64  `json:"sexual,omitempty"`
	Violence  float64  `json:"violence,omitempty"`
	Rating    float64  `json:"rating,omitempty"` // 10-100
	Votes     int      `json:"votes,omitempty"`
	Length    int      `json:"length,omitempty"` // 1-5
	LengthMin int      `json:"length_min,omitempty"`
	Devs      []string `json:"devs,omitempty"`

	OnList   bool   `json:"on_list,omitempty"`
	Labels   []int  `json:"labels,omitempty"`
	Vote     int    `json:"vote,omitempty"` // 10-100, 0 = none
	Started  string `json:"started,omitempty"`
	Finished string `json:"finished,omitempty"`
	Notes    string `json:"notes,omitempty"`
	Added    int64  `json:"added,omitempty"`
	Updated  int64  `json:"updated,omitempty"`
	Pending  bool   `json:"pending,omitempty"`  // changed locally, not synced yet
	Playtime int64  `json:"playtime,omitempty"` // seconds tracked by the daemon
}

// Key is a stable id, also used as the kitty image id: the VN number with
// bit 22 set (AniList ids stay below it; MAL ids use bit 23).
func (e *Entry) Key() uint32 { return uint32(vndb.Num(e.ID)) | 1<<22 }

func (e *Entry) Has(label int) bool {
	for _, l := range e.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// Status is the first built-in status label (Playing…Wishlist), or 0.
func (e *Entry) Status() int {
	for _, s := range []int{vndb.Playing, vndb.Finished, vndb.Stalled, vndb.Dropped, vndb.Wishlist} {
		if e.Has(s) {
			return s
		}
	}
	return 0
}

// Year is the release year, or 0.
func (e *Entry) Year() int {
	if len(e.Released) >= 4 {
		y := 0
		for _, c := range e.Released[:4] {
			if c < '0' || c > '9' {
				return 0
			}
			y = y*10 + int(c-'0')
		}
		return y
	}
	return 0
}

// Explicit reports whether the cover should be hidden in "safe" mode.
func (e *Entry) Explicit() bool { return e.Sexual >= 1 || e.Violence >= 2 }

// FromVN builds an entry (display data only) from VNDB data.
func FromVN(v *vndb.VN) Entry {
	e := Entry{ID: v.ID, Title: v.Title, Alt: v.AltTitle, Released: v.Released, Rating: v.Rating, Votes: v.VoteCount,
		Length: v.Length, LengthMin: v.LengthMinutes}
	if v.Image != nil {
		e.Cover, e.Sexual, e.Violence = v.Image.URL, v.Image.Sexual, v.Image.Violence
	}
	for _, d := range v.Developers {
		e.Devs = append(e.Devs, d.Name)
	}
	return e
}

func FromUList(items []vndb.ListItem) []*Entry {
	out := make([]*Entry, 0, len(items))
	for i := range items {
		it := &items[i]
		it.VN.ID = it.ID
		e := FromVN(&it.VN)
		e.OnList, e.Added, e.Updated = true, it.Added, it.LastMod
		for _, l := range it.Labels {
			e.Labels = append(e.Labels, l.ID)
		}
		sort.Ints(e.Labels)
		if it.Vote != nil {
			e.Vote = *it.Vote
		}
		e.Started, e.Finished, e.Notes = deref(it.Started), deref(it.Finished), deref(it.Notes)
		out = append(out, &e)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type List struct {
	Fetched time.Time    `json:"fetched"` // last successful sync with VNDB
	User    string       `json:"user,omitempty"`
	Labels  []vndb.Label `json:"labels"`
	Entries []*Entry     `json:"entries"`
}

func Path() string { return filepath.Join(config.DataDir(), "vnlist.json") }

// Load reads the cached list (empty if there is none yet).
func Load() (*List, error) {
	l := &List{}
	b, err := os.ReadFile(Path())
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	return l, json.Unmarshal(b, l)
}

func (l *List) Save() error {
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

func (l *List) Find(id string) *Entry {
	for _, e := range l.Entries {
		if e.ID == id {
			return e
		}
	}
	return nil
}

func (l *List) Remove(id string) {
	out := l.Entries[:0]
	for _, e := range l.Entries {
		if e.ID != id {
			out = append(out, e)
		}
	}
	l.Entries = out
}

// Upsert finds the VN or adds it using meta for display fields.
func (l *List) Upsert(meta Entry) *Entry {
	if e := l.Find(meta.ID); e != nil {
		if e.Title == "" {
			e.Title = meta.Title
		}
		return e
	}
	e := meta
	l.Entries = append(l.Entries, &e)
	return &e
}

// LabelName returns a label's name (built-in names if the list has none).
func (l *List) LabelName(id int) string {
	for _, x := range l.Labels {
		if x.ID == id {
			return x.Label
		}
	}
	return DefaultLabels[id]
}

var DefaultLabels = map[int]string{vndb.Playing: "Playing", vndb.Finished: "Finished", vndb.Stalled: "Stalled",
	vndb.Dropped: "Dropped", vndb.Wishlist: "Wishlist", vndb.Blacklist: "Blacklist", vndb.Voted: "Voted"}

// Titles returns all searchable names (for filtering).
func (e *Entry) Titles() string {
	return strings.Join(append([]string{e.Title, e.Alt}, e.Devs...), " ")
}
