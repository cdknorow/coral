package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func taskEntries(t *testing.T, data string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(data, "hook_cache"))
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if taskCacheFile.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// #4602 M11: the task cache holds at most taskCacheMax entries; the write that goes past it removes the oldest down to
// taskCacheKeep, never the entry just written, never another file (the debug log, a stranger's name, a link, a folder).
func TestTaskCacheIsBoundedAndRemovesTheOldest(t *testing.T) {
	_, data, _ := cacheRig(t)
	defer func(m, k int) { taskCacheMax, taskCacheKeep = m, k }(taskCacheMax, taskCacheKeep)
	taskCacheMax, taskCacheKeep = 20, 15
	require.NoError(t, TaskCacheWrite("seed", "x")) // makes the folder
	cache := filepath.Join(data, "hook_cache")

	// Things that are not task entries and must survive a prune.
	require.NoError(t, os.WriteFile(filepath.Join(cache, "debug.log"), []byte("log\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cache, "task_not-a-hash"), []byte("stranger"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(cache, "task_"+fmt.Sprintf("%064x", 7)), 0o700))
	ancient := time.Now().Add(-365 * 24 * time.Hour) // the oldest things there: a prune that took them for entries would remove them first
	for _, n := range []string{"debug.log", "task_not-a-hash", "task_" + fmt.Sprintf("%064x", 7)} {
		require.NoError(t, os.Chtimes(filepath.Join(cache, n), ancient, ancient))
	}

	base := time.Now().Add(-48 * time.Hour)
	seedName, _ := taskCacheName("seed")
	require.NoError(t, os.Chtimes(filepath.Join(cache, seedName), base.Add(-time.Hour), base.Add(-time.Hour))) // older than all of them
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("task-%d", i)
		require.NoError(t, TaskCacheWrite(id, fmt.Sprintf("subject %d", i)))
		name, _ := taskCacheName(id)
		require.NoError(t, os.Chtimes(filepath.Join(cache, name), base.Add(time.Duration(i)*time.Minute), base.Add(time.Duration(i)*time.Minute)))
		n := len(taskEntries(t, data)) - 1 // the folder made to look like an entry is not a file entry
		assert.LessOrEqual(t, n, taskCacheMax, "after write %d", i)
	}
	// The newest are there, the oldest are gone, and what was read back is what was written.
	assert.Equal(t, "subject 39", TaskCacheRead("task-39"))
	assert.Equal(t, "subject 30", TaskCacheRead("task-30"))
	assert.Equal(t, "", TaskCacheRead("task-0"), "the oldest was removed (read back as nothing)")
	assert.Equal(t, "", TaskCacheRead("seed"))
	for _, keep := range []string{"debug.log", "task_not-a-hash", "task_" + fmt.Sprintf("%064x", 7)} {
		_, err := os.Lstat(filepath.Join(cache, keep))
		assert.NoError(t, err, "%s is not a cache entry and was not touched", keep)
	}
}

// Updating an entry that exists adds none, so it never prunes; a prune never removes the entry being written.
func TestTaskCacheUpdateOfAnExistingEntryDoesNotPrune(t *testing.T) {
	_, data, _ := cacheRig(t)
	defer func(m, k int) { taskCacheMax, taskCacheKeep = m, k }(taskCacheMax, taskCacheKeep)
	taskCacheMax, taskCacheKeep = 5, 3
	for i := 0; i < 5; i++ {
		require.NoError(t, TaskCacheWrite(fmt.Sprintf("t%d", i), "v1"))
	}
	require.Len(t, taskEntries(t, data), 5, "at the limit, nothing removed yet")
	for i := 0; i < 5; i++ {
		require.NoError(t, TaskCacheWrite(fmt.Sprintf("t%d", i), "v2"))
	}
	assert.Len(t, taskEntries(t, data), 5, "rewriting entries that exist removes none")
	require.NoError(t, TaskCacheWrite("t-new", "new"))
	assert.Len(t, taskEntries(t, data), 3, "a sixth entry prunes down to the keep size")
	assert.Equal(t, "new", TaskCacheRead("t-new"), "and the new entry is one of those kept")
}

// The real limits: 5,000 entries, the oldest 500 (more if more are over) removed by the write past them.
func TestTaskCacheRealLimits(t *testing.T) {
	assert.Equal(t, 5000, taskCacheMax)
	assert.Equal(t, 4500, taskCacheKeep)
	_, data, _ := cacheRig(t)
	require.NoError(t, TaskCacheWrite("first", "x"))
	cache := filepath.Join(data, "hook_cache")
	base := time.Now().Add(-100 * time.Hour)
	for i := 1; i < 5000; i++ { // 5,000 entries in all, written directly: this is fast
		name, _ := taskCacheName(fmt.Sprintf("bulk-%d", i))
		require.NoError(t, os.WriteFile(filepath.Join(cache, name), []byte("s"), 0o600))
		require.NoError(t, os.Chtimes(filepath.Join(cache, name), base.Add(time.Duration(i)*time.Second), base.Add(time.Duration(i)*time.Second)))
	}
	require.Len(t, taskEntries(t, data), 5000)
	require.NoError(t, TaskCacheWrite("the-5001st", "fresh"))
	assert.Len(t, taskEntries(t, data), 4500, "5,001 entries: the oldest 501 are removed, leaving 4,500")
	assert.Equal(t, "fresh", TaskCacheRead("the-5001st"))
	assert.Equal(t, "", TaskCacheRead("bulk-1"), "an old one is gone")
	assert.Equal(t, "s", TaskCacheRead("bulk-4999"), "a newer one is kept")
}
