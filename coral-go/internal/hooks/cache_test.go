package hooks

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/linktest"
)

// Path-shaped task IDs an agent could send (#3571).
var hostileTaskIDs = []string{"../x", "/abs", "a/../../b", "x/../../..", "../../secret", "..", ".", `..\..\secret`, "task_../../x", "a\x00b"}

// cacheRig points the hooks at a fresh data folder inside root, with a file outside it that
// no cache operation may read or change.
func cacheRig(t *testing.T) (root, data, secret string) {
	t.Helper()
	root = t.TempDir()
	data = filepath.Join(root, "data")
	t.Setenv("CORAL_DATA_DIR", data)
	t.Setenv("CORAL_DIR", "")
	secret = filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, data, secret
}

var cacheEntry = regexp.MustCompile(`^(task_[0-9a-f]{64}|debug\.log)$`)

// onlyCacheFiles fails unless every file under root, other than the secret, is a cache
// entry or the debug log directly inside <data>/hook_cache, and the secret is unchanged.
func onlyCacheFiles(t *testing.T, root, data, secret string) {
	t.Helper()
	cache := filepath.Join(data, "hook_cache")
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == secret {
			return err
		}
		if filepath.Dir(p) != cache || !cacheEntry.MatchString(d.Name()) {
			t.Errorf("a file outside the cache, or not named by a hash: %s", p)
		}
		return nil
	})
	if b, _ := os.ReadFile(secret); string(b) != "SECRET" {
		t.Errorf("the file outside the cache changed: %q", b)
	}
}

func TestTaskCacheIDsNeverFormAPath(t *testing.T) {
	root, data, secret := cacheRig(t)
	for _, id := range hostileTaskIDs {
		if got := TaskCacheRead(id); got != "" {
			t.Errorf("TaskCacheRead(%q) before any write = %q, want nothing", id, got)
		}
		if err := TaskCacheWrite(id, "pwned "+id); err != nil {
			t.Fatalf("TaskCacheWrite(%q): %v", id, err)
		}
		if got := TaskCacheRead(id); got != "pwned "+id {
			t.Errorf("TaskCacheRead(%q) = %q", id, got)
		}
	}
	onlyCacheFiles(t, root, data, secret)
	if _, err := os.Stat("/abs"); err == nil && runtime.GOOS != "windows" {
		t.Error("/abs exists")
	}
	// Each ID has its own entry.
	entries, _ := os.ReadDir(filepath.Join(data, "hook_cache"))
	if len(entries) != len(hostileTaskIDs) {
		t.Errorf("%d cache entries for %d IDs", len(entries), len(hostileTaskIDs))
	}
}

func TestTaskCacheRejectsEmptyAndLongIDs(t *testing.T) {
	root, data, secret := cacheRig(t)
	for _, id := range []string{"", "<nil>", strings.Repeat("a", MaxTaskIDLen+1)} {
		if err := TaskCacheWrite(id, "x"); err == nil {
			t.Errorf("TaskCacheWrite(%.20q...) cached it", id)
		}
		if TaskCacheRead(id) != "" {
			t.Errorf("TaskCacheRead(%.20q...) read something", id)
		}
	}
	if err := TaskCacheWrite(strings.Repeat("a", MaxTaskIDLen), "x"); err != nil {
		t.Errorf("an ID of the longest length: %v", err)
	}
	onlyCacheFiles(t, root, data, secret)
}

func TestCacheModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	_, data, _ := cacheRig(t)
	cache := filepath.Join(data, "hook_cache")
	// A folder an older build made too open is tightened.
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := TaskCacheWrite("7", "subject"); err != nil {
		t.Fatal(err)
	}
	DebugLog("TaskCreate: cache_id=7")
	mode := func(p string) os.FileMode {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}
	name, _ := taskCacheName("7")
	if m := mode(cache); m != 0o700 {
		t.Errorf("cache folder %o, want 700", m)
	}
	for _, f := range []string{name, "debug.log"} {
		if m := mode(filepath.Join(cache, f)); m != 0o600 {
			t.Errorf("%s %o, want 600", f, m)
		}
	}
}

// A link planted at a cache file's name or at debug.log (a symbolic link; on Windows also a
// junction) is never written, read or followed (#3920: on Windows it was).
func TestCacheNeverFollowsAPlantedLink(t *testing.T) {
	_, _, _ = cacheRig(t)
	cache, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	name, _ := taskCacheName("7")
	linktest.Each(t, filepath.Join(cache, name), func(t *testing.T, p linktest.Planted) {
		if err := TaskCacheWrite("7", "pwned"); err == nil {
			t.Error("wrote through a link at the cache entry")
		}
		if got := TaskCacheRead("7"); got != "" {
			t.Errorf("read %q through a link at the cache entry", got)
		}
	})
	linktest.Each(t, filepath.Join(cache, "debug.log"), func(t *testing.T, p linktest.Planted) {
		DebugLog("through a link")
	})
}

func TestCacheNeverFollowsASymlinkedFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a symlinked cache folder is Lstat-checked; on Windows a folder symlink needs privileges this test doesn't assume")
	}
	root, data, _ := cacheRig(t)
	cache, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}

	// The cache folder itself a symlink: refused.
	os.RemoveAll(cache)
	elsewhere := filepath.Join(root, "elsewhere")
	os.Mkdir(elsewhere, 0o700)
	if err := os.Symlink(elsewhere, cache); err != nil {
		t.Fatal(err)
	}
	if _, err := CacheDir(); err == nil {
		t.Error("CacheDir accepted a symlink")
	}
	if err := TaskCacheWrite("8", "pwned"); err == nil {
		t.Error("wrote into a symlinked cache folder")
	}
	DebugLog("into a symlinked folder")
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("files written through the symlinked folder: %v", entries)
	}
	_ = data
}

func TestDebugLogKeepsOneLinePerEntry(t *testing.T) {
	_, data, _ := cacheRig(t)
	DebugLog("TaskCreate: cache_id=a\n[00:00:00.000] forged\r")
	b, err := os.ReadFile(filepath.Join(data, "hook_cache", "debug.log"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\n"); n != 1 {
		t.Errorf("%d lines: %q", n, b)
	}
}

func TestResolveAgentNameAndLivePath(t *testing.T) {
	for cwd, want := range map[string]string{
		"/w/agent-x": "agent-x", "/w/agent-x/": "agent-x", "/w/..": "", "/w/.": "", "/": "", "..": "",
	} {
		if got := ResolveAgentName(map[string]any{"cwd": cwd}); got != want {
			t.Errorf("ResolveAgentName(cwd %q) = %q, want %q", cwd, got, want)
		}
	}
	if got := LivePath("a?b#c%d e", "/tasks"); got != "/api/sessions/live/a%3Fb%23c%25d%20e/tasks" {
		t.Errorf("LivePath = %q", got)
	}
}
