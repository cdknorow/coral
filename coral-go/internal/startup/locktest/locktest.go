// Package locktest is the test scaffolding for #6388: a data folder another server holds, and the proof that a program that lost the instance lock left the folder as it found it. It is
// imported by tests only.
package locktest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/startup"
)

// Folder is an isolated data folder that looks used: looser than a start would leave it (0755, a 0644 file) and with a log, a setting file and a Terms marker, all with old times, held by the
// instance lock as a running server holds it.
type Folder struct {
	Dir  string
	Home string
	lock *startup.Instance
}

// New makes the folder under t.TempDir() and takes the lock on it. The lock is released with the test.
func New(t *testing.T) *Folder {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("modes are a Unix notion")
	}
	home := t.TempDir()
	dir := filepath.Join(home, "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for name, content := range map[string]string{"coral.log": "2026-10-20 an earlier run\n", "notes.txt": "kept", ".eula-accepted": "{}"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := startup.LockInstance(dir)
	if err != nil || lock == nil {
		t.Fatalf("the test's own lock: %v", err)
	}
	t.Cleanup(lock.Release)
	return &Folder{Dir: dir, Home: home, lock: lock}
}

// Snapshot is the state of the folder: its mode and, for each entry, its mode, size and modification time, and the bytes of coral.log.
type Snapshot struct {
	Entries map[string]string
	Log     []byte
}

func (f *Folder) Snapshot(t *testing.T) Snapshot {
	t.Helper()
	s := Snapshot{Entries: map[string]string{}}
	fi, err := os.Stat(f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Entries["."] = fi.Mode().String()
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "coral-server.lock" {
			continue // the lock file is the lock's own: its contents are the holder's pid
		}
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		s.Entries[e.Name()] = info.Mode().String() + " " + info.ModTime().String() + " " + string(rune('0'+info.Size()%10))
	}
	s.Log, _ = os.ReadFile(filepath.Join(f.Dir, "coral.log"))
	return s
}

// Env is the environment a program under test runs in: this folder as its data folder and its own home, nothing of the operator's.
func (f *Folder) Env(port string) []string {
	return append(os.Environ(), "CORAL_DATA_DIR="+f.Dir, "HOME="+f.Home, "USERPROFILE="+f.Home, "CORAL_NO_DEMO_SEED=1", "CORAL_PORT="+port, "CORAL_CLOUD_URL=")
}

// Equal reports whether two snapshots are the same, and what differs.
func Equal(a, b Snapshot) (bool, string) {
	for k, v := range a.Entries {
		if b.Entries[k] != v {
			return false, "entry " + k + ": " + v + " became " + b.Entries[k]
		}
	}
	for k := range b.Entries {
		if _, ok := a.Entries[k]; !ok {
			return false, "a new entry: " + k
		}
	}
	if !bytes.Equal(a.Log, b.Log) {
		return false, "coral.log changed"
	}
	return true, ""
}

// Build builds the main package of dir (a cmd folder) with the dev tag into a temporary file and returns its path; ok is false when it cannot be built here (a tool or library missing).
func Build(t *testing.T, dir string, tags ...string) (path string, ok bool) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "prog")
	args := []string{"build", "-tags", "dev"}
	for _, tag := range tags {
		args[2] += "," + tag
	}
	args = append(args, "-o", bin, ".")
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("not built here: %v\n%s", err, out)
		return "", false
	}
	return bin, true
}
