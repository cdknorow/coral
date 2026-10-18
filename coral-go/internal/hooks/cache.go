package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/secfile"
	"github.com/cdknorow/coral/internal/testguard"
)

// The hooks' cache and debug log (#3571). They live in the per-user Coral data folder, in a
// 0700 folder of their own, never in a shared temp folder. Every file is 0600 and opened
// without following a symlink. No file name is built from what an agent sent: a task's
// cache entry is named by the sha256 of its ID, so no ID can name a path.
//
// The old /tmp/coral_task_cache (or $TMPDIR/coral_task_cache) folder is no longer used;
// files left there are not touched.

// MaxTaskIDLen is the longest task ID the cache accepts.
const MaxTaskIDLen = 128

// maxCachedSubject caps what a cache entry holds and what is read back.
const maxCachedSubject = 64 << 10

const debugLogMax = 512000

var errNotCacheDir = errors.New("hooks: the hook cache folder is not a plain folder")

// CoralDir is the data folder of the Coral that launched this agent (Coral sets
// CORAL_DATA_DIR in every agent's environment), else ~/.coral.
func CoralDir() string {
	dir := ""
	for _, k := range []string{"CORAL_DATA_DIR", "CORAL_DIR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			dir = v
			break
		}
	}
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".coral")
	}
	testguard.CheckDataPath(dir)
	return dir
}

// CacheDir returns the hooks' cache folder, <data dir>/hook_cache, made 0700 if needed. It
// fails when that path is a symlink or not a folder.
func CacheDir() (string, error) {
	d := filepath.Join(CoralDir(), "hook_cache")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(d)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", errNotCacheDir
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(d, 0o700); err != nil {
			return "", err
		}
	}
	return d, nil
}

// openCacheFile opens name (a fixed name or a hash, never agent text) in the cache folder:
// 0600, and never through a symlink.
func openCacheFile(name string, flag int) (*os.File, error) {
	dir, err := CacheDir()
	if err != nil {
		return nil, err
	}
	f, err := secfile.OpenNoFollow(filepath.Join(dir, name), flag, 0o600)
	if err != nil {
		return nil, err
	}
	if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
			f.Close()
			return nil, errNotCacheDir
		}
		_ = f.Chmod(0o600) // a file made by an older hook may be 0644
	}
	return f, nil
}

// taskCacheName names a task's cache entry, or reports that the ID is not one to cache.
func taskCacheName(taskID string) (string, bool) {
	if taskID == "" || taskID == "<nil>" || len(taskID) > MaxTaskIDLen {
		return "", false
	}
	sum := sha256.Sum256([]byte(taskID))
	return "task_" + hex.EncodeToString(sum[:]), true
}

// The task cache holds at most taskCacheMax entries (#4602, M11): every task an agent creates adds a file and nothing
// removed them. A write that adds one past the limit removes the oldest entries first (by modification time) down to
// taskCacheKeep, at least taskCacheMax-taskCacheKeep of them. These are Coral's own cache files, never user data: an entry is
// only a subject remembered for a later TaskUpdate, and a missing one is read back as "".
var (
	taskCacheMax  = 5000
	taskCacheKeep = 4500
)

// taskCacheFile is exactly the name taskCacheName makes: only such files are ever counted or removed.
var taskCacheFile = regexp.MustCompile(`^task_[0-9a-f]{64}$`)

// TaskCacheWrite remembers a task's subject so a later TaskUpdate without one can find it.
func TaskCacheWrite(taskID, subject string) error {
	name, ok := taskCacheName(taskID)
	if !ok {
		return errors.New("hooks: not a task ID")
	}
	if len(subject) > maxCachedSubject {
		subject = subject[:maxCachedSubject]
	}
	isNew := false
	if dir, err := CacheDir(); err == nil {
		if _, err := os.Lstat(filepath.Join(dir, name)); errors.Is(err, os.ErrNotExist) {
			isNew = true
		}
	}
	f, err := openCacheFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	_, err = f.WriteString(subject)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if isNew {
		pruneTaskCache(name)
	}
	return err
}

// pruneTaskCache removes the oldest task entries when there are more than taskCacheMax, never keep (the one just written).
func pruneTaskCache(keep string) {
	dir, err := CacheDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type entry struct {
		name string
		mod  time.Time
	}
	var tasks []entry
	for _, e := range entries {
		if !taskCacheFile.MatchString(e.Name()) || e.Name() == keep {
			continue
		}
		info, err := e.Info() // lstat: a link is its own entry, never followed
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		tasks = append(tasks, entry{e.Name(), info.ModTime()})
	}
	if len(tasks)+1 <= taskCacheMax {
		return
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].mod.Before(tasks[j].mod) })
	for _, t := range tasks[:len(tasks)+1-taskCacheKeep] {
		_ = os.Remove(filepath.Join(dir, t.name))
	}
}

// TaskCacheRead returns the subject cached for a task, or "".
func TaskCacheRead(taskID string) string {
	name, ok := taskCacheName(taskID)
	if !ok {
		return ""
	}
	f, err := openCacheFile(name, os.O_RDONLY)
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	data, _ := io.ReadAll(io.LimitReader(f, maxCachedSubject))
	return string(data)
}

// DebugLog appends a timestamped line to <data dir>/hook_cache/debug.log (0600). A line
// names an event and its IDs: never a payload, a subject or a title. Control characters
// are dropped, so a value can't start a line of its own.
func DebugLog(msg string) {
	flag := os.O_APPEND | os.O_CREATE | os.O_WRONLY
	if dir, err := CacheDir(); err == nil {
		if fi, err := os.Lstat(filepath.Join(dir, "debug.log")); err == nil && fi.Mode().IsRegular() && fi.Size() > debugLogMax {
			flag |= os.O_TRUNC // rotate
		}
	}
	f, err := openCacheFile("debug.log", flag)
	if err != nil {
		return
	}
	defer f.Close()
	msg = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, msg)
	fmt.Fprintf(f, "[%s] %s\n", time.Now().UTC().Format("15:04:05.000"), msg)
}

// LogID shortens an agent-supplied ID for the debug log.
func LogID(id string) string {
	return Truncate(id, 64)
}
