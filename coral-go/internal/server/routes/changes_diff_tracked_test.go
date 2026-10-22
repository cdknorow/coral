package routes

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// QA's #245 review: the tracked changes come first and are never cut for new files. A modified tracked file that sorts after
// 12 MiB of new files is in the diff whole; the note counts exactly the new files that are not.
func TestPersistedDiffKeepsTrackedChangesBeforeNewFiles(t *testing.T) {
	_, h, _, _ := setupSessionsTestServer(t)
	repo := diffRepo(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	require.NoError(t, os.WriteFile(filepath.Join(repo, "zz.txt"), []byte("before\n"), 0o644))
	run("add", "zz.txt")
	run("commit", "-q", "-m", "zz")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "zz.txt"), []byte("before\nthe agent's tracked edit\n"), 0o644))
	body := strings.Repeat("a line of a new file\n", 200) // 4,200 bytes: 3,000 of them are 12 MiB
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "u"), 0o755))
	const n = 3000
	for i := 0; i < n; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(repo, "u", fmt.Sprintf("f%04d.txt", i)), []byte(body), 0o644))
	}

	diff, err := h.generatePersistedDiff(context.Background(), repo)
	require.NoError(t, err)
	out := string(diff)
	assert.Contains(t, out, "+the agent's tracked edit", "the tracked change is never cut for new files")
	assert.Less(t, strings.Index(out, "+++ b/zz.txt"), strings.Index(out, "+++ b/u/f0000.txt"), "tracked changes first")
	assert.LessOrEqual(t, len(diff), diffPersistMax)
	assert.NotContains(t, out, "diff cut at", "whole new files only: nothing is cut mid-file")
	in := strings.Count(out, "+++ b/u/")
	m := regexp.MustCompile(`# Coral: (\d+) new files are left out of this diff`).FindStringSubmatch(out)
	require.NotNil(t, m)
	more, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	assert.Equal(t, n, in+more, "every new file is either in the diff or counted in the note")
}
