// Package vndb is a minimal VNDB Kana API client.
package vndb

import (
	"context"
	"net/http"

	"github.com/evansunde/anisync/internal/netx"
)

// API is a variable so tests can point it at a mock server.
var API = "https://api.vndb.org/kana"

// Built-in ulist labels.
const (
	Playing  = 1
	Finished = 2
	Stalled  = 3
	Dropped  = 4
	Wishlist = 5
)

type Client struct{ Token string }

func (c *Client) req(ctx context.Context, method, path string, in, out any) error {
	r, err := netx.JSONRequest(ctx, method, API+path, in)
	if err != nil {
		return err
	}
	if c.Token != "" {
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

type VN struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	AltTitle string `json:"alttitle"`
	Released string `json:"released"`
	Titles   []struct {
		Title string `json:"title"`
	} `json:"titles"`
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

type UEntry struct {
	ID     string `json:"id"`
	Labels []struct {
		ID int `json:"id"`
	} `json:"labels"`
	Started  *string `json:"started"`
	Finished *string `json:"finished"`
	Notes    *string `json:"notes"`
	Vote     *int    `json:"vote"`
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

// Patch accepts fields like labels_set, labels_unset, started, finished, notes, vote.
func (c *Client) Patch(ctx context.Context, vid string, fields map[string]any) error {
	return c.req(ctx, http.MethodPatch, "/ulist/"+vid, fields, nil)
}
