package daemon

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/listcache"
	"github.com/evansunde/anisync/internal/media"
	"github.com/evansunde/anisync/internal/store"
)

// activeStatus: list entries that are strong evidence the user is watching it.
var activeStatus = map[string]bool{"CURRENT": true, "REPEATING": true, "PLANNING": true, "PAUSED": true}

func (d *Daemon) refreshList(ctx context.Context) error {
	var entries []*listcache.Entry
	var source string
	switch {
	case d.enabled(svcAniList):
		d.tokMu.Lock()
		uid := d.tok.AniList.UserID
		d.tokMu.Unlock()
		items, err := d.al.ListFull(ctx, uid)
		if err != nil {
			return err
		}
		entries, source = listcache.FromAniList(items), "anilist"
	case d.enabled(svcMAL):
		items, err := d.mal.List(ctx)
		if err != nil {
			return err
		}
		entries, source = listcache.FromMAL(items), "mal"
	default:
		return nil
	}
	d.stateMu.Lock()
	d.cache = &listcache.List{Fetched: time.Now(), Source: source, Entries: entries}
	d.listFetched = time.Now()
	err := d.cache.Save()
	d.stateMu.Unlock()
	if err != nil {
		log.Printf("list cache: %v", err)
	}
	d.overlayQueue() // keep offline changes that aren't on the server yet
	log.Printf("list refreshed (%d entries)", len(entries))
	return nil
}

// listStatus returns the user's list status for a show.
func (d *Daemon) listStatus(id, malID int) string {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.cache == nil {
		return ""
	}
	if e := d.cache.Find(id, malID); e != nil {
		return e.Status
	}
	return ""
}

// resolveAnime returns the mapping for an item, auto-matching if possible.
func (d *Daemon) resolveAnime(ctx context.Context, it *store.Item) (store.Mapping, error) {
	d.st.Lock()
	m, ok := d.st.S.Mappings[it.Key]
	_, pending := d.st.S.Pending[it.Key]
	_, ignored := d.st.S.Ignored[it.Key]
	d.st.Unlock()
	if ignored {
		return m, errDrop
	}
	if ok {
		return m, nil
	}
	if pending {
		return m, errBlocked
	}

	var cands []anilist.Media
	if it.Season > 1 {
		// Find the entry whose title says it is season N. Following the sequel
		// chain is wrong for split-cour shows (S1 "Part 2" is the first sequel).
		var err error
		if cands, err = d.al.Search(ctx, fmt.Sprintf("%s season %d", it.Title, it.Season)); err != nil {
			return m, err
		}
		more, err := d.al.Search(ctx, it.Title)
		if err != nil {
			return m, err
		}
		cands = mergeMedia(cands, more)
		if pick := seasonPick(it.Title, it.Season, cands); pick != nil {
			m = store.Mapping{AnilistID: pick.ID, MalID: pick.IDMal, Title: pick.Title.Best()}
			d.saveMapping(it.Key, m)
			log.Printf("matched %q season %d → %s (%d)", it.Title, it.Season, m.Title, m.AnilistID)
			return m, nil
		}
	} else {
		var err error
		if cands, err = d.al.Search(ctx, it.Title); err != nil {
			return m, err
		}
		if pick := d.autoPick(it.Title, it.Episode, cands); pick != nil {
			m = store.Mapping{AnilistID: pick.ID, MalID: pick.IDMal, Title: pick.Title.Best()}
			d.saveMapping(it.Key, m)
			log.Printf("matched %q → %s (%d)", it.Title, m.Title, m.AnilistID)
			return m, nil
		}
	}
	if it.Stream {
		log.Printf("no confident match for stream %q; ignoring", it.Title)
		return m, errDrop
	}
	d.addPending(it, d.candidates(it.Title, cands))
	return m, errBlocked
}

// autoPick only accepts unambiguous matches; anything else goes to the user.
func (d *Daemon) autoPick(title string, episode int, cands []anilist.Media) *anilist.Media {
	n := media.MatchForm(title)
	if n == "" {
		return nil
	}
	// 1. exact title match against the user's own list
	d.stateMu.Lock()
	var inList []int
	if d.cache != nil {
		for _, e := range d.cache.Entries {
			if e.AnilistID == 0 || e.Status == "" {
				continue
			}
			for _, t := range e.Titles() {
				if media.MatchForm(t) == n {
					inList = append(inList, e.AnilistID)
					break
				}
			}
		}
	}
	d.stateMu.Unlock()
	if len(inList) == 1 {
		for i := range cands {
			if cands[i].ID == inList[0] {
				return &cands[i]
			}
		}
	}
	// 2. exact title match among search results
	var exact []*anilist.Media
	for i := range cands {
		if cands[i].Status == "NOT_YET_RELEASED" { // can't have a file of an unaired show
			continue
		}
		if f := cands[i].Format; episode > 1 && (f == "MOVIE" || f == "MUSIC") {
			continue
		}
		for _, t := range cands[i].Titles() {
			if media.MatchForm(t) == n {
				exact = append(exact, &cands[i])
				break
			}
		}
	}
	if len(exact) == 1 {
		return exact[0]
	}
	if len(exact) > 1 { // remakes etc.: only if exactly one is on the user's list as active
		var active []*anilist.Media
		for _, c := range exact {
			if activeStatus[d.listStatus(c.ID, c.IDMal)] {
				active = append(active, c)
			}
		}
		if len(active) == 1 {
			return active[0]
		}
		return nil
	}
	// 3. near-exact match that the user is actively watching
	var best *anilist.Media
	bestScore, second := 0.0, 0.0
	for i := range cands {
		if !activeStatus[d.listStatus(cands[i].ID, cands[i].IDMal)] {
			continue
		}
		s := bestSim(title, cands[i].Titles())
		if s > bestScore {
			best, second, bestScore = &cands[i], bestScore, s
		} else if s > second {
			second = s
		}
	}
	if best != nil && bestScore >= 0.9 && bestScore-second >= 0.1 {
		return best
	}
	return nil
}

func bestSim(q string, titles []string) float64 {
	b := 0.0
	for _, t := range titles {
		if s := media.Similarity(q, t); s > b {
			b = s
		}
	}
	return b
}

func (d *Daemon) candidates(title string, cands []anilist.Media) []store.Candidate {
	var out []store.Candidate
	for _, c := range cands {
		cd := store.Candidate{AnilistID: c.ID, MalID: c.IDMal, Title: c.Title.Best(), Format: c.Format,
			Year: c.SeasonYear, Episodes: c.Episodes, Score: bestSim(title, c.Titles())}
		if c.Title.English != "" && c.Title.English != cd.Title {
			cd.Alt = c.Title.English
		}
		cd.InList = d.listStatus(c.ID, c.IDMal)
		if cd.InList == "" && c.MediaListEntry != nil {
			cd.InList = c.MediaListEntry.Status
		}
		out = append(out, cd)
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) > rank(out[j]) })
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func rank(c store.Candidate) float64 {
	r := c.Score
	if activeStatus[c.InList] {
		r += 0.2
	} else if c.InList != "" {
		r += 0.05
	}
	return r
}

func (d *Daemon) saveMapping(key string, m store.Mapping) {
	d.st.Lock()
	defer d.st.Unlock()
	d.st.S.Mappings[key] = m
	_ = d.st.Save()
}

func (d *Daemon) addPending(it *store.Item, cands []store.Candidate) {
	p := &store.Pending{Key: it.Key, Kind: it.Kind, Query: it.Title, Candidates: cands, Created: time.Now()}
	if it.Kind == "anime" && it.Season > 1 {
		p.Query = fmt.Sprintf("%s (season %d)", it.Title, it.Season)
	}
	d.st.Lock()
	d.st.S.Pending[it.Key] = p
	_ = d.st.Save()
	d.st.Unlock()
	d.notifyPending(p, it)
}

func (d *Daemon) notifyPending(p *store.Pending, it *store.Item) {
	var actions []string
	for i, c := range p.Candidates {
		if i == 3 {
			break
		}
		label := c.Title
		if c.Year > 0 {
			label += fmt.Sprintf(" (%d)", c.Year)
		}
		if c.InList != "" {
			label += " ✓"
		}
		actions = append(actions, "pick:"+strconv.Itoa(i), label)
	}
	actions = append(actions, "ignore", "Ignore this title")
	what := p.Query
	if it != nil && it.Kind == "anime" && it.Episode > 0 {
		what += fmt.Sprintf(" — episode %d", it.Episode)
	}
	key := p.Key
	cands := p.Candidates
	id := d.notif.Send("Which one is this?", what+"\nPick a match below or run: anisync resolve", actions, func(action string) {
		var err error
		switch {
		case action == "ignore":
			err = d.Resolve(ResolveArgs{Key: key, Ignore: true})
		case strings.HasPrefix(action, "pick:"):
			i, _ := strconv.Atoi(strings.TrimPrefix(action, "pick:"))
			if i < len(cands) {
				c := cands[i]
				err = d.Resolve(ResolveArgs{Key: key, AnilistID: c.AnilistID, MalID: c.MalID, VNDBID: c.VNDBID, Title: c.Title})
			}
		}
		if err != nil {
			log.Printf("resolve from notification: %v", err)
		}
	})
	d.st.Lock()
	if pp := d.st.S.Pending[key]; pp != nil {
		pp.NotifyID = id
	}
	d.st.Unlock()
}

type ResolveArgs struct {
	Key       string `json:"key"`
	AnilistID int    `json:"anilist_id,omitempty"`
	MalID     int    `json:"mal_id,omitempty"`
	VNDBID    string `json:"vndb_id,omitempty"`
	Title     string `json:"title,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	Ignore    bool   `json:"ignore,omitempty"`
}

// Resolve applies a user's choice for a pending title.
func (d *Daemon) Resolve(a ResolveArgs) error {
	d.st.Lock()
	p := d.st.S.Pending[a.Key]
	if p == nil {
		if _, ok := d.st.S.Mappings[a.Key]; !ok {
			d.st.Unlock()
			return fmt.Errorf("nothing pending for %q", a.Key)
		}
	}
	var nid uint32
	if p != nil {
		nid = p.NotifyID
	}
	delete(d.st.S.Pending, a.Key)
	if a.Ignore {
		title := a.Key
		if p != nil {
			title = p.Query
		}
		d.st.S.Ignored[a.Key] = title
		q := d.st.S.Queue[:0]
		for _, it := range d.st.S.Queue {
			if it.Key != a.Key {
				q = append(q, it)
			}
		}
		d.st.S.Queue = q
	} else {
		if a.AnilistID == 0 && a.VNDBID == "" {
			d.st.Unlock()
			return fmt.Errorf("no id given")
		}
		d.st.S.Mappings[a.Key] = store.Mapping{AnilistID: a.AnilistID, MalID: a.MalID, VNDBID: a.VNDBID, Title: a.Title, Offset: a.Offset}
		// pending items are re-resolved through the new mapping
		for _, it := range d.st.S.Queue {
			if it.Key == a.Key {
				it.TargetID, it.TargetVN = 0, ""
			}
		}
	}
	err := d.st.Save()
	d.st.Unlock()
	d.notif.Close(nid)
	if a.Ignore {
		log.Printf("ignoring %q from now on", a.Key)
	} else {
		log.Printf("mapped %q → %s", a.Key, a.Title)
	}
	d.Kick()
	return err
}

var reLaterPart = regexp.MustCompile(`\bpart ([2-9]|ii|iii|iv)\b|\bcour ([2-9])\b`)

var roman = []string{"", "i", "ii", "iii", "iv", "v", "vi", "vii", "viii", "ix", "x"}

func ordinal(n int) string {
	suf := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suf = "st"
	case n%10 == 2:
		suf = "nd"
	case n%10 == 3:
		suf = "rd"
	}
	return strconv.Itoa(n) + suf
}

// seasonPick returns the single entry titled "<title> Season N",
// "<title> Nth Season", "<title> N" or "<title> <roman N>". An exact title
// wins; otherwise a marker followed by a subtitle ("II: Isekai Ittara...").
func seasonPick(title string, n int, cands []anilist.Media) *anilist.Media {
	base := media.MatchForm(title)
	ns := strconv.Itoa(n)
	markers := []string{"season " + ns, ordinal(n) + " season", ns}
	if n < len(roman) {
		markers = append(markers, roman[n])
	}
	var exact, prefixed []*anilist.Media
	for i := range cands {
		c := &cands[i]
		if c.Status == "NOT_YET_RELEASED" || (c.Format != "TV" && c.Format != "TV_SHORT" && c.Format != "ONA") {
			continue
		}
		later := false
		for _, t := range c.Titles() {
			later = later || reLaterPart.MatchString(media.MatchForm(t))
		}
		if later { // a later cour ("Part 2") of that season: reached via episode overflow
			continue
		}
		isExact, isPrefixed := false, false
		for _, t := range c.Titles() {
			mt := media.MatchForm(t)
			if !strings.HasPrefix(mt, base+" ") {
				continue
			}
			rest := strings.TrimSpace(mt[len(base):])
			for _, w := range markers {
				if rest == w || rest == w+" part 1" {
					isExact = true
				} else if strings.HasPrefix(rest, w+" ") {
					isPrefixed = true
				}
			}
		}
		if isExact {
			exact = append(exact, c)
		} else if isPrefixed {
			prefixed = append(prefixed, c)
		}
	}
	if len(exact) == 1 {
		return exact[0]
	}
	if len(exact) == 0 && len(prefixed) == 1 {
		return prefixed[0]
	}
	return nil
}

func mergeMedia(a, b []anilist.Media) []anilist.Media {
	seen := map[int]bool{}
	for _, m := range a {
		seen[m.ID] = true
	}
	for _, m := range b {
		if !seen[m.ID] {
			a = append(a, m)
			seen[m.ID] = true
		}
	}
	return a
}
