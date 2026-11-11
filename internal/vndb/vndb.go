// Package vndb is a small VNDB Kana API client: auth info, VN search and
// details, the user's list (ulist + labels) and list edits.
package vndb

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/evansunde/anisync/internal/netx"
)

// API is a variable so tests can point it at a mock server.
var API = "https://api.vndb.org/kana"

// Built-in ulist labels.
const (
	Playing   = 1
	Finished  = 2
	Stalled   = 3
	Dropped   = 4
	Wishlist  = 5
	Blacklist = 6
	Voted     = 7 // set automatically when a vote exists; can't be set by hand
)

type Client struct{ Token string }

func (c *Client) req(ctx context.Context, method, path string, in, out any) error {
	r, err := netx.JSONRequest(ctx, method, API+path, in)
	if err != nil {
		return err
	}
	if c != nil && c.Token != "" {
		r.Header.Set("Authorization", "Token "+c.Token)
	}
	return netx.Do(r, out)
}

type AuthInfo struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	Permissions []string `json:"permissions"`
}

func (c *Client) AuthInfo(ctx context.Context) (AuthInfo, error) {
	var a AuthInfo
	err := c.req(ctx, http.MethodGet, "/authinfo", nil, &a)
	return a, err
}

type Image struct {
	URL      string  `json:"url"`
	Sexual   float64 `json:"sexual"`   // 0 safe · 1 suggestive · 2 explicit (average)
	Violence float64 `json:"violence"` // 0 tame · 1 violent · 2 brutal
}

type Tag struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Rating   float64 `json:"rating"`  // 0-3
	Spoiler  int     `json:"spoiler"` // 0 none · 1 minor · 2 major
	Category string  `json:"category,omitempty"`
}

type Producer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Relation struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Relation string `json:"relation"`
	Official bool   `json:"relation_official"`
}

type VN struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	AltTitle string `json:"alttitle"`
	Released string `json:"released"`
	Titles   []struct {
		Title string `json:"title"`
		Lang  string `json:"lang,omitempty"`
		Main  bool   `json:"main,omitempty"`
	} `json:"titles"`
	Image         *Image     `json:"image,omitempty"`
	Rating        float64    `json:"rating"` // 10-100, 0 = no votes
	VoteCount     int        `json:"votecount"`
	Length        int        `json:"length"` // 1 very short … 5 very long, 0 unknown
	LengthMinutes int        `json:"length_minutes"`
	Developers    []Producer `json:"developers,omitempty"`
	Tags          []Tag      `json:"tags,omitempty"`
	Description   string     `json:"description,omitempty"`
	Relations     []Relation `json:"relations,omitempty"`
	Languages     []string   `json:"languages,omitempty"`
	Platforms     []string   `json:"platforms,omitempty"`
	OLang         string     `json:"olang,omitempty"`
}

func (v *VN) AllTitles() []string {
	out := []string{v.Title}
	if v.AltTitle != "" {
		out = append(out, v.AltTitle)
	}
	for _, t := range v.Titles {
		out = append(out, t.Title)
	}
	return out
}

// Fields for list/search rows and for the detail view.
const (
	RowFields    = "id,title,alttitle,released,image{url,sexual,violence},rating,votecount,length,length_minutes,developers{name}"
	DetailFields = RowFields + ",titles{title,lang,main},tags{id,name,rating,spoiler,category},description,relations{id,title,relation,relation_official},languages,platforms,olang"
)

func (c *Client) Search(ctx context.Context, q string) ([]VN, error) {
	var out struct {
		Results []VN `json:"results"`
	}
	err := c.req(ctx, http.MethodPost, "/vn", map[string]any{
		"filters": []any{"search", "=", q}, "fields": "id,title,alttitle,released,titles.title",
		"sort": "searchrank", "results": 10,
	}, &out)
	return out.Results, err
}

// Query is a POST /vn request.
type Query struct {
	Filters any // nil = everything
	Sort    string
	Reverse bool
	Page    int
	Results int
}

type Page struct {
	Results []VN `json:"results"`
	More    bool `json:"more"`
	Count   int  `json:"count"`
}

// Browse runs a VN query with the row fields.
func (c *Client) Browse(ctx context.Context, q Query) (Page, error) {
	body := map[string]any{"fields": RowFields, "page": max(q.Page, 1), "results": q.Results, "count": q.Page <= 1}
	if q.Results == 0 {
		body["results"] = 30
	}
	if q.Filters != nil {
		body["filters"] = q.Filters
	}
	if q.Sort != "" {
		body["sort"] = q.Sort
		body["reverse"] = q.Reverse
	}
	var p Page
	err := c.req(ctx, http.MethodPost, "/vn", body, &p)
	return p, err
}

// Detail fetches one VN with everything the detail view shows.
func (c *Client) Detail(ctx context.Context, id string) (*VN, error) {
	var out struct {
		Results []VN `json:"results"`
	}
	if err := c.req(ctx, http.MethodPost, "/vn", map[string]any{"filters": []any{"id", "=", id}, "fields": DetailFields}, &out); err != nil {
		return nil, err
	}
	if len(out.Results) == 0 {
		return nil, &netx.Error{Kind: netx.NotFound, Msg: id + " not found"}
	}
	return &out.Results[0], nil
}

// TagInfo is a tag from POST /tag.
type TagInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases,omitempty"`
	Category   string   `json:"category"`
	VNCount    int      `json:"vn_count"`
	Searchable bool     `json:"searchable"`
}

// Tags returns searchable tags, most used first (100 per page).
func (c *Client) Tags(ctx context.Context, page int) ([]TagInfo, bool, error) {
	var out struct {
		Results []TagInfo `json:"results"`
		More    bool      `json:"more"`
	}
	err := c.req(ctx, http.MethodPost, "/tag", map[string]any{
		"fields": "id,name,category,vn_count,searchable", "sort": "vn_count", "reverse": true, "results": 100, "page": page}, &out)
	tags := out.Results[:0]
	for _, t := range out.Results {
		if t.Searchable {
			tags = append(tags, t)
		}
	}
	return tags, out.More, err
}

// FindTag resolves a tag name (or alias) to a tag, case-insensitively.
func (c *Client) FindTag(ctx context.Context, name string) (*TagInfo, error) {
	var out struct {
		Results []TagInfo `json:"results"`
	}
	if err := c.req(ctx, http.MethodPost, "/tag", map[string]any{"filters": []any{"search", "=", name},
		"fields": "id,name,aliases,category,vn_count", "results": 25}, &out); err != nil {
		return nil, err
	}
	for i, t := range out.Results {
		if strings.EqualFold(t.Name, name) {
			return &out.Results[i], nil
		}
	}
	for i, t := range out.Results {
		for _, a := range t.Aliases {
			if strings.EqualFold(a, name) {
				return &out.Results[i], nil
			}
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------- user list

type LabelRef struct {
	ID    int    `json:"id"`
	Label string `json:"label,omitempty"`
}

type UEntry struct {
	ID       string     `json:"id"`
	Labels   []LabelRef `json:"labels"`
	Started  *string    `json:"started"`
	Finished *string    `json:"finished"`
	Notes    *string    `json:"notes"`
	Vote     *int       `json:"vote"`
}

func (u *UEntry) Has(label int) bool {
	if u == nil {
		return false
	}
	for _, l := range u.Labels {
		if l.ID == label {
			return true
		}
	}
	return false
}

// Entry returns the user's list entry for vid, or nil.
func (c *Client) Entry(ctx context.Context, user, vid string) (*UEntry, error) {
	var out struct {
		Results []UEntry `json:"results"`
	}
	err := c.req(ctx, http.MethodPost, "/ulist", map[string]any{
		"user": user, "filters": []any{"id", "=", vid}, "fields": "labels.id,started,finished,notes,vote",
	}, &out)
	if err != nil || len(out.Results) == 0 {
		return nil, err
	}
	return &out.Results[0], nil
}

// ListItem is one VN in the user's list with display data.
type ListItem struct {
	ID       string     `json:"id"`
	Vote     *int       `json:"vote"`
	Started  *string    `json:"started"`
	Finished *string    `json:"finished"`
	Notes    *string    `json:"notes"`
	Added    int64      `json:"added"`
	LastMod  int64      `json:"lastmod"`
	Labels   []LabelRef `json:"labels"`
	VN       VN         `json:"vn"`
}

// UList fetches the user's whole list, most recently changed first.
func (c *Client) UList(ctx context.Context, user string) ([]ListItem, error) {
	var all []ListItem
	for page := 1; page <= 100; page++ {
		var out struct {
			Results []ListItem `json:"results"`
			More    bool       `json:"more"`
		}
		err := c.req(ctx, http.MethodPost, "/ulist", map[string]any{"user": user,
			"fields":  "id,vote,started,finished,notes,added,lastmod,labels{id,label},vn{" + strings.ReplaceAll(RowFields, "id,", "") + "}",
			"results": 100, "page": page, "sort": "lastmod", "reverse": true}, &out)
		if err != nil {
			return nil, err
		}
		all = append(all, out.Results...)
		if !out.More {
			break
		}
	}
	return all, nil
}

type Label struct {
	ID      int    `json:"id"`
	Label   string `json:"label"`
	Private bool   `json:"private"`
	Count   int    `json:"count"`
}

// Labels returns the user's labels (built-in and custom) with entry counts.
func (c *Client) Labels(ctx context.Context, user string) ([]Label, error) {
	var out struct {
		Labels []Label `json:"labels"`
	}
	err := c.req(ctx, http.MethodGet, "/ulist_labels?fields=count&user="+url.QueryEscape(user), nil, &out)
	return out.Labels, err
}

// Patch adds or updates a list entry. Fields: labels_set, labels_unset,
// vote (10-100, nil clears), started, finished (YYYY-MM-DD, nil clears), notes.
func (c *Client) Patch(ctx context.Context, vid string, fields map[string]any) error {
	return c.req(ctx, http.MethodPatch, "/ulist/"+vid, fields, nil)
}

// Delete removes a VN from the user's list.
func (c *Client) Delete(ctx context.Context, vid string) error {
	return c.req(ctx, http.MethodDelete, "/ulist/"+vid, nil, nil)
}

// ---------------------------------------------------------------- helpers

// Num returns the numeric part of an id like "v17" (0 if malformed).
func Num(id string) int {
	if len(id) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(id[1:])
	return n
}

var LengthNames = []string{"", "Very short", "Short", "Medium", "Long", "Very long"}

// LengthHint is the rough playtime of a length class.
var LengthHint = []string{"", "< 2h", "2–10h", "10–30h", "30–50h", "> 50h"}

var (
	reBBURL   = regexp.MustCompile(`(?is)\[url=[^\]]*\](.*?)\[/url\]`)
	reBBSpoil = regexp.MustCompile(`(?is)\[spoiler\].*?\[/spoiler\]`)
	reBBRaw   = regexp.MustCompile(`(?is)\[raw\](.*?)\[/raw\]`)
	reBBCode  = regexp.MustCompile(`(?is)\[code\](.*?)\[/code\]`)
	reBBTag   = regexp.MustCompile(`(?i)\[/?(?:b|i|u|s|quote|url|spoiler)\]`)
	reBlank   = regexp.MustCompile(`\n{3,}`)
)

// PlainDescription strips VNDB BBCode: links keep their text, spoilers are
// removed, and formatting tags disappear.
func PlainDescription(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = reBBRaw.ReplaceAllString(s, "$1")
	s = reBBCode.ReplaceAllString(s, "$1")
	s = reBBSpoil.ReplaceAllString(s, "")
	s = reBBURL.ReplaceAllString(s, "$1")
	s = reBBTag.ReplaceAllString(s, "")
	s = reBlank.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
