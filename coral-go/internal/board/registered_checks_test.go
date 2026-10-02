package board

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func gitFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n\ngo 1.21\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fixture_test.go"), []byte("package fixture\nimport \"testing\"\nfunc TestFixture(t *testing.T) {}\n"), 0600))
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "coral@example.test"}, {"config", "user.name", "Coral Test"}, {"add", "."}, {"commit", "-qm", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), strings.Join(args, " "))
	}
	revCmd := exec.Command("git", "rev-parse", "HEAD")
	revCmd.Dir = dir
	out, err := revCmd.Output()
	require.NoError(t, err)
	return dir, strings.TrimSpace(string(out))
}

func TestLocalRegisteredCheckRunnerGoTestBindsCandidateAndRuns(t *testing.T) {
	dir, revision := gitFixture(t)
	t.Logf("fixture dir=%s revision=%s", dir, revision)
	runner := NewLocalRegisteredCheckRunner(dir)
	evidence := runner.Run(context.Background(), "go_test", revision, map[string]string{"packages": "./..."})
	require.True(t, evidence.Passed, evidence.Details+"\n"+evidence.Log)
	require.Equal(t, revision, evidence.ObservedRevision)
	require.Equal(t, 0, *evidence.ExitCode)
	require.NotEmpty(t, evidence.OutputDigest)
	wrong := runner.Run(context.Background(), "go_test", strings.Repeat("0", 40), nil)
	require.False(t, wrong.Passed, wrong.Details)
	require.Contains(t, wrong.Details, "candidate")
}

func TestLocalRegisteredCheckRunnerGitAncestryRejectsUnmergedAndAcceptsRemoteBranch(t *testing.T) {
	dir, revision := gitFixture(t)
	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	cmd := exec.Command("git", "init", "--bare", remoteDir)
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "remote", "add", "origin", remoteDir)
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "push", "origin", "HEAD:refs/heads/release/candidate")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	runner := NewLocalRegisteredCheckRunner(dir)
	pass := runner.Run(context.Background(), "git_ancestry", revision, map[string]string{"remote": "", "branch": "release/candidate"})
	// A remote name is required; a local branch is intentionally not enough.
	require.False(t, pass.Passed)
	remote := runner.Run(context.Background(), "git_ancestry", revision, map[string]string{"remote": "origin", "branch": "release/candidate"})
	require.True(t, remote.Passed, remote.Details)
	require.Equal(t, revision, remote.ObservedRevision)
	// A different valid commit is not an ancestor of the designated branch.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "later.txt"), []byte("later"), 0600))
	cmd = exec.Command("git", "add", "later.txt")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-qm", "later")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	laterBytes, err := exec.Command("git", "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	later := runner.Run(context.Background(), "git_ancestry", strings.TrimSpace(string(laterBytes)), map[string]string{"remote": "origin", "branch": "release/candidate"})
	require.False(t, later.Passed)
	require.Contains(t, later.Details, "failed")
}

func TestLocalRegisteredCheckRunnerUnavailableAndTimeoutAreExplicit(t *testing.T) {
	unavailable := NewLocalRegisteredCheckRunner("").Run(context.Background(), "go_test", "abc", nil)
	require.False(t, unavailable.Passed)
	require.Contains(t, unavailable.Details, "unavailable")
	dir, revision := gitFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	timeout := NewLocalRegisteredCheckRunner(dir).Run(ctx, "go_test", revision, map[string]string{"timeout_seconds": "1", "packages": "./..."})
	require.False(t, timeout.Passed)
	require.NotNil(t, timeout.ExitCode)
}
