package vn

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mkproc(t *testing.T, root string, pid int, cmdline, cwd string) {
	d := filepath.Join(root, itoa(pid))
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "cmdline"), []byte(cmdline), 0o644)
	if cwd != "" {
		os.Symlink(cwd, filepath.Join(d, "cwd"))
	}
}
func itoa(i int) string {
	return string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10))
}

func TestScanner(t *testing.T) {
	tmp := t.TempDir()
	vnDir := filepath.Join(tmp, "VN")
	os.MkdirAll(filepath.Join(vnDir, "Summer Pockets"), 0o755)
	os.MkdirAll(filepath.Join(vnDir, "Other"), 0o755)
	proc := filepath.Join(tmp, "proc")
	s := New([]string{vnDir})
	s.procRoot = proc
	s.MinSession = time.Minute
	var started, stopped []string
	var dur time.Duration
	s.OnStart = func(k, _ string) { started = append(started, k) }
	s.OnStop = func(k, _ string, d time.Duration) { stopped = append(stopped, k); dur = d }

	// shell sitting in the VN folder: must not count
	mkproc(t, proc, 101, "bash\x00", filepath.Join(vnDir, "Other"))
	// wine game launched with Z: path
	mkproc(t, proc, 102, "Z:"+filepath.FromSlash(filepath.Join(vnDir, "Summer Pockets", "SiglusEngine.exe"))+"\x00", "")
	t0 := time.Now()
	s.Scan(t0)
	s.Scan(t0.Add(30 * time.Second))
	if len(started) != 0 {
		t.Fatal("started before min session")
	}
	s.Scan(t0.Add(90 * time.Second))
	if len(started) != 1 || started[0] != "Summer Pockets" {
		t.Fatalf("started=%v", started)
	}
	os.RemoveAll(filepath.Join(proc, "102"))
	s.Scan(t0.Add(120 * time.Second))
	if len(stopped) != 1 || dur != 90*time.Second {
		t.Fatalf("stopped=%v dur=%v", stopped, dur)
	}
}

func TestWineCwd(t *testing.T) {
	tmp := t.TempDir()
	vnDir := filepath.Join(tmp, "VN")
	os.MkdirAll(filepath.Join(vnDir, "Muv-Luv"), 0o755)
	proc := filepath.Join(tmp, "proc")
	s := New([]string{vnDir})
	s.procRoot = proc
	mkproc(t, proc, 200, `C:\Program Files\MuvLuv\muvluv.exe`+"\x00", filepath.Join(vnDir, "Muv-Luv"))
	if k := s.classify(200); k != "Muv-Luv" {
		t.Fatalf("got %q", k)
	}
}
