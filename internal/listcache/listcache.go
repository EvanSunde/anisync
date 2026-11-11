// Package listcache is the on-disk copy of your anime list. The daemon keeps
// it fresh and applies queued (offline) changes to it immediately, so the
// TUI can show your list offline and the daemon can match titles offline.
package listcache

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/config"
	"github.com/evansunde/anisync/internal/mal"
)

// Entry is one show: display data plus your list entry ("" Status = not on list).
type Entry struct {
	AnilistID  int      `json:"al,omitempty"`
	MalID      int      `json:"mal,omitempty"`
	Title      string   `json:"title"`
	English    string   `json:"en,omitempty"`
	Native     string   `json:"native,omitempty"`
	Synonyms   []string `json:"syn,omitempty"`
	Format     string   `json:"format,omitempty"`
	AirStatus  string   `json:"air,omitempty"`
	Season     string   `json:"season,omitempty"`
	Year       int      `json:"year,omitempty"`
	Episodes   int      `json:"eps,omitempty"`
	Duration   int      `json:"dur,omitempty"`
	Score      int      `json:"score,omitempty"` // average 0-100
	Popularity int      `json:"pop,omitempty"`
	Genres     []string `json:"genres,omitempty"`
	Cover      string   `json:"cover,omitempty"`
	Studio     string   `json:"studio,omitempty"`
	Rank       int      `json:"rank,omitempty"` // all-time rated rank
	NextEp     int      `json:"next_ep,omitempty"`
	NextAt     int64    `json:"next_at,omitempty"` // unix time

	Status   string `json:"status,omitempty"`
	Progress int    `json:"progress,omitempty"`
	MyScore  int    `json:"my_score,omitempty"` // 0-100
	Repeat   int    `json:"repeat,omitempty"`
	Updated  int64  `json:"updated,omitempty"`
	Pending  bool   `json:"pending,omitempty"` // changed locally, not synced yet
}

// Key is a stable id (also used as kitty image id): AniList id, or MAL id
// with bit 23 set.
func (e *Entry) Key() uint32 {
	if e.AnilistID != 0 {
		return uint32(e.AnilistID)
	}
	return uint32(e.MalID) | 1<<23
}

func (e *Entry) Titles() []string {
	out := []string{e.Title}
	for _, t := range append([]string{e.English, e.Native}, e.Synonyms...) {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Behind is how many aired episodes you haven't watched (airing shows).
func (e *Entry) Behind() int {
	if e.NextEp <= 1 || (e.Status != "CURRENT" && e.Status != "REPEATING") {
		return 0
	}
	return max(0, e.NextEp-1-e.Progress)
}

// FromMedia builds an entry from AniList data.
func FromMedia(m *anilist.Media) Entry {
	e := Entry{AnilistID: m.ID, MalID: m.IDMal, Title: m.Title.Best(), English: m.Title.English, Native: m.Title.Native,
		Synonyms: m.Synonyms, Format: m.Format, AirStatus: m.Status, Season: m.Season, Year: m.SeasonYear,
		Episodes: m.Episodes, Duration: m.Duration, Score: m.AverageScore, Popularity: m.Popularity,
		Genres: m.Genres, Cover: m.CoverImage.Large}
	if e.Year == 0 && m.StartDate.Year != nil {
		e.Year = *m.StartDate.Year
	}
	if len(m.Studios.Nodes) > 0 {
		e.Studio = m.Studios.Nodes[0].Name
	}
	for _, r := range m.Rankings {
		if r.AllTime && r.Type == "RATED" {
			e.Rank = r.Rank
			break
		}
	}
	if a := m.NextAiringEpisode; a != nil {
		e.NextEp, e.NextAt = a.Episode, a.AiringAt
		if e.NextAt == 0 && a.TimeUntilAiring > 0 {
			e.NextAt = time.Now().Unix() + int64(a.TimeUntilAiring)
		}
	}
	if l := m.MediaListEntry; l != nil {
		e.Status, e.Progress, e.MyScore, e.Repeat = l.Status, l.Progress, int(l.Score), l.Repeat
	}
	return e
}

func FromAniList(items []anilist.FullListItem) []*Entry {
	out := make([]*Entry, 0, len(items))
	for i := range items {
		it := &items[i]
		e := FromMedia(&it.Media)
		e.Status, e.Progress, e.MyScore, e.Repeat, e.Updated = it.Status, it.Progress, int(it.Score), it.Repeat, it.UpdatedAt
		out = append(out, &e)
	}
	return out
}

func FromMAL(items []mal.ListEntry) []*Entry {
	out := make([]*Entry, 0, len(items))
	for _, m := range items {
		out = append(out, &Entry{MalID: m.ID, Title: m.Title, English: m.English, Format: strings.ToUpper(m.MediaType),
			AirStatus: m.AirStatus, Year: m.Year, Episodes: m.Episodes, Score: int(m.Mean * 10), Genres: m.Genres,
			Cover: m.Picture, Status: m.Status, Progress: m.Progress, MyScore: m.Score})
	}
	return out
}

type List struct {
	Fetched time.Time `json:"fetched"` // last successful sync with the server
	Source  string    `json:"source"`  // anilist | mal
	Entries []*Entry  `json:"entries"`
}

func Path() string { return filepath.Join(config.DataDir(), "list.json") }

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

// Find returns the entry for a show (by AniList or MAL id), or nil.
func (l *List) Find(anilistID, malID int) *Entry {
	for _, e := range l.Entries {
		if (anilistID != 0 && e.AnilistID == anilistID) || (malID != 0 && e.MalID == malID) {
			return e
		}
	}
	return nil
}

// Remove drops a show from the list.
func (l *List) Remove(anilistID, malID int) {
	out := l.Entries[:0]
	for _, e := range l.Entries {
		if (anilistID != 0 && e.AnilistID == anilistID) || (malID != 0 && e.MalID == malID) {
			continue
		}
		out = append(out, e)
	}
	l.Entries = out
}

// Upsert finds the show or adds it using meta for display fields.
func (l *List) Upsert(meta Entry) *Entry {
	if e := l.Find(meta.AnilistID, meta.MalID); e != nil {
		if e.AnilistID == 0 {
			e.AnilistID = meta.AnilistID
		}
		if e.MalID == 0 {
			e.MalID = meta.MalID
		}
		return e
	}
	e := meta
	l.Entries = append(l.Entries, &e)
	return &e
}
