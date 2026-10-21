package routes

import (
	"context"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/gitutil"
)

// fileListTTL bounds how stale a cached file list can be when the git poller hasn't seen the
// change yet (it polls less often than this).
const fileListTTL = 15 * time.Second

// The cache is bounded (#4620, Security's M13): at most fileListMaxEntries repositories (the oldest
// out), and a list longer than fileListMaxFiles is answered but not kept: a repository that big is
// listed again, which costs a git call and not hundreds of megabytes of paths held for 15 s each.
const (
	fileListMaxEntries = 32
	fileListMaxFiles   = 200_000
)

// fileLists caches each repository's file list for the file search (#3721): listing it (git
// ls-files with the untracked files) was the slowest request of every agent switch, 124 to 302
// ms. One listing per repository at a time (concurrent callers share it); a list is dropped
// when the git poller sees the folder's changed files change, and after fileListTTL.
var fileLists = newFileListCache(fileListTTL)

func init() { background.OnFilesChanged(fileLists.invalidate) }

type fileListEntry struct {
	files []string
	at    time.Time
}

type fileListCache struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]fileListEntry
	flights map[string]chan struct{}
	now     func() time.Time
	list    func(ctx context.Context, workdir string) ([]string, error)
}

func newFileListCache(ttl time.Duration) *fileListCache {
	return &fileListCache{ttl: ttl, entries: map[string]fileListEntry{}, flights: map[string]chan struct{}{}, now: time.Now, list: gitListFiles}
}

// gitListFiles lists a repository's tracked and untracked (not ignored) files.
func gitListFiles(ctx context.Context, workdir string) ([]string, error) {
	out, err := gitutil.Command(ctx, workdir, "ls-files", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, err
	}
	files := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(files) == 1 && files[0] == "" {
		files = nil
	}
	return files, nil
}

// get returns workdir's file list, listing it at most once at a time; the slice is shared, never
// modify it. A failed listing isn't kept.
func (c *fileListCache) get(ctx context.Context, workdir string) ([]string, error) {
	for {
		c.mu.Lock()
		if e, ok := c.entries[workdir]; ok && c.now().Sub(e.at) < c.ttl {
			c.mu.Unlock()
			return e.files, nil
		}
		if wait, ok := c.flights[workdir]; ok {
			c.mu.Unlock()
			select {
			case <-wait:
				continue // the listing finished (or failed): look again
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		done := make(chan struct{})
		c.flights[workdir] = done
		c.mu.Unlock()

		files, err := c.list(ctx, workdir)
		c.mu.Lock()
		delete(c.flights, workdir)
		if err == nil && len(files) <= fileListMaxFiles {
			c.keep(workdir, files)
		}
		c.mu.Unlock()
		close(done)
		return files, err
	}
}

// keep stores a list, dropping the oldest entries when there are fileListMaxEntries already.
// c.mu is held.
func (c *fileListCache) keep(workdir string, files []string) {
	if _, ok := c.entries[workdir]; !ok {
		for len(c.entries) >= fileListMaxEntries {
			oldest, first := "", true
			var oldestAt time.Time
			for k, e := range c.entries {
				if first || e.at.Before(oldestAt) {
					oldest, oldestAt, first = k, e.at, false
				}
			}
			delete(c.entries, oldest)
		}
	}
	c.entries[workdir] = fileListEntry{files: files, at: c.now()}
}

// invalidate drops the lists of workdir and of any repository containing it or inside it (the
// poller's folder can be an agent's subfolder of the repository the search lists).
func (c *fileListCache) invalidate(workdir string) {
	w := folderKey(workdir, runtime.GOOS == "windows")
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		key := folderKey(k, runtime.GOOS == "windows")
		if strings.HasPrefix(w, key) || strings.HasPrefix(key, w) {
			delete(c.entries, k)
		}
	}
}

// folderKey is a folder as invalidate compares it: cleaned, with a trailing separator so that
// /repo never matches /repo2. On Windows either slash separates and case doesn't count, so
// C:\Repo\pkg is inside C:/repo (#59, QA #28 C4: a subfolder's change never cleared its
// repository's list there).
func folderKey(dir string, windows bool) string {
	if windows {
		dir = strings.ToLower(strings.ReplaceAll(dir, "/", `\`))
		return strings.TrimRight(filepath.Clean(dir), `\`) + `\`
	}
	return strings.TrimRight(path.Clean(dir), "/") + "/"
}
