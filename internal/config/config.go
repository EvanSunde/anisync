// Package config loads ~/.config/anisync/config.toml and resolves XDG paths.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

type Config struct {
	// Fraction of an episode that must actually be watched (seeked-over parts
	// don't count) before it is marked as watched.
	Threshold float64 `toml:"threshold"`
	// Substrings matched (case-insensitive) against the MPRIS bus name, e.g.
	// "mpv" matches org.mpris.MediaPlayer2.mpv.instance1234.
	Players []string `toml:"players"`
	// If non-empty, only local files under these directories are tracked.
	LibraryDirs []string `toml:"library_dirs"`
	// Paths containing any of these substrings are never tracked.
	IgnorePaths []string `toml:"ignore_paths"`
	// Track network streams (ani-cli etc.) using the player's title.
	Streams bool `toml:"streams"`
	// Videos shorter than this (OP/ED files, trailers, music) are ignored.
	MinLength Duration `toml:"min_length"`
	// Add shows to your list when they aren't in it yet.
	AddToList bool `toml:"add_to_list"`
	// Watching episode 1 of a completed show starts a rewatch.
	Rewatch bool `toml:"rewatch"`
	// Desktop notifications.
	Notify        bool `toml:"notify"`
	NotifyUpdates bool `toml:"notify_updates"`
	// Cover art in the TUI (kitty / Ghostty graphics protocol).
	Covers bool `toml:"covers"`
	// VN covers in the TUI: "safe" hides covers VNDB rates as sexual or
	// brutal, "all" shows every cover, "off" shows none.
	VNCovers string `toml:"vn_covers"`

	AniList struct {
		ClientID string `toml:"client_id"`
		Enabled  bool   `toml:"enabled"`
	} `toml:"anilist"`
	MAL struct {
		ClientID     string `toml:"client_id"`
		ClientSecret string `toml:"client_secret"`
		RedirectPort int    `toml:"redirect_port"`
		Enabled      bool   `toml:"enabled"`
	} `toml:"mal"`
	VNDB struct {
		Enabled bool `toml:"enabled"`
		// Folders that contain one sub-folder per visual novel.
		Dirs         []string `toml:"dirs"`
		ScanInterval Duration `toml:"scan_interval"`
		// Sessions shorter than this don't count (e.g. opening the config tool).
		MinSession Duration `toml:"min_session"`
		// Keep a "Playtime: Xh Ym" line in your VNDB list notes up to date.
		PlaytimeNote bool `toml:"playtime_note"`
	} `toml:"vndb"`
}

func Default() Config {
	var c Config
	c.Threshold = 0.8
	c.Players = []string{"mpv", "vlc", "celluloid", "haruna", "smplayer", "mpc-qt", "mpcqt", "dragonplayer", "totem", "showtime", "clapper", "jellyfin"}
	c.Streams = true
	c.MinLength = Duration{3 * time.Minute}
	c.AddToList = true
	c.Notify = true
	c.NotifyUpdates = true
	c.Covers = true
	c.VNCovers = "safe"
	c.AniList.Enabled = true
	c.MAL.Enabled = true
	c.MAL.RedirectPort = 8789
	c.VNDB.Enabled = false // opt-in: no process scanning at all unless enabled
	c.VNDB.ScanInterval = Duration{30 * time.Second}
	c.VNDB.MinSession = Duration{2 * time.Minute}
	return c
}

func ConfigDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "anisync")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "anisync")
}

func DataDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "anisync")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "state", "anisync")
}

func SocketPath() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "anisync.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("anisync-%d.sock", os.Getuid()))
}

func Path() string { return filepath.Join(ConfigDir(), "config.toml") }

// Load reads the config file; a missing file yields defaults.
func Load() (Config, error) {
	c := Default()
	_, err := toml.DecodeFile(Path(), &c)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return c, fmt.Errorf("%s: %w", Path(), err)
	}
	if c.Threshold <= 0 || c.Threshold > 1 {
		return c, fmt.Errorf("threshold must be in (0,1], got %v", c.Threshold)
	}
	switch c.VNCovers {
	case "safe", "all", "off":
	case "":
		c.VNCovers = "safe"
	default:
		return c, fmt.Errorf("vn_covers must be \"safe\", \"all\" or \"off\", got %q", c.VNCovers)
	}
	if c.VNDB.ScanInterval.Duration < 5*time.Second {
		c.VNDB.ScanInterval.Duration = 5 * time.Second
	}
	for i, d := range c.LibraryDirs {
		c.LibraryDirs[i] = ExpandHome(d)
	}
	for i, d := range c.VNDB.Dirs {
		c.VNDB.Dirs[i] = ExpandHome(d)
	}
	return c, nil
}

func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[1:])
	}
	return p
}

// WriteDefault writes an annotated config file if none exists.
func WriteDefault() (bool, error) {
	if _, err := os.Stat(Path()); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(ConfigDir(), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(Path(), []byte(Template), 0o644)
}

const Template = `# anisync configuration

# Fraction of an episode you must actually watch (seeking past parts doesn't
# count; progress carries over if you resume a file later).
threshold = 0.8

# MPRIS players to track (substring of the bus name, case-insensitive).
# mpv needs the mpv-mpris plugin; VLC, Celluloid, Haruna have MPRIS built in.
players = ["mpv", "vlc", "celluloid", "haruna", "smplayer", "mpc-qt", "mpcqt", "dragonplayer", "totem", "showtime", "clapper", "jellyfin"]

# Only track files under these folders (empty = everything the players open).
library_dirs = []
# Never track paths containing any of these substrings.
ignore_paths = []

# Track network streams (ani-cli, jellyfin, ...) by the player's media title.
streams = true
# Ignore anything shorter than this (OP/ED files, trailers, music videos).
min_length = "3m"

# Add the show to your list if it's not in it yet.
add_to_list = true
# Watching episode 1 of a completed show starts a rewatch.
rewatch = false

notify = true          # desktop notifications (pending matches, errors)
notify_updates = true  # also notify on every successful update

# Cover art in "anisync tui" (kitty or Ghostty; detected automatically).
covers = true
# Visual novel covers: "safe" hides covers VNDB flags as sexual or brutal
# (shows a placeholder), "all" shows every cover, "off" shows none.
vn_covers = "safe"

[anilist]
enabled = true
# Create a client at https://anilist.co/settings/developer with redirect URL
#   https://anilist.co/api/v2/oauth/pin
client_id = ""

[mal]
enabled = true
# Create an app at https://myanimelist.net/apiconfig (App Type: other) with
# redirect URL http://localhost:8789/callback
client_id = ""
client_secret = ""
redirect_port = 8789

[vndb]
# Off by default. When disabled nothing VN-related runs at all (no scanning).
enabled = false
# Folders containing one sub-folder per visual novel. A running process whose
# command line or working directory is inside one of them counts as playing it.
dirs = []
scan_interval = "30s"
min_session = "2m"
# Maintain a "Playtime: Xh Ym" line in your VNDB list notes.
playtime_note = false
`
