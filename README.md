# anisync

[![build](https://github.com/evansunde/anisync/actions/workflows/build.yml/badge.svg)](https://github.com/evansunde/anisync/actions/workflows/build.yml)

A small background daemon that updates **AniList** and **MyAnimeList** (at the same time) when you watch anime in a local player (mpv, VLC, Celluloid, Haruna, …), and **VNDB** when you play visual novels. Works offline: updates are queued and sent when you're back online.

- **Works on any desktop.** It uses MPRIS over D-Bus instead of compositor window titles, so niri, Hyprland, Plasma, GNOME and Sway behave the same. It also gets the real file path, not a truncated window title.
- **Near-zero resource use.** It reacts to player events and never scans your library. The only periodic work is one position read every 30 s while an episode is playing. Measured at 0 CPU ticks (under 10 ms) over 30 s of playback and about 9 MB of RAM. One static binary, no runtime dependencies.
- **Counts what you actually watched, using the player's own position.** By default it needs 80% of the episode watched. Coverage is anchored to the position the player reports (read on play, pause, rate change, and every 30 s while playing). Parts you seek past don't count, and buffering or stalls don't inflate it. The clock is only a fallback for players that don't report a position. Progress carries over if you stop and resume the file another day.
- **Offline queue.** Episodes watched offline are stored on disk. They're sent as soon as NetworkManager reports connectivity, or by backoff retry otherwise. If AniList succeeded and MAL was down, only MAL is retried.
- **Asks when unsure, instead of guessing.** If a title is ambiguous (a remake, a subtitled season, an unknown show), you get a notification with buttons for the top matches, or you can run `anisync resolve` for the full list and a custom search. Your choice is remembered for every later episode.
- **Shows that aren't in your list** are added as Watching (configurable).
- **Season and episode handling.** `S02E05`, `Season 2`, `2nd Season` and `II` find the right entry. Absolute numbering (`Show - 30` where season 1 has 28 episodes) rolls over into the sequel. Recaps (`12.5`), NCOP/NCED and short clips are ignored.
- **Status rules.** Your list never goes backwards, completion is set on the final episode, start and finish dates are filled in, and rewatches are supported. `anisync undo` reverts a wrong update on both sites.
- **TUI (`anisync tui`).** Your list (works offline from a local copy), what's playing now with "mark watched" and "change match", and AniList search with a clickable filter editor. Details include **cover art in kitty/Ghostty**, rankings, genres, tags, related shows and recommendations, all clickable. It works with the mouse or the keyboard.
- **Local copy of your list.** It's refreshed every 6 hours, and queued (offline) changes are applied to it right away, so both the TUI and title matching work offline.
- **VNDB (optional, off by default).** It detects a running VN from your VN folders (native or Wine/Proton), marks it *Playing*, tracks playtime, and can write it into your list notes. `anisync vn finish -vote 8.5` marks it finished.
- **VN mode in the TUI.** Press `v` to switch the TUI to visual novels: your VNDB list by your own labels (custom ones included) with local playtime, running VNs with "finish" and "change match", and VNDB search with the same filter chips and editor. Covers flagged as sexual or brutal are hidden by default.

## Install

**Prebuilt (x86_64 / arm64, static):** download `anisync-linux-<arch>.tar.gz` from [Releases](https://github.com/evansunde/anisync/releases), then:

```bash
tar -xzf anisync-linux-amd64.tar.gz && install -Dm755 anisync ~/.local/bin/anisync && install -Dm644 anisync.service ~/.config/systemd/user/anisync.service && anisync init
```

**From source:**

Needs Go ≥ 1.24 to build (or use the prebuilt x86_64 binary).

```bash
git clone https://github.com/evansunde/anisync && cd anisync
```

```bash
make install
```

Or, without cloning (binary only; then install `contrib/anisync.service` yourself):

```bash
go install github.com/evansunde/anisync/cmd/anisync@latest
```

`make install` puts the binary in `~/.local/bin`, installs the systemd user unit, and writes `~/.config/anisync/config.toml`.

**mpv needs the MPRIS plugin.** VLC, Celluloid, Haruna, SMPlayer and mpc-qt have MPRIS built in.

| distro | package |
|---|---|
| Arch | `mpv-mpris` |
| Fedora | `mpv-mpris` |
| Debian/Ubuntu | `mpv-mpris` |
| NixOS | `mpvScripts.mpris` |

## Set up accounts

AniList and MAL require you to register your own (free) API client.

**AniList**
1. Go to https://anilist.co/settings/developer and click *Create New Client*. Set the redirect URL to `https://anilist.co/api/v2/oauth/pin`.
2. Put the client ID in `[anilist] client_id` in the config.
3. Run `anisync auth anilist`, approve in the browser, and paste the token shown. The token lasts 1 year, and you'll get a notification before it expires.

**MyAnimeList**
1. Go to https://myanimelist.net/apiconfig and click *Create ID*. Set *App Type* to `other` and the redirect URL to `http://localhost:8789/callback`.
2. Put the client ID (and the secret, if one is shown) in `[mal]`.
3. Run `anisync auth mal` and approve in the browser. Tokens refresh automatically.

**VNDB** (optional)
1. Set `[vndb] enabled = true` and `dirs = ["~/Games/VN"]`. Each VN should be in its own subfolder.
2. Create a token at https://vndb.org/u/tokens with *list read* and *list write* permissions.
3. Run `anisync auth vndb`.

When `[vndb]` is disabled, nothing VN-related runs at all.

Start the daemon:

```bash
systemctl --user daemon-reload
```

```bash
systemctl --user enable --now anisync
```

If you don't use systemd user services, start `anisync daemon` from your compositor's autostart instead, e.g. `exec-once = anisync daemon` in Hyprland or `spawn-at-startup "anisync" "daemon"` in niri.

## Usage

Just watch things. When the threshold is reached you get a notification such as *"Sousou no Frieren — episode 5/28 · AniList: progress 4 → 5 · MAL: progress 4 → 5"*.

```
anisync tui [vn]          search, details with covers, your list, pick matches (see below); vn starts in VN mode
anisync status            services, what each player is playing and how much is watched, queued updates
anisync resolve           choose the right show for titles that couldn't be matched automatically
anisync map "Show" https://anilist.co/anime/123 [-season 2] [-offset -12]
                          pin a file title to a show yourself (e.g. weird release names)
anisync forget "Show"     drop a wrong mapping so the next episode is matched again
anisync mappings          list remembered title → show mappings
anisync history [N]       recent updates (with IDs)
anisync undo [ID]         revert the last (or given) update on AniList and MAL (or a VNDB list change)
anisync sync              retry the queue now and refresh your lists
anisync vn list           visual novels with playtime
anisync vn finish [name] [-vote 8.5]
anisync vn tui            the TUI in visual novel mode
```

`resolve` accepts a number, `s` to search another title, `i` to paste an AniList ID/URL or a MAL URL, or `x` to always ignore that title (e.g. non-anime videos).

## TUI

```bash
anisync tui
```

It's handy bound to a key as a floating terminal, e.g. in niri:

```
bind "Mod+A" { spawn "kitty" "--class" "anisync" "anisync" "tui"; }
```

Everything is keyboard- **and** mouse-driven: click rows, tabs, chips, buttons, genres, tags and related shows; the wheel scrolls whichever pane is under the pointer, one step per notch. Hold shift to select text.

**1 · My list** shows your list from the **local copy**, so it opens instantly and works offline. Sections are Watching, Rewatching, Planning, Completed, Paused, Dropped and All, with:
- a progress bar and your score
- for airing shows, "+2 new" (aired episodes you haven't watched) and when the next one airs
- "⇅" on changes that haven't synced yet

`h`/`l` switch sections, `/` filters, `S` sorts (recently updated, title, your score, average score, airing next).

**2 · Now** shows what each player is playing: the file, what was detected (title, episode, season), which show it's matched to, and a watched bar with the threshold.
- `u` **marks it watched right now**, however much you've watched.
- `m` **changes the match**: it opens a search, you pick the right show, and episodes waiting on that title are applied.

Below that are titles waiting for you to pick (`enter` to search, `x` to ignore) and queued updates (with "offline" when it can't reach the services).

**3 · Search** is live AniList search.
- **Filter chips** sit under the search box. Click a chip to edit it, click × to remove it, and click the sort chip to cycle the sort (right-click goes backwards).
- **Filter editor (`f`):**
  - sort, format, status, season
  - year / score / episode ranges (type `2018-2024`, `>7.5`, `<13`)
  - minimum popularity
  - genres and tags in a picker: type to narrow it, then space or click cycles **include → exclude → off**
  - source material, country (JP/CN/KR/TW), on my list, adult
- Results update live as you change filters, and the editor and the typed query always stay in sync, so you can type part of it and click the rest.
- Rows show rank position, format, season, episodes, score (coloured), popularity and your status.

The same query syntax can be typed directly:

```
frieren                              title
isekai g:action -g:ecchi             genres must all match; - excludes
t:"time skip" -t:harem               tags (same)
f:tv,movie,ova,ona,special,short     format
y:2023   y:2015-2020   y:>2018       year
s:fall y:2025                        season
st:airing|finished|upcoming          status
score>75   score:7.5-9   eps<13      score (0-100 or 0-10), episodes
pop>50k   src:manga,ln   c:cn        popularity, source, country
sort:score|pop|trending|fav|new|old|title|eps|worst|obscure
list:no                              only shows that aren't on your list
```

**Details** appear on the right of every tab, or full-screen on narrow terminals:
- the cover
- titles, format, episodes × length, season, studio, source, air dates
- score, mean, popularity, favourites, rankings (e.g. "★ #1 highest rated 2023")
- your entry with a progress bar, next-episode countdown, and catch-up count
- genres, tags (no spoilers) and description
- **Related** shows and **If you liked this** recommendations

**Genres, tags, related shows and recommendations are links.** Click them, or press `enter` and use `j`/`k` then `enter`.
- A **genre or tag** starts a search for it. ctrl/alt-click or `A` adds it to your current filters instead.
- A **show** opens in place, and `esc` goes back.

| key | action |
|---|---|
| `1` `2` `3` / tab | My list · Now · Search |
| `j` `k` `g` `G` `pgup` `pgdn` | move (more search results load as you scroll) |
| `enter` | details: then `j`/`k` links, `enter` open, `A` add to search, `esc` back |
| `s` | status, or add to your list: `w` watching, `p` planning, `c` completed, `h` paused, `d` dropped, `r` rewatching, `x` remove |
| `+` `-` · `e` · `r` | episode ±1 (the last episode completes it) · set episodes · your score (0–10) |
| `/` · `f` · `S` · `X` | type a query / filter the list · filter editor · sort · clear filters |
| `u` · `m` | (Now) mark watched now · change match |
| `J` `K` | scroll details · `o`/`O` open on AniList/MAL · `ctrl+r` sync · `?` help · `q` quit |
| `v` | switch between anime and visual novels (see [VN mode](#vn-mode-vndb)) |

Edits go **through the daemon**:
- They show in your local list immediately (⇅), are queued offline, and are applied to AniList and MAL together.
- `anisync undo` works on them too.
- The daemon must be running to edit; browsing and your local list work without it.
- MAL only stores whole-number scores, so 8.5 becomes 9 there.

**Covers** are drawn with the kitty graphics protocol's Unicode placeholders.
- This works in kitty ≥ 0.28 and Ghostty, but not inside tmux.
- They're cached in `~/.cache/anisync/covers`.
- Turn them off with `covers = false`, or force them with `ANISYNC_COVERS=1`/`0`.

**Your list** is read from AniList if you're logged in there, otherwise from MAL through the daemon. It's stored in `~/.local/state/anisync/list.json`, and the daemon refreshes it every 6 hours or when you press `ctrl+r`. Your VNDB list is stored the same way in `vnlist.json` next to it.

### VN mode (VNDB)

Press `v`, or click **Anime │ VN** in the header, to switch the whole TUI to visual novels. `anisync tui vn` starts in it. The three tabs and the detail panel work the same way as for anime.

**1 · My list** is your VNDB list.
- Sections come from **your labels**: Playing, Finished, Stalled, Dropped, Wishlist, then your custom labels (and Blacklist/Voted when you use them), then All.
- Rows show the label, your vote, the VNDB rating, the length, **playtime tracked by anisync** (⏱) and your start/finish dates.
- It's read from a local copy, `~/.local/state/anisync/vnlist.json`, so it opens instantly and works offline. The daemon owns the file like the anime list, refreshes it every 6 hours or on `ctrl+r`, and applies queued edits to it right away (⇅).
- `h`/`l` switch labels, `/` filters (title or developer), `S` sorts (recently updated, title, your vote, rating, released, playtime).

**2 · Now** shows running visual novels from your `[vndb] dirs`: the folder, the VN it's matched to, this session's length and your total playtime.
- `f` **marks it finished** and asks for your vote (enter skips the vote).
- `m` **changes the match**: it opens a VNDB search, you pick the right VN, and queued updates follow.
- VN folders that couldn't be matched are listed below; `enter` picks one, `x` ignores it.

**3 · Search** is live VNDB search with the same chips and filter editor (`f`): sort, tags (include/exclude), length, release year, rating, votes, language, original language, platform and developer.

```
summer pockets                       title (any language or alias)
t:"male protagonist" -t:nukige       tags (all must match); - excludes
len:short,medium                     length: vshort short medium long vlong
y:2015-2020   y:>2018                release year
rating>7.5   rating:70-90            VNDB rating (0-10 or 10-100)
votes>100                            number of votes
lang:en,ja   ol:ja                   available languages · original language
p:win,lin                            platform: win lin mac web and ios swi ps4 ps5 psv …
dev:key   dev:"type-moon"            developer
sort:match|rating|votes|new|old|title|worst|obscure
```

**Details** show the cover, titles, developers, release date, rating and votes, length, platforms and languages, your labels, vote, dates, playtime and notes, tags (no spoilers), the description (VNDB formatting and spoilers stripped) and related VNs.
- **Tags and related VNs are links.** A tag starts a search (ctrl/alt-click or `A` adds it to your filters), and a related VN opens in place (`esc` goes back).

| key (VN mode) | action |
|---|---|
| `s` | label picker: `p` Playing, `f` Finished, `s` Stalled, `d` Dropped, `w` Wishlist (these replace each other), `1`–`9` toggle your custom labels, `x` remove from list |
| `r` | your vote (1–10, e.g. 8.5; `0` clears) |
| `e` · `E` | started · finished date (`YYYY-MM-DD`, `today`, or `-` to clear) |
| `o` | open on vndb.org |

Setting *Playing* fills in the start date and *Finished* the finish date, if they're empty. VN edits go through the daemon queue like anime edits: they work offline and `anisync undo` reverts them. The daemon needs `[vndb] enabled = true` and `anisync auth vndb`.

**VN covers** follow `vn_covers` in the config:
- `"safe"` (default) hides covers that VNDB rates as sexual (≥ 1, suggestive or explicit) or brutal (violence 2). A placeholder is shown instead, and tags in the *ero* category are left out of the tag list.
- `"all"` shows every cover and tag.
- `"off"` shows no VN covers.

## How it decides

**Detection.** The daemon listens for MPRIS players whose bus name matches `players` in the config. Browsers aren't in that list by default. For each file it records which *position ranges* actually played:
- Spans start at the position the player reports when playback starts or after a seek.
- They're closed at the player's reported position on pause, rate change, and a check every 30 s while playing.
- If the player jumps without announcing a seek, that uncertain interval isn't counted.
- If a player doesn't report its position, the elapsed time × playback rate (measured with a monotonic clock) is used instead.

A file counts once the covered range reaches `threshold` × length. Files shorter than `min_length`, and anything that isn't a video file, are ignored. You can restrict tracking to `library_dirs` or exclude paths with `ignore_paths`.

**Parsing.** Release names are parsed with an Anitomy port (group tags, resolution, CRC, `S02E05`, `- 05v2`…). When the file name has no title (`05.mkv`, `Episode 05.mkv`), the folder name is used, and a `Season 2` folder sets the season. Streams (ani-cli etc.) use the player's media title and are only updated on a confident match. They never prompt you.

**Matching**, in order:
1. A mapping you chose or one remembered earlier.
2. An exact title match (romaji, English, native or synonyms) on AniList. Unaired entries and movies are excluded when the episode number is above 1.
3. If several shows share the title (remakes like *Hunter x Hunter*, *Fruits Basket*), it picks only the one that's active on your list. Otherwise it asks you.
4. For seasons, it looks for an entry titled *Season N*, *Nth Season*, *N* or a roman numeral. Split-cour "Part 2" entries are reached through episode overflow instead.
5. If none of these give a confident match, you get a notification and the title waits in `anisync resolve`. Further episodes of that title queue up behind it and go through once you've chosen.

MAL IDs come from AniList, so a single match updates both sites.

## Files

| path | contents |
|---|---|
| `~/.config/anisync/config.toml` | settings (see comments inside) |
| `~/.config/anisync/tokens.json` | login tokens (mode 600) |
| `~/.local/state/anisync/state.json` | queue, mappings, pending choices, history, watch coverage, VN playtime |
| `~/.local/state/anisync/list.json` | local copy of your list (offline view and matching) |
| `~/.local/state/anisync/vnlist.json` | local copy of your VNDB list (VN mode) |
| `~/.cache/anisync/` | TUI cover images, genre/tag names, VNDB tag names |

Logs: `journalctl --user -u anisync -f`

## Troubleshooting

- **The player doesn't show up in `anisync status`.** Run `busctl --user list | grep mpris`. If mpv isn't listed, install mpv-mpris. If your player has a different name, add a substring of it to `players`.
- **"not tracked (filtered)"** means the file is shorter than `min_length`, isn't a video file, or is outside `library_dirs`/inside `ignore_paths`.
- **Wrong show was matched.** Run `anisync undo`, then `anisync forget "<title>"`. The next episode will ask again, or use `anisync map` to pin it directly.
- **Nothing updates while offline.** That's expected. `anisync status` shows the queue and when the next retry happens.

## Development

CI (`.github/workflows/build.yml`) runs vet, the unit tests and the end-to-end tests on every push and pull request, and builds static x86_64 and arm64 binaries. Pushing a tag like `v0.1.0` publishes them as a GitHub release with SHA256 checksums:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

```bash
make test
```

```bash
make e2e
```

`make e2e` runs the real daemon on a private D-Bus session, with a fake MPRIS player and mock AniList/MAL/VNDB servers. There's also `go test -tags live ./internal/daemon/ -run Live -v`, which checks matching against the real AniList API, and `go test -tags live ./internal/vndb/ -v`, which checks VN search, details, tags and labels against the real VNDB API.
