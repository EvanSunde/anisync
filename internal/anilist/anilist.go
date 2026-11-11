// Package anilist is a minimal AniList GraphQL client: search, list entries,
// sequel relations, and progress updates.
package anilist

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/evansunde/anisync/internal/netx"
	"github.com/evansunde/anisync/internal/store"
)

// Endpoint is a variable so tests can point it at a mock server.
var Endpoint = "https://graphql.anilist.co"

func AuthURL(clientID string) string {
	return "https://anilist.co/api/v2/oauth/authorize?client_id=" + clientID + "&response_type=token"
}

type Client struct{ Token string }

type Title struct {
	Romaji  string `json:"romaji"`
	English string `json:"english"`
	Native  string `json:"native"`
}

func (t Title) Best() string {
	if t.Romaji != "" {
		return t.Romaji
	}
	if t.English != "" {
		return t.English
	}
	return t.Native
}

type FuzzyDate struct {
	Year  *int `json:"year"`
	Month *int `json:"month"`
	Day   *int `json:"day"`
}

func (d FuzzyDate) String() string {
	if d.Year == nil {
		return ""
	}
	m, dd := 1, 1
	if d.Month != nil {
		m = *d.Month
	}
	if d.Day != nil {
		dd = *d.Day
	}
	return fmt.Sprintf("%04d-%02d-%02d", *d.Year, m, dd)
}

func dateInput(s string) map[string]any {
	var y, m, d int
	if _, err := fmt.Sscanf(s, "%d-%d-%d", &y, &m, &d); err != nil {
		return map[string]any{"year": nil, "month": nil, "day": nil}
	}
	return map[string]any{"year": y, "month": m, "day": d}
}

type Entry struct {
	ID          int       `json:"id"`
	Status      string    `json:"status"`
	Progress    int       `json:"progress"`
	Repeat      int       `json:"repeat"`
	StartedAt   FuzzyDate `json:"startedAt"`
	CompletedAt FuzzyDate `json:"completedAt"`
	Score       float64   `json:"score"` // requested as POINT_100
}

func (e *Entry) Snapshot() store.EntrySnapshot {
	if e == nil {
		return store.EntrySnapshot{}
	}
	return store.EntrySnapshot{Exists: true, EntryID: e.ID, Status: e.Status, Progress: e.Progress,
		Repeat: e.Repeat, Started: e.StartedAt.String(), Completed: e.CompletedAt.String(), Score: int(e.Score)}
}

type Media struct {
	ID         int      `json:"id"`
	IDMal      int      `json:"idMal"`
	Title      Title    `json:"title"`
	Synonyms   []string `json:"synonyms"`
	Format     string   `json:"format"`
	Status     string   `json:"status"`
	Episodes   int      `json:"episodes"`
	SeasonYear int      `json:"seasonYear"`
	Relations  struct {
		Edges []struct {
			RelationType string `json:"relationType"`
			Node         struct {
				ID       int    `json:"id"`
				Type     string `json:"type"`
				Format   string `json:"format"`
				Status   string `json:"status"`
				Episodes int    `json:"episodes"`
				Title    Title  `json:"title"`
			} `json:"node"`
		} `json:"edges"`
	} `json:"relations"`
	MediaListEntry *Entry `json:"mediaListEntry"`

	// Browse/detail fields (only filled by Browse, Detail and ListFull).
	Duration          int        `json:"duration"`
	Season            string     `json:"season"`
	AverageScore      int        `json:"averageScore"`
	MeanScore         int        `json:"meanScore"`
	Popularity        int        `json:"popularity"`
	Favourites        int        `json:"favourites"`
	Genres            []string   `json:"genres"`
	Source            string     `json:"source"`
	Description       string     `json:"description"`
	SiteURL           string     `json:"siteUrl"`
	StartDate         FuzzyDate  `json:"startDate"`
	EndDate           FuzzyDate  `json:"endDate"`
	CoverImage        CoverImage `json:"coverImage"`
	Rankings          []Ranking  `json:"rankings"`
	Tags              []Tag      `json:"tags"`
	NextAiringEpisode *Airing    `json:"nextAiringEpisode"`
	Studios           struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"studios"`
	Recommendations struct {
		Nodes []struct {
			Media *struct {
				ID     int    `json:"id"`
				Title  Title  `json:"title"`
				Format string `json:"format"`
			} `json:"mediaRecommendation"`
		} `json:"nodes"`
	} `json:"recommendations"`
}

type CoverImage struct {
	ExtraLarge string `json:"extraLarge"`
	Large      string `json:"large"`
	Color      string `json:"color"`
}

type Ranking struct {
	Rank    int    `json:"rank"`
	Type    string `json:"type"` // RATED | POPULAR
	AllTime bool   `json:"allTime"`
	Season  string `json:"season"`
	Year    int    `json:"year"`
	Context string `json:"context"`
}

type Tag struct {
	Name    string `json:"name"`
	Rank    int    `json:"rank"`
	Spoiler bool   `json:"isMediaSpoiler"`
}

type Airing struct {
	Episode         int   `json:"episode"`
	TimeUntilAiring int   `json:"timeUntilAiring"`
	AiringAt        int64 `json:"airingAt"`
}

// Titles returns every known title for matching.
func (m *Media) Titles() []string {
	out := []string{}
	for _, t := range []string{m.Title.Romaji, m.Title.English, m.Title.Native} {
		if t != "" {
			out = append(out, t)
		}
	}
	return append(out, m.Synonyms...)
}

// Sequel returns the next main-story entry (TV/ONA), or 0.
func (m *Media) Sequel() int {
	for _, e := range m.Relations.Edges {
		if e.RelationType == "SEQUEL" && e.Node.Type == "ANIME" {
			switch e.Node.Format {
			case "TV", "TV_SHORT", "ONA":
				return e.Node.ID
			}
		}
	}
	return 0
}

func (m *Media) Info() *store.MediaInfo {
	return &store.MediaInfo{ID: m.ID, MalID: m.IDMal, Title: m.Title.Best(), Format: m.Format,
		Status: m.Status, Episodes: m.Episodes, Sequel: m.Sequel()}
}

type gqlResp struct {
	Data   any `json:"data"`
	Errors []struct {
		Message string `json:"message"`
		Status  int    `json:"status"`
	} `json:"errors"`
}

func (c *Client) gql(ctx context.Context, query string, vars map[string]any, out any) error {
	req, err := netx.JSONRequest(ctx, http.MethodPost, Endpoint, map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp := gqlResp{Data: out}
	err = netx.Do(req, &resp)
	if err != nil {
		if e, ok := err.(*netx.Error); ok && strings.Contains(strings.ToLower(e.Msg), "invalid token") {
			e.Kind = netx.Auth
		}
		return err
	}
	if len(resp.Errors) > 0 {
		e := resp.Errors[0]
		k := netx.Permanent
		switch {
		case e.Status == 404:
			k = netx.NotFound
		case e.Status == 401 || strings.Contains(strings.ToLower(e.Message), "invalid token"):
			k = netx.Auth
		case e.Status == 429 || e.Status >= 500:
			k = netx.Temporary
		}
		return &netx.Error{Kind: k, Status: e.Status, Msg: "anilist: " + e.Message}
	}
	return nil
}

func (c *Client) Viewer(ctx context.Context) (int, string, error) {
	var out struct {
		Viewer struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"Viewer"`
	}
	err := c.gql(ctx, `query{Viewer{id name}}`, nil, &out)
	return out.Viewer.ID, out.Viewer.Name, err
}

const mediaFields = `id idMal title{romaji english native} synonyms format status episodes seasonYear`

func (c *Client) Search(ctx context.Context, q string) ([]Media, error) {
	var out struct {
		Page struct {
			Media []Media `json:"media"`
		} `json:"Page"`
	}
	err := c.gql(ctx, `query($s:String){Page(perPage:10){media(search:$s,type:ANIME,sort:SEARCH_MATCH){`+mediaFields+` mediaListEntry{status}}}}`,
		map[string]any{"s": q}, &out)
	return out.Page.Media, err
}

// Media fetches a show with its sequel relation and (if authenticated) the
// viewer's list entry.
func (c *Client) Media(ctx context.Context, id int) (*Media, error) {
	var out struct {
		Media *Media `json:"Media"`
	}
	q := `query($id:Int){Media(id:$id,type:ANIME){` + mediaFields +
		` relations{edges{relationType node{id type format}}}`
	if c.Token != "" {
		q += ` mediaListEntry{id status progress repeat score(format:POINT_100) startedAt{year month day} completedAt{year month day}}`
	}
	q += `}}`
	if err := c.gql(ctx, q, map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	if out.Media == nil {
		return nil, &netx.Error{Kind: netx.NotFound, Msg: fmt.Sprintf("anilist media %d not found", id)}
	}
	return out.Media, nil
}

func (c *Client) Save(ctx context.Context, mediaID int, e store.EntrySnapshot) error {
	vars := map[string]any{
		"m": mediaID, "s": e.Status, "p": e.Progress, "r": e.Repeat, "sc": e.Score,
		"sa": dateInput(e.Started), "ca": dateInput(e.Completed),
	}
	var out struct {
		SaveMediaListEntry struct {
			ID int `json:"id"`
		} `json:"SaveMediaListEntry"`
	}
	return c.gql(ctx, `mutation($m:Int,$s:MediaListStatus,$p:Int,$r:Int,$sc:Int,$sa:FuzzyDateInput,$ca:FuzzyDateInput){
SaveMediaListEntry(mediaId:$m,status:$s,progress:$p,repeat:$r,scoreRaw:$sc,startedAt:$sa,completedAt:$ca){id}}`, vars, &out)
}

func (c *Client) Delete(ctx context.Context, entryID int) error {
	var out any
	return c.gql(ctx, `mutation($id:Int){DeleteMediaListEntry(id:$id){deleted}}`, map[string]any{"id": entryID}, &out)
}

type ListItem struct {
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Media    Media  `json:"media"`
}

// List returns the user's whole anime list (titles included, for matching).
func (c *Client) List(ctx context.Context, userID int) ([]ListItem, error) {
	var out struct {
		Coll struct {
			Lists []struct {
				Entries []ListItem `json:"entries"`
			} `json:"lists"`
		} `json:"MediaListCollection"`
	}
	err := c.gql(ctx, `query($u:Int){MediaListCollection(userId:$u,type:ANIME){lists{entries{status progress media{`+mediaFields+`}}}}}`,
		map[string]any{"u": userID}, &out)
	var items []ListItem
	for _, l := range out.Coll.Lists {
		items = append(items, l.Entries...)
	}
	return items, err
}

// ByMalID looks up a show by its MyAnimeList id.
func (c *Client) ByMalID(ctx context.Context, malID int) (*Media, error) {
	var out struct {
		Media *Media `json:"Media"`
	}
	if err := c.gql(ctx, `query($id:Int){Media(idMal:$id,type:ANIME){`+mediaFields+`}}`, map[string]any{"id": malID}, &out); err != nil {
		return nil, err
	}
	if out.Media == nil {
		return nil, &netx.Error{Kind: netx.NotFound, Msg: fmt.Sprintf("no AniList entry for MAL id %d", malID)}
	}
	return out.Media, nil
}
