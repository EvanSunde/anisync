// Package vn detects running visual novels by looking for processes whose
// executable lives inside one of the configured VN folders (native or Wine).
//
// Cost per scan: one readdir of /proc; only PIDs not seen before have their
// cmdline read, so the steady state is a few microseconds of work.
package vn

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Scanner struct {
	Dirs       []string
	Interval   time.Duration
	MinSession time.Duration
	OnStart    func(key, folder string)                  // session reached MinSession
	OnStop     func(key, folder string, d time.Duration) // only for started sessions

	procRoot string
	seen     map[int]*pidInfo
	mu       sync.Mutex // guards active (Active is called from other goroutines)
	active   map[string]*session
	self     int
}

type pidInfo struct {
	key    string // VN folder name, "" if not a VN
	checks int
	alive  bool
}

type session struct {
	start, last time.Time
	started     bool
}

func New(dirs []string) *Scanner {
	var clean []string
	for _, d := range dirs {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			d = r
		}
		clean = append(clean, filepath.Clean(d))
	}
	return &Scanner{Dirs: clean, procRoot: "/proc", seen: map[int]*pidInfo{}, active: map[string]*session{}, self: os.Getpid()}
}

func (s *Scanner) Run(stop <-chan struct{}) {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	s.Scan(time.Now())
	for {
		select {
		case <-stop:
			s.endAll(time.Now())
			return
		case now := <-t.C:
			s.Scan(now)
		}
	}
}

// Scan runs one detection pass.
func (s *Scanner) Scan(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ents, err := os.ReadDir(s.procRoot)
	if err != nil {
		return
	}
	for _, p := range s.seen {
		p.alive = false
	}
	running := map[string]bool{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == s.self {
			continue
		}
		info := s.seen[pid]
		if info == nil {
			info = &pidInfo{}
			s.seen[pid] = info
		}
		info.alive = true
		// Re-check new negatives once: Wine re-execs and rewrites its cmdline.
		if info.key == "" && info.checks < 2 {
			info.key = s.classify(pid)
			info.checks++
		}
		if info.key != "" {
			running[info.key] = true
		}
	}
	for pid, p := range s.seen {
		if !p.alive {
			delete(s.seen, pid)
		}
	}
	for key := range running {
		ss := s.active[key]
		if ss == nil {
			ss = &session{start: now}
			s.active[key] = ss
		}
		ss.last = now
		if !ss.started && now.Sub(ss.start) >= s.MinSession {
			ss.started = true
			if s.OnStart != nil {
				s.OnStart(key, key)
			}
		}
	}
	for key, ss := range s.active {
		if !running[key] {
			s.end(key, ss)
		}
	}
}

func (s *Scanner) end(key string, ss *session) {
	delete(s.active, key)
	if ss.started && s.OnStop != nil {
		s.OnStop(key, key, ss.last.Sub(ss.start))
	}
}

func (s *Scanner) endAll(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, ss := range s.active {
		ss.last = now
		s.end(key, ss)
	}
}

// Active returns VN folders currently running with their session length.
func (s *Scanner) Active() map[string]time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]time.Duration{}
	for k, ss := range s.active {
		out[k] = ss.last.Sub(ss.start)
	}
	return out
}

var exeExt = map[string]bool{".exe": true, ".sh": true, ".x86_64": true, ".x86": true, ".py": true, ".appimage": true, ".bat": true}

func (s *Scanner) classify(pid int) string {
	dir := filepath.Join(s.procRoot, strconv.Itoa(pid))
	raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil || len(raw) == 0 {
		return ""
	}
	args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	if len(args) == 1 && strings.Contains(args[0], " ") && strings.Contains(strings.ToLower(args[0]), ".exe") {
		// some Wine processes rewrite argv into one space-joined string
		args = append(args, splitExe(args[0])...)
	}
	isWine := false
	for _, a := range args {
		p := WinePath(a)
		if strings.HasSuffix(strings.ToLower(p), ".exe") {
			isWine = true
		}
		if key := s.under(p); key != "" && looksExecutable(p) {
			return key
		}
	}
	// Wine processes often only show "C:\...\game.exe"; their working
	// directory is usually the game folder.
	if isWine {
		if cwd, err := os.Readlink(filepath.Join(dir, "cwd")); err == nil {
			return s.under(cwd)
		}
	}
	return ""
}

// under returns the VN folder name if p is inside one of the VN dirs.
func (s *Scanner) under(p string) string {
	if p == "" || p[0] != '/' {
		return ""
	}
	p = filepath.Clean(p)
	for _, d := range s.Dirs {
		if strings.HasPrefix(p, d+"/") {
			rest := p[len(d)+1:]
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				return rest[:i]
			}
			return rest // the folder itself (cwd case)
		}
	}
	return ""
}

func looksExecutable(p string) bool {
	if exeExt[strings.ToLower(filepath.Ext(p))] {
		return true
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

// WinePath converts "Z:\home\me\x.exe" to "/home/me/x.exe".
func WinePath(a string) string {
	if len(a) >= 3 && (a[0] == 'Z' || a[0] == 'z') && a[1] == ':' && (a[2] == '\\' || a[2] == '/') {
		return strings.ReplaceAll(a[2:], `\`, "/")
	}
	return a
}

func splitExe(s string) []string {
	l := strings.ToLower(s)
	if i := strings.Index(l, ".exe"); i > 0 {
		return []string{s[:i+4]}
	}
	return nil
}
