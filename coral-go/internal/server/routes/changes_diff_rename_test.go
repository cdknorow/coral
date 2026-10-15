package routes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// QA, #3555 review: a tracked secret file renamed to an ordinary name, with an edit,
// must not leave its old lines in a stored or tunneled diff.
func TestQARenameFromHiddenPath(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=qa@example.test", "-c", "user.name=qa"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q")
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "API_TOKEN_LINE_"+string(rune('A'+i))+"=qa-secret-value")
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x\n"), 0o644))
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	run("mv", ".env", "config.txt")
	lines[10] = "API_TOKEN_LINE_K=changed"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	out, err := exec.Command("git", "-C", dir, "diff", "--binary", "HEAD", "--").Output()
	require.NoError(t, err)
	t.Logf("raw diff header: %q", strings.SplitN(string(out), "\n", 5)[:4])
	got := string(filterPatch(out))
	t.Logf("filtered: %q", got[:min(len(got), 300)])
	require.NotContains(t, got, "qa-secret-value", "lines of the renamed .env leak through the filter")
}
