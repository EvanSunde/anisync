// Package store holds all persistent daemon state in one JSON file, written
// atomically. The data is small (mappings, queue, history), so a database
// would be overkill.
package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Mapping ties a parsed title (or VN folder) to a database entry.
type Mapping struct {
	AnilistID int    `json:"anilist_id,omitempty"`
	MalID     int    `json:"mal_id,omitempty"`
	VNDBID    string `json:"vndb_id,omitempty"`
	Title     string `json:"title"`
	// Added to the parsed episode number before sequel resolution.
	Offset int `json:"offset,omitempty"`
}

type Candidate struct {
	AnilistID int     `json:"anilist_id,omitempty"`
	MalID     int     `json:"mal_id,omitempty"`
	VNDBID    string  `json:"vndb_id,omitempty"`
	Title     string  `json:"title"`
	Alt       string  `json:"alt,omitempty"`
	Format    string  `json:"format,omitempty"`
	Year      int     `json:"year,omitempty"`
	Episodes  int     `json:"episodes,omitempty"`
	InList    string  `json:"in_list,omitempty"` // list status if already in the user's list
	Score     float64 `json:"score"`
}

// Pending is a title that could not be matched automatically.
type Pending struct {
	Key        string      `json:"key"`
	Kind       string      `json:"kind"` // anime | vn
	Query      string      `json:"query"`
	Candidates []Candidate `json:"candidates"`
	Created    time.Time   `json:"created"`
	NotifyID   uint32      `json:"-"`
}

// Item is one queued unit of work (a watched episode, a VN state change).
type Item struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"` // anime | vn
	Key  string `json:"key"`  // mapping key
	// anime
	Title   string `json:"title,omitempty"`
	Season  int    `json:"season,omitempty"`
	Episode int    `json:"episode,omitempty"` // 0 = unknown (movie / single file)
	Source  string `json:"source,omitempty"`
	// vn
	Action string `json:"action,omitempty"` // start | finish | playtime
	Vote   int    `json:"vote,omitempty"`   // 10..100

	Stream bool `json:"stream,omitempty"` // from a network stream: never prompt

	// kind "set": a manual edit from the TUI. -1 = leave unchanged.
	SetStatus   string `json:"set_status,omitempty"`
	SetProgress int    `json:"set_progress,omitempty"`
	SetScore    int    `json:"set_score,omitempty"` // 0-100
	SetDelete   bool   `json:"set_delete,omitempty"`
	// Display data (a listcache.Entry, or a vnlist.Entry for kind "vnset")
	// so the local list can show an entry added while offline.
	Meta json.RawMessage `json:"meta,omitempty"`
	// kind "vnset": a manual VNDB list edit from the TUI.
	VNEdit *VNEdit `json:"vn_edit,omitempty"`

	// Filled in once resolved; kept so a half-applied update (AniList ok,
	// MAL offline) resumes where it stopped.
	TargetID    int                      `json:"target_id,omitempty"`
	TargetMal   int                      `json:"target_mal,omitempty"`
	TargetVN    string                   `json:"target_vn,omitempty"`
	TargetEp    int                      `json:"target_ep,omitempty"`
	TargetTotal int                      `json:"target_total,omitempty"`
	TargetTitle string                   `json:"target_title,omitempty"`
	Before      map[string]EntrySnapshot `json:"before,omitempty"`
	BeforeVN    *VNSnapshot              `json:"before_vn,omitempty"`
	Result      map[string]string        `json:"result,omitempty"`

	Created  time.Time       `json:"created"`
	Done     map[string]bool `json:"done,omitempty"` // per service
	Attempts int             `json:"attempts,omitempty"`
	LastErr  string          `json:"last_error,omitempty"`
}

// VNEdit is a change to a VNDB list entry. Nil fields stay unchanged.
type VNEdit struct {
	LabelsSet   []int   `json:"labels_set,omitempty"`
	LabelsUnset []int   `json:"labels_unset,omitempty"`
	Vote        *int    `json:"vote,omitempty"`     // 10-100; 0 clears
	Started     *string `json:"started,omitempty"`  // YYYY-MM-DD; "" clears
	Finished    *string `json:"finished,omitempty"` // YYYY-MM-DD; "" clears
	Notes       *string `json:"notes,omitempty"`
	Delete      bool    `json:"delete,omitempty"`
}

// VNSnapshot is a VNDB list entry (before an update, for undo).
type VNSnapshot struct {
	Exists   bool   `json:"exists"`
	Labels   []int  `json:"labels,omitempty"`
	Vote     int    `json:"vote,omitempty"`
	Started  string `json:"started,omitempty"`
	Finished string `json:"finished,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

// EntrySnapshot is a service list entry before an update, for undo.
type EntrySnapshot struct {
	Exists    bool   `json:"exists"`
	EntryID   int    `json:"entry_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Progress  int    `json:"progress"`
	Repeat    int    `json:"repeat,omitempty"`
	Started   string `json:"started,omitempty"`
	Completed string `json:"completed,omitempty"`
	Score     int    `json:"score,omitempty"` // 0-100
}

type HistoryEntry struct {
	ID        int64                    `json:"id"`
	Time      time.Time                `json:"time"`
	Kind      string                   `json:"kind"`
	Title     string                   `json:"title"`
	AnilistID int                      `json:"anilist_id,omitempty"`
	MalID     int                      `json:"mal_id,omitempty"`
	VNDBID    string                   `json:"vndb_id,omitempty"`
	Episode   int                      `json:"episode,omitempty"`
	Action    string                   `json:"action,omitempty"`
	Result    map[string]string        `json:"result"` // service -> "progress 4→5" / "skipped: ..."
	Before    map[string]EntrySnapshot `json:"before,omitempty"`
	BeforeVN  *VNSnapshot              `json:"before_vn,omitempty"`
	Undone    bool                     `json:"undone,omitempty"`
}

// Coverage records which parts of a file were actually played, in seconds.
type Coverage struct {
	Spans   [][2]float64 `json:"spans"`
	Length  float64      `json:"length"`
	Counted bool         `json:"counted"`
	Updated time.Time    `json:"updated"`
}

// Add merges [a,b] into the span set.
func (c *Coverage) Add(a, b float64) {
	if b <= a {
		return
	}
	spans := append(c.Spans, [2]float64{a, b})
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	out := spans[:0:0]
	for _, s := range spans {
		if n := len(out); n > 0 && s[0] <= out[n-1][1]+1 { // 1s slack joins adjacent spans
			if s[1] > out[n-1][1] {
				out[n-1][1] = s[1]
			}
			continue
		}
		out = append(out, s)
	}
	c.Spans = out
}

// Total returns covered seconds, optionally including a still-open span.
func (c *Coverage) Total(open ...[2]float64) float64 {
	tmp := Coverage{Spans: append([][2]float64(nil), c.Spans...)}
	for _, o := range open {
		tmp.Add(o[0], o[1])
	}
	var t float64
	for _, s := range tmp.Spans {
		t += s[1] - s[0]
	}
	return t
}

type MediaInfo struct {
	ID       int       `json:"id"`
	MalID    int       `json:"mal_id"`
	Title    string    `json:"title"`
	Format   string    `json:"format"`
	Status   string    `json:"status"`
	Episodes int       `json:"episodes"`
	Sequel   int       `json:"sequel,omitempty"` // next TV/ONA entry
	Fetched  time.Time `json:"fetched"`
}

type VNEntry struct {
	Key        string        `json:"key"`
	Dir        string        `json:"dir"`
	Playtime   time.Duration `json:"playtime"`
	LastPlayed time.Time     `json:"last_played"`
	Started    bool          `json:"started"` // "playing" already sent
}

type State struct {
	Mappings map[string]Mapping   `json:"mappings"`
	Ignored  map[string]string    `json:"ignored"` // key -> display title
	Pending  map[string]*Pending  `json:"pending"`
	Queue    []*Item              `json:"queue"`
	History  []*HistoryEntry      `json:"history"`
	Coverage map[string]*Coverage `json:"coverage"`
	Media    map[int]*MediaInfo   `json:"media"`
	VN       map[string]*VNEntry  `json:"vn"`
	NextID   int64                `json:"next_id"`
}

type Store struct {
	mu   sync.Mutex
	path string
	S    State
}

const maxHistory = 300

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	st := &Store{path: filepath.Join(dir, "state.json")}
	b, err := os.ReadFile(st.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &st.S); err != nil {
			// keep the broken file for inspection instead of silently losing it
			_ = os.Rename(st.path, st.path+".corrupt")
		}
	}
	s := &st.S
	if s.Mappings == nil {
		s.Mappings = map[string]Mapping{}
	}
	if s.Ignored == nil {
		s.Ignored = map[string]string{}
	}
	if s.Pending == nil {
		s.Pending = map[string]*Pending{}
	}
	if s.Coverage == nil {
		s.Coverage = map[string]*Coverage{}
	}
	if s.Media == nil {
		s.Media = map[int]*MediaInfo{}
	}
	if s.VN == nil {
		s.VN = map[string]*VNEntry{}
	}
	st.prune()
	return st, nil
}

func (st *Store) Lock()   { st.mu.Lock() }
func (st *Store) Unlock() { st.mu.Unlock() }

// NewID must be called with the lock held.
func (st *Store) NewID() int64 { st.S.NextID++; return st.S.NextID }

func (st *Store) prune() {
	cut := time.Now().Add(-30 * 24 * time.Hour)
	for k, c := range st.S.Coverage {
		if c.Updated.Before(cut) {
			delete(st.S.Coverage, k)
		}
	}
	if n := len(st.S.History); n > maxHistory {
		st.S.History = st.S.History[n-maxHistory:]
	}
}

// Save writes state atomically. Must be called with the lock held.
func (st *Store) Save() error {
	st.prune()
	b, err := json.Marshal(&st.S)
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}

// ---- tokens (separate file so state can be shared/inspected safely) ----

type Tokens struct {
	AniList struct {
		Token   string    `json:"token"`
		UserID  int       `json:"user_id"`
		User    string    `json:"user"`
		Expires time.Time `json:"expires"`
	} `json:"anilist"`
	MAL struct {
		Access  string    `json:"access"`
		Refresh string    `json:"refresh"`
		Expires time.Time `json:"expires"`
		User    string    `json:"user"`
	} `json:"mal"`
	VNDB struct {
		Token  string `json:"token"`
		UserID string `json:"user_id"`
		User   string `json:"user"`
	} `json:"vndb"`
}

func TokensPath(configDir string) string { return filepath.Join(configDir, "tokens.json") }

func LoadTokens(configDir string) (Tokens, error) {
	var t Tokens
	b, err := os.ReadFile(TokensPath(configDir))
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(b, &t)
}

func SaveTokens(configDir string, t Tokens) error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	p := TokensPath(configDir)
	if err := os.WriteFile(p+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}
