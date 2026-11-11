// Package netx has the shared HTTP client and error classification used to
// decide whether a queued update should be retried later.
package netx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Kind int

const (
	Temporary Kind = iota // offline, timeout, 5xx, rate limited: retry later
	Auth                  // token missing/expired: retry after re-auth
	Permanent             // bad request: drop the item
	NotFound
)

type Error struct {
	Kind       Kind
	Status     int
	Msg        string
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Msg)
	}
	return e.Msg
}

func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	if err == nil {
		return Permanent
	}
	return Temporary // transport errors
}

func IsTemporary(err error) bool { return err != nil && KindOf(err) == Temporary }

var Client = &http.Client{Timeout: 20 * time.Second}

const UserAgent = "anisync/1.0"

// Do sends req and decodes a JSON response into out (if non-nil).
func Do(req *http.Request, out any) error {
	req.Header.Set("User-Agent", UserAgent)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := Client.Do(req)
	if err != nil {
		return &Error{Kind: Temporary, Msg: err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return &Error{Kind: Temporary, Msg: err.Error()}
	}
	if resp.StatusCode >= 300 {
		e := &Error{Status: resp.StatusCode, Msg: truncate(strings.TrimSpace(string(body)), 300)}
		switch {
		case resp.StatusCode == 429:
			e.Kind = Temporary
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
				e.RetryAfter = time.Duration(s) * time.Second
			} else {
				e.RetryAfter = time.Minute
			}
		case resp.StatusCode >= 500:
			e.Kind = Temporary
		case resp.StatusCode == 401:
			e.Kind = Auth
		case resp.StatusCode == 404:
			e.Kind = NotFound
		default:
			e.Kind = Permanent
		}
		return e
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &Error{Kind: Permanent, Msg: "decode: " + err.Error()}
	}
	return nil
}

func JSONRequest(ctx context.Context, method, url string, in any) (*http.Request, error) {
	var r io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// Today returns the local date as YYYY-MM-DD.
func Today() string { return time.Now().Format("2006-01-02") }
