package background

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3601 (weak site W24): a stored base branch that git would read as an option never reaches it:
// branchHasCommits answers "keep the branch" without running git.
func TestBranchHasCommitsNeverPassesAnOption(t *testing.T) {
	repo := t.TempDir()
	out := filepath.Join(t.TempDir(), "written")
	assert.True(t, branchHasCommits(context.Background(), repo, "--output="+out, "coral/job-run-1"))
	assert.True(t, branchHasCommits(context.Background(), repo, "main", "-x"))
	_, err := os.Stat(out)
	assert.True(t, os.IsNotExist(err), "git was never given --output")
}
