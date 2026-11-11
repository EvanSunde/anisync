package anilist

import (
	"context"
	"html"
	"regexp"
	"strings"
)

// Filter is an AniList anime search. Zero values mean "no filter".
type Filter struct {
	Search    string
	Genres    []string // must have all
	NotGenres []string
	Tags      []string // must have all
	NotTags   []string
	Formats   []string // TV, TV_SHORT, MOVIE, SPECIAL, OVA, ONA, MUSIC
	Status    string   // FINISHED, RELEASING, NOT_YET_RELEASED, CANCELLED, HIATUS
	Season    string   // WINTER, SPRING, SUMMER, FALL
	Year      int
	YearFrom  int
	YearTo    int
	ScoreMin  int
	ScoreMax  int
	EpsMin    int
	EpsMax    int
	Sources   []string // MediaSource: ORIGINAL, MANGA, LIGHT_NOVEL, ...
	Country   string   // JP, CN, KR, TW
	PopMin    int
	Sort      []string // MediaSort values
	OnList    *bool
	Adult     bool
}

// Empty reports whether no filter (other than text/sort) is set.
func (f Filter) Empty() bool {
	return len(f.Genres)+len(f.NotGenres)+len(f.Tags)+len(f.NotTags)+len(f.Formats)+len(f.Sources) == 0 &&
		f.Status == "" && f.Season == "" && f.Year == 0 && f.YearFrom == 0 && f.YearTo == 0 && f.ScoreMin == 0 &&
		f.ScoreMax == 0 && f.EpsMin == 0 && f.EpsMax == 0 && f.Country == "" && f.PopMin == 0 && f.OnList == nil && !f.Adult
}

func (f Filter) vars(page int) map[string]any {
	v := map[string]any{"page": page, "isAdult": f.Adult}
	set := func(k string, val any, ok bool) {
		if ok {
			v[k] = val
		}
	}
	set("search", f.Search, f.Search != "")
	set("genre_in", f.Genres, len(f.Genres) > 0)
	set("genre_not_in", f.NotGenres, len(f.NotGenres) > 0)
	set("tag_in", f.Tags, len(f.Tags) > 0)
	set("tag_not_in", f.NotTags, len(f.NotTags) > 0)
	set("format_in", f.Formats, len(f.Formats) > 0)
	set("status", f.Status, f.Status != "")
	set("season", f.Season, f.Season != "")
	set("seasonYear", f.Year, f.Year > 0)
	// FuzzyDateInt is YYYYMMDD; greater/lesser are exclusive
	set("startDate_greater", f.YearFrom*10000-1, f.YearFrom > 0)
	set("startDate_lesser", (f.YearTo+1)*10000, f.YearTo > 0)
	set("averageScore_greater", f.ScoreMin-1, f.ScoreMin > 0)
	set("averageScore_lesser", f.ScoreMax+1, f.ScoreMax > 0)
	set("episodes_greater", f.EpsMin-1, f.EpsMin > 0)
	set("episodes_lesser", f.EpsMax+1, f.EpsMax > 0)
	set("onList", f.OnList, f.OnList != nil)
	set("source_in", f.Sources, len(f.Sources) > 0)
	set("countryOfOrigin", f.Country, f.Country != "")
	set("popularity_greater", f.PopMin-1, f.PopMin > 0)
	sort := f.Sort
	if len(sort) == 0 {
		if f.Search != "" {
			sort = []string{"SEARCH_MATCH"}
		} else {
			sort = []string{"POPULARITY_DESC"}
		}
	}
	v["sort"] = sort
	return v
}

const browseFields = `id idMal title{romaji english native} synonyms format status episodes duration season seasonYear
averageScore popularity genres coverImage{large color} rankings{rank type allTime season year context}
nextAiringEpisode{episode timeUntilAiring airingAt} studios(isMain:true){nodes{name}}`

type Page struct {
	Media   []Media
	HasNext bool
	Total   int
}

// Browse runs a filtered search. With a token, results include the
// viewer's list entry.
func (c *Client) Browse(ctx context.Context, f Filter, page int) (Page, error) {
	fields := browseFields
	if c.Token != "" {
		fields += ` mediaListEntry{id status progress repeat score(format:POINT_100)}`
	}
	q := `query($page:Int,$search:String,$genre_in:[String],$genre_not_in:[String],$tag_in:[String],$tag_not_in:[String],
$format_in:[MediaFormat],$status:MediaStatus,$season:MediaSeason,$seasonYear:Int,$startDate_greater:FuzzyDateInt,
$startDate_lesser:FuzzyDateInt,$averageScore_greater:Int,$averageScore_lesser:Int,$episodes_greater:Int,$episodes_lesser:Int,
$sort:[MediaSort],$onList:Boolean,$isAdult:Boolean,$source_in:[MediaSource],$countryOfOrigin:CountryCode,$popularity_greater:Int){Page(page:$page,perPage:30){pageInfo{hasNextPage total}
media(type:ANIME,search:$search,genre_in:$genre_in,genre_not_in:$genre_not_in,tag_in:$tag_in,tag_not_in:$tag_not_in,
format_in:$format_in,status:$status,season:$season,seasonYear:$seasonYear,startDate_greater:$startDate_greater,
startDate_lesser:$startDate_lesser,averageScore_greater:$averageScore_greater,averageScore_lesser:$averageScore_lesser,
episodes_greater:$episodes_greater,episodes_lesser:$episodes_lesser,sort:$sort,onList:$onList,isAdult:$isAdult,
source_in:$source_in,countryOfOrigin:$countryOfOrigin,popularity_greater:$popularity_greater){` + fields + `}}}`
	var out struct {
		Page struct {
			PageInfo struct {
				HasNextPage bool `json:"hasNextPage"`
				Total       int  `json:"total"`
			} `json:"pageInfo"`
			Media []Media `json:"media"`
		} `json:"Page"`
	}
	err := c.gql(ctx, q, f.vars(page), &out)
	return Page{Media: out.Page.Media, HasNext: out.Page.PageInfo.HasNextPage, Total: out.Page.PageInfo.Total}, err
}

// Detail fetches everything shown on the detail pane.
func (c *Client) Detail(ctx context.Context, id int) (*Media, error) {
	q := `query($id:Int){Media(id:$id,type:ANIME){` + browseFields + ` meanScore favourites source description(asHtml:false)
siteUrl startDate{year month day} endDate{year month day} coverImage{extraLarge large color}
tags{name rank isMediaSpoiler} relations{edges{relationType node{id type format status episodes title{romaji english native}}}}
recommendations(perPage:6,sort:RATING_DESC){nodes{mediaRecommendation{id format title{romaji english native}}}}`
	if c.Token != "" {
		q += ` mediaListEntry{id status progress repeat score(format:POINT_100) startedAt{year month day} completedAt{year month day}}`
	}
	q += `}}`
	var out struct {
		Media *Media `json:"Media"`
	}
	if err := c.gql(ctx, q, map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	return out.Media, nil
}

// FullListItem is a list entry with display fields.
type FullListItem struct {
	Status    string    `json:"status"`
	Progress  int       `json:"progress"`
	Score     float64   `json:"score"`
	Repeat    int       `json:"repeat"`
	UpdatedAt int64     `json:"updatedAt"`
	StartedAt FuzzyDate `json:"startedAt"`
	Media     Media     `json:"media"`
}

// ListFull returns the user's anime list with details for display.
func (c *Client) ListFull(ctx context.Context, userID int) ([]FullListItem, error) {
	var out struct {
		Coll struct {
			Lists []struct {
				Entries []FullListItem `json:"entries"`
			} `json:"lists"`
		} `json:"MediaListCollection"`
	}
	err := c.gql(ctx, `query($u:Int){MediaListCollection(userId:$u,type:ANIME){lists{entries{status progress
score(format:POINT_100) repeat updatedAt startedAt{year month day} media{`+browseFields+`}}}}}`, map[string]any{"u": userID}, &out)
	var items []FullListItem
	for _, l := range out.Coll.Lists {
		items = append(items, l.Entries...)
	}
	return items, err
}

// Collections returns all genre names and (non-adult) tag names.
func (c *Client) Collections(ctx context.Context) (genres, tags []string, err error) {
	var out struct {
		Genres []string `json:"GenreCollection"`
		Tags   []struct {
			Name    string `json:"name"`
			IsAdult bool   `json:"isAdult"`
		} `json:"MediaTagCollection"`
	}
	if err = c.gql(ctx, `query{GenreCollection MediaTagCollection{name isAdult}}`, nil, &out); err != nil {
		return
	}
	for _, t := range out.Tags {
		if !t.IsAdult {
			tags = append(tags, t.Name)
		}
	}
	return out.Genres, tags, nil
}

var (
	reBR   = regexp.MustCompile(`(?i)<br\s*/?>`)
	reTag  = regexp.MustCompile(`<[^>]+>`)
	reBlnk = regexp.MustCompile(`\n{3,}`)
)

// PlainDescription strips AniList's HTML from a description.
func PlainDescription(s string) string {
	s = reBR.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reBlnk.ReplaceAllString(strings.ReplaceAll(s, "\r", ""), "\n\n")
	return strings.TrimSpace(s)
}
