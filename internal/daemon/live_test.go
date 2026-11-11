//go:build live

package daemon

import (
	"context"
	"fmt"
	"testing"

	"github.com/evansunde/anisync/internal/anilist"
	"github.com/evansunde/anisync/internal/media"
)

// go test -tags live ./internal/daemon/ -run Live -v   (hits the real AniList API)
func TestLiveAutoPick(t *testing.T) {
	d := &Daemon{al: &anilist.Client{}}
	files := []string{
		"/a/[SubsPlease] Sousou no Frieren - 05 (1080p) [ABCD1234].mkv",
		"/a/[SubsPlease] Dandadan - 03 (1080p).mkv",
		"/a/Bocchi the Rock! - 01 [BD 1080p].mkv",
		"/a/[Erai-raws] Kusuriya no Hitorigoto - 12 [1080p].mkv",
		"/a/One Piece - 1100 [1080p].mkv",
		"/a/[Judas] Spy x Family - 03.mkv",
		"/a/Hunter x Hunter - 05.mkv", // 1999 vs 2011: must NOT auto-pick
		"/a/Fruits Basket - 05.mkv",   // 2001 vs 2019: must NOT auto-pick
		"/a/[ASW] 86 - Eighty Six - 12.mkv",
	}
	for _, f := range files {
		p := media.ParsePath(f)
		res, err := d.al.Search(context.Background(), p.Title)
		if err != nil {
			t.Fatal(err)
		}
		pick := d.autoPick(p.Title, p.Episode, res)
		got := "→ (ask user)"
		if pick != nil {
			got = fmt.Sprintf("→ %s (%d)", pick.Title.Best(), pick.SeasonYear)
		}
		t.Logf("%-45q ep %-4d %s", p.Title, p.Episode, got)
	}
}

func TestLiveSeasons(t *testing.T) {
	d := &Daemon{al: &anilist.Client{}}
	files := []string{
		"/a/Mushoku Tensei S02E05 1080p WEB H.264-VARYG.mkv",
		"/a/[Judas] Kimetsu no Yaiba S3 - 03.mkv",
		"/a/Re Zero kara Hajimeru Isekai Seikatsu S03E01.mkv",
		"/a/[SubsPlease] Spy x Family Season 2 - 01 (1080p).mkv",
		"/a/[SubsPlease] Oshi no Ko S2 - 01 (1080p).mkv",
		"/a/Sousou no Frieren S2 - 01.mkv",
		"/a/Kaguya-sama wa Kokurasetai Season 2 - 04.mkv",
	}
	ctx := context.Background()
	for _, f := range files {
		p := media.ParsePath(f)
		a, _ := d.al.Search(ctx, fmt.Sprintf("%s season %d", p.Title, p.Season))
		b, _ := d.al.Search(ctx, p.Title)
		pick := seasonPick(p.Title, p.Season, mergeMedia(a, b))
		got := "→ (ask user)"
		if pick != nil {
			got = fmt.Sprintf("→ %s (%d)", pick.Title.Best(), pick.SeasonYear)
		}
		t.Logf("%-40q S%d E%-3d %s", p.Title, p.Season, p.Episode, got)
	}
}
