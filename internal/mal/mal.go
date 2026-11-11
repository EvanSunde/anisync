// Package mal is a minimal MyAnimeList API v2 client (OAuth2 PKCE).
package mal

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/evansunde/anisync/internal/netx"
	"github.com/evansunde/anisync/internal/store"
)

// Variables so tests can point them at a mock server.
var (
	API      = "https://api.myanimelist.net/v2"
	AuthBase = "https://myanimelist.net/v1/oauth2"
)

// AniList status vocabulary <-> MAL status.
var toMAL = map[string]string{
	"CURRENT": "watching", "REPEATING": "completed", "COMPLETED": "completed",
	"PAUSED": "on_hold", "DROPPED": "dropped", "PLANNING": "plan_to_watch",
}

type Client struct {
	ClientID, ClientSecret string

	mu      sync.Mutex
	Access  string
	Refresh string
	Expires time.Time
	// OnRefresh persists rotated tokens.
	OnRefresh func(access, refresh string, expires time.Time)
}

func Verifier() string {
	b := make([]byte, 64)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b) // 86 chars, within 43..128
}

// AuthURL uses PKCE "plain", the only method MAL supports.
func AuthURL(clientID, verifier, state, redirect string) string {
	v := url.Values{"response_type": {"code"}, "client_id": {clientID}, "code_challenge": {verifier},
		"code_challenge_method": {"plain"}, "state": {state}, "redirect_uri": {redirect}}
	return AuthBase + "/authorize?" + v.Encode()
}

type tokenResp struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	ExpiresIn int    `json:"expires_in"`
}

func (c *Client) token(ctx context.Context, v url.Values) error {
	v.Set("client_id", c.ClientID)
	if c.ClientSecret != "" {
		v.Set("client_secret", c.ClientSecret)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, AuthBase+"/token", strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var t tokenResp
	if err := netx.Do(req, &t); err != nil {
		if e, ok := err.(*netx.Error); ok && (e.Status == 400 || e.Status == 401) {
			e.Kind = netx.Auth
		}
		return err
	}
	c.Access, c.Refresh = t.Access, t.Refresh
	c.Expires = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	if c.OnRefresh != nil {
		c.OnRefresh(c.Access, c.Refresh, c.Expires)
	}
	return nil
}

func (c *Client) Exchange(ctx context.Context, code, verifier, redirect string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"code_verifier": {verifier}, "redirect_uri": {redirect}})
}

func (c *Client) ensure(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Access == "" && c.Refresh == "" {
		return "", &netx.Error{Kind: netx.Auth, Msg: "mal: not logged in (run: anisync auth mal)"}
	}
	if force || (c.Refresh != "" && time.Until(c.Expires) < 3*24*time.Hour) {
		if err := c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.Refresh}}); err != nil {
			return "", err
		}
	}
	return c.Access, nil
}

// do performs an authenticated request, refreshing once on 401.
func (c *Client) do(ctx context.Context, method, path string, form url.Values, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := c.ensure(ctx, attempt > 0)
		if err != nil {
			return err
		}
		var body *strings.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		} else {
			body = strings.NewReader("")
		}
		req, err := http.NewRequestWithContext(ctx, method, API+path, body)
		if err != nil {
			return err
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		err = netx.Do(req, out)
		if netx.KindOf(err) == netx.Auth && attempt == 0 && c.Refresh != "" {
			continue
		}
		return err
	}
	return nil
}

func (c *Client) User(ctx context.Context) (string, error) {
	var u struct {
		Name string `json:"name"`
	}
	err := c.do(ctx, http.MethodGet, "/users/@me", nil, &u)
	return u.Name, err
}

type listStatus struct {
	Status       string `json:"status"`
	Watched      int    `json:"num_episodes_watched"`
	Rewatching   bool   `json:"is_rewatching"`
	TimesRewatch int    `json:"num_times_rewatched"`
	StartDate    string `json:"start_date"`
	FinishDate   string `json:"finish_date"`
	Score        int    `json:"score"` // 0-10
}

func (s *listStatus) snapshot() store.EntrySnapshot {
	if s == nil || s.Status == "" {
		return store.EntrySnapshot{}
	}
	st := map[string]string{"watching": "CURRENT", "completed": "COMPLETED", "on_hold": "PAUSED",
		"dropped": "DROPPED", "plan_to_watch": "PLANNING"}[s.Status]
	if s.Rewatching {
		st = "REPEATING"
	}
	return store.EntrySnapshot{Exists: true, Status: st, Progress: s.Watched, Repeat: s.TimesRewatch,
		Started: s.StartDate, Completed: s.FinishDate, Score: s.Score * 10}
}

// Entry returns the user's list entry and the episode count.
func (c *Client) Entry(ctx context.Context, id int) (store.EntrySnapshot, int, error) {
	var a struct {
		Episodes int         `json:"num_episodes"`
		My       *listStatus `json:"my_list_status"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/anime/%d?fields=num_episodes,my_list_status{start_date,finish_date,num_times_rewatched,score}", id), nil, &a)
	return a.My.snapshot(), a.Episodes, err
}

func (c *Client) Save(ctx context.Context, id int, e store.EntrySnapshot) error {
	f := url.Values{
		"status":               {toMAL[e.Status]},
		"num_watched_episodes": {strconv.Itoa(e.Progress)},
		"is_rewatching":        {strconv.FormatBool(e.Status == "REPEATING")},
		"num_times_rewatched":  {strconv.Itoa(e.Repeat)},
		"score":                {strconv.Itoa((e.Score + 5) / 10)},
	}
	if e.Started != "" {
		f.Set("start_date", e.Started)
	}
	if e.Completed != "" {
		f.Set("finish_date", e.Completed)
	}
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("/anime/%d/my_list_status", id), f, nil)
}

func (c *Client) Delete(ctx context.Context, id int) error {
	err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/anime/%d/my_list_status", id), nil, nil)
	if netx.KindOf(err) == netx.NotFound {
		return nil
	}
	return err
}

// ListIDs returns MAL id -> AniList-vocabulary status for the whole list.
func (c *Client) ListIDs(ctx context.Context) (map[int]string, error) {
	out := map[int]string{}
	path := "/users/@me/animelist?fields=list_status&limit=1000&nsfw=true"
	for path != "" {
		var page struct {
			Data []struct {
				Node struct {
					ID int `json:"id"`
				} `json:"node"`
				ListStatus *listStatus `json:"list_status"`
			} `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
			return out, err
		}
		for _, d := range page.Data {
			out[d.Node.ID] = d.ListStatus.snapshot().Status
		}
		path = strings.TrimPrefix(page.Paging.Next, API)
	}
	return out, nil
}

// ListEntry is one row of the user's MAL anime list with show details.
type ListEntry struct {
	ID          int      `json:"id"`
	Title       string   `json:"title"`
	English     string   `json:"english,omitempty"`
	Episodes    int      `json:"episodes"`
	Mean        float64  `json:"mean"`
	MediaType   string   `json:"media_type"`
	AirStatus   string   `json:"air_status"`
	Genres      []string `json:"genres"`
	Year        int      `json:"year"`
	Picture     string   `json:"picture"`
	Status      string   `json:"status"` // AniList vocabulary
	Progress    int      `json:"progress"`
	Score       int      `json:"score"` // 0-100
	StartedDate string   `json:"started,omitempty"`
}

// List returns the whole anime list with details (for the TUI).
func (c *Client) List(ctx context.Context) ([]ListEntry, error) {
	var out []ListEntry
	path := "/users/@me/animelist?limit=1000&nsfw=true&fields=list_status{start_date,finish_date,num_times_rewatched,score}," +
		"num_episodes,mean,media_type,status,genres,start_season,alternative_titles,main_picture"
	for path != "" {
		var page struct {
			Data []struct {
				Node struct {
					ID        int     `json:"id"`
					Title     string  `json:"title"`
					Episodes  int     `json:"num_episodes"`
					Mean      float64 `json:"mean"`
					MediaType string  `json:"media_type"`
					Status    string  `json:"status"`
					Genres    []struct {
						Name string `json:"name"`
					} `json:"genres"`
					StartSeason struct {
						Year int `json:"year"`
					} `json:"start_season"`
					Alt struct {
						En string `json:"en"`
					} `json:"alternative_titles"`
					Picture struct {
						Large  string `json:"large"`
						Medium string `json:"medium"`
					} `json:"main_picture"`
				} `json:"node"`
				ListStatus *listStatus `json:"list_status"`
			} `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
			return out, err
		}
		for _, d := range page.Data {
			n := d.Node
			snap := d.ListStatus.snapshot()
			e := ListEntry{ID: n.ID, Title: n.Title, English: n.Alt.En, Episodes: n.Episodes, Mean: n.Mean,
				MediaType: n.MediaType, AirStatus: n.Status, Year: n.StartSeason.Year, Picture: n.Picture.Large,
				Status: snap.Status, Progress: snap.Progress, Score: snap.Score, StartedDate: snap.Started}
			if e.Picture == "" {
				e.Picture = n.Picture.Medium
			}
			for _, g := range n.Genres {
				e.Genres = append(e.Genres, g.Name)
			}
			out = append(out, e)
		}
		path = strings.TrimPrefix(page.Paging.Next, API)
	}
	return out, nil
}
