// anisync: scrobble anime watched in local players (via MPRIS) to AniList and
// MyAnimeList, and visual novels to VNDB, with an offline queue.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/config"
	"github.com/evansunde/anisync/internal/daemon"
	"github.com/evansunde/anisync/internal/ipc"
	"github.com/evansunde/anisync/internal/mal"
	"github.com/evansunde/anisync/internal/media"
	"github.com/evansunde/anisync/internal/mpris"
	"github.com/evansunde/anisync/internal/store"
	"github.com/evansunde/anisync/internal/tui"
	"github.com/evansunde/anisync/internal/vndb"
)

var version = "dev"

const usage = `anisync — AniList / MyAnimeList / VNDB scrobbler for local playback

Usage:
  anisync daemon                 run the background tracker (use the systemd unit)
  anisync init                   write a default config file
  anisync auth anilist|mal|vndb  log in to a service
  anisync tui [vn]               browse/search anime (or visual novels), manage your lists, pick matches

  anisync status                 services, players, queued updates
  anisync resolve                pick the right show for titles that couldn't be matched
  anisync map <title> <id|url> [-season N] [-offset N]
                                 manually map a file title to an AniList/MAL id or URL
  anisync forget <title>         drop a (wrong) mapping or ignore so it's matched again
  anisync mappings               list remembered title mappings
  anisync sync                   retry queued updates now and refresh lists
  anisync history [N]            recent updates
  anisync undo [ID]              revert the last (or given) update (anime or VNDB list)

  anisync vn list                tracked visual novels and playtime
  anisync vn finish [name] [-vote 8.5]
                                 mark a VN finished on VNDB (default: last played)
  anisync vn tui                 the TUI in visual novel mode
`

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "daemon", "run":
		log.SetFlags(log.LstdFlags)
		var cfg config.Config
		if cfg, err = config.Load(); err == nil {
			err = daemon.Run(cfg)
		}
	case "init":
		var created bool
		if created, err = config.WriteDefault(); err == nil {
			if created {
				fmt.Println("wrote", config.Path())
			} else {
				fmt.Println(config.Path(), "already exists")
			}
		}
	case "auth":
		err = auth(args)
	case "tui", "ui":
		vn := len(args) > 0 && strings.TrimLeft(args[0], "-") == "vn"
		err = tui.Run(vn)
	case "status":
		err = status()
	case "resolve", "pending":
		err = resolve()
	case "map":
		err = mapCmd(args)
	case "forget":
		var removed []string
		if err = call("forget", strings.Join(args, " "), &removed); err == nil {
			if len(removed) == 0 {
				fmt.Println("nothing matched")
			}
			for _, r := range removed {
				fmt.Println("forgot", r)
			}
		}
	case "mappings":
		var ms []daemon.MappingInfo
		if err = call("mappings", nil, &ms); err == nil {
			for _, m := range ms {
				id := ""
				switch {
				case m.AnilistID != 0:
					id = fmt.Sprintf("anilist:%d", m.AnilistID)
				case m.VNDBID != "":
					id = m.VNDBID
				}
				off := ""
				if m.Offset != 0 {
					off = fmt.Sprintf(" offset %+d", m.Offset)
				}
				fmt.Printf("%-45s %-16s %s%s\n", m.Key, id, m.Title, off)
			}
		}
	case "sync":
		if err = call("sync", nil, nil); err == nil {
			fmt.Println("sync requested")
		}
	case "history":
		err = history(args)
	case "undo":
		var id int64
		if len(args) > 0 {
			id, _ = strconv.ParseInt(args[0], 10, 64)
		}
		var h store.HistoryEntry
		if err = call("undo", id, &h); err == nil {
			if h.Episode > 0 {
				fmt.Printf("reverted: %s episode %d\n", h.Title, h.Episode)
			} else {
				fmt.Printf("reverted: %s\n", h.Title)
			}
		}
	case "vn":
		err = vnCmd(args)
	case "version", "--version":
		fmt.Println("anisync", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func call(cmd string, args, out any) error { return ipc.Call(config.SocketPath(), cmd, args, out) }

var in = bufio.NewReader(os.Stdin)

func prompt(q string) string {
	fmt.Print(q)
	s, _ := in.ReadString('\n')
	return strings.TrimSpace(s)
}

func openBrowser(u string) {
	if exec.Command("xdg-open", u).Start() != nil {
		fmt.Println("(open the URL above in your browser)")
	}
}

// ---------------------------------------------------------------- auth

func auth(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: anisync auth anilist|mal|vndb")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	tok, err := store.LoadTokens(config.ConfigDir())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	switch args[0] {
	case "anilist":
		if cfg.AniList.ClientID == "" {
			return fmt.Errorf(`set [anilist] client_id in %s first:
  1. https://anilist.co/settings/developer → Create New Client
  2. Redirect URL: https://anilist.co/api/v2/oauth/pin
  3. copy the client ID into the config`, config.Path())
		}
		u := anilist.AuthURL(cfg.AniList.ClientID)
		fmt.Println("Authorize anisync, then paste the token shown:\n ", u)
		openBrowser(u)
		t := prompt("token: ")
		if t == "" {
			return errors.New("no token")
		}
		c := &anilist.Client{Token: t}
		id, name, err := c.Viewer(ctx)
		if err != nil {
			return err
		}
		tok.AniList.Token, tok.AniList.UserID, tok.AniList.User = t, id, name
		tok.AniList.Expires = time.Now().AddDate(1, 0, 0)
		fmt.Println("logged in to AniList as", name)
	case "mal":
		if cfg.MAL.ClientID == "" {
			return fmt.Errorf(`set [mal] client_id in %s first:
  1. https://myanimelist.net/apiconfig → Create ID
  2. App Type: other, App Redirect URL: http://localhost:%d/callback
  3. copy the Client ID (and secret, if shown) into the config`, config.Path(), cfg.MAL.RedirectPort)
		}
		name, access, refresh, exp, err := malLogin(ctx, cfg)
		if err != nil {
			return err
		}
		tok.MAL.Access, tok.MAL.Refresh, tok.MAL.Expires, tok.MAL.User = access, refresh, exp, name
		fmt.Println("logged in to MyAnimeList as", name)
	case "vndb":
		fmt.Println(`Create a token at https://vndb.org/u/tokens (log in → My Profile → Applications)
with "Access to my list" and "Write to my list" enabled.`)
		t := prompt("token: ")
		c := &vndb.Client{Token: t}
		a, err := c.AuthInfo(ctx)
		if err != nil {
			return err
		}
		if !contains(a.Permissions, "listwrite") {
			return errors.New(`token lacks "listwrite" permission`)
		}
		tok.VNDB.Token, tok.VNDB.UserID, tok.VNDB.User = t, a.ID, a.Username
		fmt.Println("logged in to VNDB as", a.Username)
		if !cfg.VNDB.Enabled || len(cfg.VNDB.Dirs) == 0 {
			fmt.Printf("note: VN tracking is off; set [vndb] enabled = true and dirs in %s, then restart the daemon\n", config.Path())
		}
	default:
		return fmt.Errorf("unknown service %q", args[0])
	}
	if err := store.SaveTokens(config.ConfigDir(), tok); err != nil {
		return err
	}
	if err := call("reload", nil, nil); err != nil && !errors.Is(err, ipc.ErrNotRunning) {
		return err
	}
	return nil
}

func malLogin(ctx context.Context, cfg config.Config) (name, access, refresh string, exp time.Time, err error) {
	verifier := mal.Verifier()
	sb := make([]byte, 16)
	_, _ = rand.Read(sb)
	state := hex.EncodeToString(sb)
	redirect := fmt.Sprintf("http://localhost:%d/callback", cfg.MAL.RedirectPort)

	codes := make(chan string, 2)
	errs := make(chan error, 2)
	check := func(q url.Values) {
		if q.Get("state") != state {
			errs <- errors.New("state mismatch (stale browser tab?)")
			return
		}
		if c := q.Get("code"); c != "" {
			codes <- c
		} else {
			errs <- fmt.Errorf("authorization failed: %s", q.Get("error"))
		}
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintln(w, "anisync: done, you can close this tab.")
		check(r.URL.Query())
	})}
	if l, lerr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.MAL.RedirectPort)); lerr == nil {
		go srv.Serve(l)
		defer srv.Close()
	} else {
		fmt.Println("(could not listen on the redirect port; paste the redirected URL instead)")
	}
	u := mal.AuthURL(cfg.MAL.ClientID, verifier, state, redirect)
	fmt.Println("Authorize anisync in your browser:\n ", u)
	fmt.Println("If the browser can't reach localhost, paste the URL it was redirected to here.")
	openBrowser(u)
	go func() {
		s, _ := in.ReadString('\n')
		if pu, perr := url.Parse(strings.TrimSpace(s)); perr == nil && pu.RawQuery != "" {
			check(pu.Query())
		}
	}()
	var code string
	select {
	case code = <-codes:
	case err = <-errs:
		return
	case <-ctx.Done():
		err = ctx.Err()
		return
	}
	c := &mal.Client{ClientID: cfg.MAL.ClientID, ClientSecret: cfg.MAL.ClientSecret}
	if err = c.Exchange(ctx, code, verifier, redirect); err != nil {
		return
	}
	name, err = c.User(ctx)
	return name, c.Access, c.Refresh, c.Expires, err
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- status

func status() error {
	var s daemon.Status
	if err := call("status", nil, &s); err != nil {
		return err
	}
	var svc []string
	for _, k := range []string{"anilist", "mal", "vndb"} {
		name := map[string]string{"anilist": "AniList", "mal": "MAL", "vndb": "VNDB"}[k]
		if u, ok := s.Services[k]; ok {
			mark := "✓"
			if contains(s.AuthErr, k) {
				mark = "✗ login expired"
			}
			svc = append(svc, fmt.Sprintf("%s: %s %s", name, u, mark))
		} else {
			svc = append(svc, name+": off")
		}
	}
	fmt.Println("Services:", strings.Join(svc, "   "))
	if len(s.Players) == 0 {
		fmt.Println("Players:  none running")
	} else {
		fmt.Println("Players:")
		for _, p := range s.Players {
			printPlayer(p)
		}
	}
	for k, v := range s.VNActive {
		fmt.Printf("VN:       %s (session %s)\n", k, v)
	}
	if len(s.Queue) == 0 {
		fmt.Println("Queue:    empty")
	} else {
		fmt.Printf("Queue:    %d item(s)", len(s.Queue))
		if s.LastErr != "" {
			fmt.Printf(" — offline/unavailable, retry %s (%s)", humanUntil(s.RetryAt), s.LastErr)
		}
		fmt.Println()
		for _, it := range s.Queue {
			fmt.Println("  ", describeItem(it))
		}
	}
	if s.Pending > 0 {
		fmt.Printf("\n%d title(s) need a match — run: anisync resolve\n", s.Pending)
	}
	return nil
}

func printPlayer(p mpris.Status) {
	title := p.Title
	if pth := media.FromURL(title); pth != "" {
		title = pth[strings.LastIndex(pth, "/")+1:]
	}
	note := fmt.Sprintf("%3.0f%% watched", p.Watched*100)
	switch {
	case !p.Tracked && title != "":
		note = "not tracked (filtered)"
	case p.Counted:
		note += " ✓ counted"
	}
	if title == "" {
		title, note = "(nothing loaded)", ""
	}
	fmt.Printf("  %-12s %-8s %s  %s\n", p.Player, p.State, title, note)
}

func describeItem(it *store.Item) string {
	switch it.Kind {
	case "vn":
		return fmt.Sprintf("VN %s: %s", firstNonEmpty(it.TargetTitle, it.Title), it.Action)
	}
	s := firstNonEmpty(it.TargetTitle, it.Title)
	if it.Season > 1 && it.TargetTitle == "" {
		s += fmt.Sprintf(" S%d", it.Season)
	}
	ep := it.Episode
	if it.TargetEp > 0 {
		ep = it.TargetEp
	}
	s += fmt.Sprintf(" episode %d", ep)
	if it.LastErr != "" {
		s += "  (" + it.LastErr + ")"
	}
	return s
}

func humanUntil(t time.Time) string {
	if t.IsZero() {
		return "soon"
	}
	d := time.Until(t).Round(time.Second)
	if d <= 0 {
		return "now"
	}
	return "in " + d.String()
}

// ---------------------------------------------------------------- resolve

func resolve() error {
	var pend []*store.Pending
	if err := call("pending", nil, &pend); err != nil {
		return err
	}
	if len(pend) == 0 {
		fmt.Println("nothing to resolve")
		return nil
	}
	for _, p := range pend {
		cands := p.Candidates
	again:
		fmt.Printf("\n\033[1m%s\033[0m  (%s)\n", p.Query, map[string]string{"anime": "anime", "vn": "visual novel"}[p.Kind])
		for i, c := range cands {
			fmt.Printf("  %d) %s\n", i+1, describeCandidate(c))
		}
		if len(cands) == 0 {
			fmt.Println("  no results")
		}
		a := prompt("number | s=search another title | i=enter ID/URL | x=always ignore | Enter=skip: ")
		switch {
		case a == "":
			continue
		case a == "x":
			if err := call("resolve", daemon.ResolveArgs{Key: p.Key, Ignore: true}, nil); err != nil {
				return err
			}
			fmt.Println("ignored")
		case a == "s":
			q := prompt("search: ")
			if q == "" {
				goto again
			}
			var res []store.Candidate
			if err := call("search", daemon.SearchArgs{Kind: p.Kind, Query: q}, &res); err != nil {
				return err
			}
			cands = res
			goto again
		case a == "i":
			ra, err := parseID(p.Kind, prompt("ID or URL: "))
			if err != nil {
				fmt.Println(err)
				goto again
			}
			ra.Key = p.Key
			if err := call("resolve", ra, nil); err != nil {
				return err
			}
			fmt.Println("mapped →", ra.Title)
		default:
			n, err := strconv.Atoi(a)
			if err != nil || n < 1 || n > len(cands) {
				goto again
			}
			c := cands[n-1]
			if err := call("resolve", daemon.ResolveArgs{Key: p.Key, AnilistID: c.AnilistID, MalID: c.MalID, VNDBID: c.VNDBID, Title: c.Title}, nil); err != nil {
				return err
			}
			fmt.Println("mapped →", c.Title)
		}
	}
	return nil
}

func describeCandidate(c store.Candidate) string {
	s := c.Title
	if c.Alt != "" && c.Alt != c.Title {
		s += " / " + c.Alt
	}
	var meta []string
	if c.Format != "" {
		meta = append(meta, c.Format)
	}
	if c.Year > 0 {
		meta = append(meta, strconv.Itoa(c.Year))
	}
	if c.Episodes > 0 {
		meta = append(meta, fmt.Sprintf("%d eps", c.Episodes))
	}
	if len(meta) > 0 {
		s += "  [" + strings.Join(meta, ", ") + "]"
	}
	if c.InList != "" {
		s += "  \033[32m(on your list: " + strings.ToLower(c.InList) + ")\033[0m"
	}
	return s
}

var (
	reALURL  = regexp.MustCompile(`anilist\.co/anime/(\d+)`)
	reMALURL = regexp.MustCompile(`myanimelist\.net/anime/(\d+)`)
	reVNID   = regexp.MustCompile(`\b(v\d+)\b`)
)

// parseID accepts an AniList id/URL, a MAL URL ("mal:123" too), or a VNDB id/URL.
func parseID(kind, s string) (daemon.ResolveArgs, error) {
	var a daemon.ResolveArgs
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if kind == "vn" {
		m := reVNID.FindStringSubmatch(s)
		if m == nil {
			return a, errors.New("expected a VNDB id like v17 or a vndb.org URL")
		}
		a.VNDBID, a.Title = m[1], m[1]
		return a, nil
	}
	al := &anilist.Client{}
	var med *anilist.Media
	var err error
	switch {
	case reMALURL.MatchString(s) || strings.HasPrefix(s, "mal:"):
		var id int
		if m := reMALURL.FindStringSubmatch(s); m != nil {
			id, _ = strconv.Atoi(m[1])
		} else {
			id, _ = strconv.Atoi(strings.TrimPrefix(s, "mal:"))
		}
		med, err = al.ByMalID(ctx, id)
	default:
		id := 0
		if m := reALURL.FindStringSubmatch(s); m != nil {
			id, _ = strconv.Atoi(m[1])
		} else if id, err = strconv.Atoi(s); err != nil {
			return a, errors.New("expected an AniList id/URL or a MAL URL")
		}
		med, err = al.Media(ctx, id)
	}
	if err != nil {
		return a, err
	}
	a.AnilistID, a.MalID, a.Title = med.ID, med.IDMal, med.Title.Best()
	return a, nil
}

func mapCmd(args []string) error {
	fs := flag.NewFlagSet("map", flag.ContinueOnError)
	season := fs.Int("season", 0, "season number in the file names")
	offset := fs.Int("offset", 0, "added to the file's episode number")
	var pos []string
	for len(args) > 0 { // allow flags after positionals
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != 2 {
		return errors.New(`usage: anisync map "<title as in file names>" <anilist id|url|mal url> [-season N] [-offset N]`)
	}
	a, err := parseID("anime", pos[1])
	if err != nil {
		return err
	}
	a.Key = media.Key(pos[0], *season)
	a.Offset = *offset
	if err := call("map", a, nil); err != nil {
		return err
	}
	fmt.Printf("%q → %s (anilist %d)\n", pos[0], a.Title, a.AnilistID)
	return nil
}

// ---------------------------------------------------------------- history / vn

func history(args []string) error {
	n := 20
	if len(args) > 0 {
		n, _ = strconv.Atoi(args[0])
	}
	var h []store.HistoryEntry
	if err := call("history", n, &h); err != nil {
		return err
	}
	for _, e := range h {
		what := e.Title
		if e.Kind == "anime" {
			what += fmt.Sprintf(" ep %d", e.Episode)
		} else {
			what += " (" + e.Action + ")"
		}
		var res []string
		for _, k := range []string{"anilist", "mal", "vndb"} {
			if r, ok := e.Result[k]; ok {
				res = append(res, k+": "+r)
			}
		}
		u := ""
		if e.Undone {
			u = " [undone]"
		}
		fmt.Printf("%4d  %s  %s%s\n      %s\n", e.ID, e.Time.Local().Format("Jan 02 15:04"), what, u, strings.Join(res, "; "))
	}
	return nil
}

func vnCmd(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "tui", "ui":
		return tui.Run(true)
	case "list":
		var l []daemon.VNInfo
		if err := call("vn-list", nil, &l); err != nil {
			return err
		}
		if len(l) == 0 {
			fmt.Println("no VNs tracked yet")
		}
		for _, v := range l {
			t := v.Title
			if t == "" {
				t = "(unmatched)"
			}
			fmt.Printf("%-35s %-8s %-30s %3dh%02dm  last %s\n", v.Folder, v.VNDBID, t,
				int(v.Playtime.Hours()), int(v.Playtime.Minutes())%60, v.LastPlayed.Local().Format("Jan 02"))
		}
		return nil
	case "finish":
		fs := flag.NewFlagSet("vn finish", flag.ContinueOnError)
		vote := fs.Float64("vote", 0, "your rating 1-10 (e.g. 8.5)")
		rest := args[1:]
		var name []string
		for len(rest) > 0 {
			if err := fs.Parse(rest); err != nil {
				return err
			}
			if fs.NArg() == 0 {
				break
			}
			name = append(name, fs.Arg(0))
			rest = fs.Args()[1:]
		}
		v := int(*vote*10 + 0.5)
		if *vote != 0 && (v < 10 || v > 100) {
			return errors.New("vote must be between 1 and 10")
		}
		var got string
		if err := call("vn-finish", daemon.VNFinishArgs{Name: strings.Join(name, " "), Vote: v}, &got); err != nil {
			return err
		}
		fmt.Println("queued: mark finished →", got)
		return nil
	}
	return fmt.Errorf("unknown vn command %q", args[0])
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}
