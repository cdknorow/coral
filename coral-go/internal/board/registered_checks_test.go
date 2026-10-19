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

// quietGitEnv turns off the background work git starts after a commit or a push: since git 2.29 every commit runs
// "git maintenance run --auto --quiet --detach" (and a push into a repository runs it there), a detached process that
// can still be touching .git when the test ends, so t.TempDir's cleanup found .git "directory not empty" (the v1.4.106 gate).
var quietGitEnv = []string{"GIT_CONFIG_COUNT=3",
	"GIT_CONFIG_KEY_0=maintenance.auto", "GIT_CONFIG_VALUE_0=false",
	"GIT_CONFIG_KEY_1=gc.auto", "GIT_CONFIG_VALUE_1=0",
	"GIT_CONFIG_KEY_2=receive.autogc", "GIT_CONFIG_VALUE_2=false"}

// quietBare makes a bare repository that, too, starts nothing after a push into it: git strips GIT_CONFIG_COUNT from the
// receive-pack it runs for a push, so the remote's own config has to say it.
func quietBare(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, quietGit("init", "-q", "--bare", dir).Run())
	for _, kv := range [][2]string{{"receive.autogc", "false"}, {"maintenance.auto", "false"}, {"gc.auto", "0"}} {
		cmd := quietGit("config", kv[0], kv[1])
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), kv[0])
	}
}

// quietGit is a git command of these fixtures: it leaves no process behind once it has returned.
func quietGit(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), quietGitEnv...)
	return cmd
}

func gitFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n\ngo 1.21\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fixture_test.go"), []byte("package fixture\nimport \"testing\"\nfunc TestFixture(t *testing.T) {}\n"), 0600))
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "coral@example.test"}, {"config", "user.name", "Coral Test"}, {"add", "."}, {"commit", "-qm", "fixture"}} {
		cmd := quietGit(args...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), strings.Join(args, " "))
	}
	revCmd := quietGit("rev-parse", "HEAD")
	revCmd.Dir = dir
	out, err := revCmd.Output()
	require.NoError(t, err)
	return dir, strings.TrimSpace(string(out))
}

func TestLocalRegisteredCheckRunnerGoTestBindsCandidateAndRuns(t *testing.T) {
	dir, revision := gitFixture(t)
	t.Logf("fixture dir=%s revision=%s", dir, revision)
	runner := NewLocalRegisteredCheckRunner(dir)
	// This test proves the check binds the candidate and runs go test to a pass, not the timeout: the
	// 30 s default is a production default, and compiling the fixture's test binary from a cold build
	// cache on a loaded gate overran it (#4909). The timeout path is covered by
	// TestLocalRegisteredCheckRunnerUnavailableAndTimeoutAreExplicit, so give this run the full
	// maxCheckTimeout (2 min) cap of headroom, which is about what binding+running needs under load.
	evidence := runner.Run(context.Background(), "go_test", revision, map[string]string{"packages": "./...", "timeout_seconds": "120"})
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
	quietBare(t, remoteDir)
	cmd := quietGit("remote", "add", "origin", remoteDir)
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = quietGit("push", "origin", "HEAD:refs/heads/release/candidate")
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
	cmd = quietGit("add", "later.txt")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = quietGit("commit", "-qm", "later")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = quietGit("rev-parse", "HEAD")
	cmd.Dir = dir // the test repo's commit, not the checkout running the tests (#3442)
	laterBytes, err := cmd.Output()
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

// #3586: a go_test package is a pattern inside the repository, never a go flag or a path
// outside it, so the evidence can only describe the candidate revision's own packages.
func TestRegisteredGoTestPackagesStayInTheRepo(t *testing.T) {
	for _, pkg := range []string{"./...", ".", "./internal/board", "./internal/...", "./a/b_c-d.e"} {
		require.True(t, safeGoPackage(pkg), pkg)
	}
	refused := []string{"-C", "-toolexec", "-exec", "-modfile", "-overlay", "-o", "/tmp", "/tmp/...",
		"../x/...", "./a/../../b", "./..", "..", "internal/board", `.\x`, `.\..\x`, "‐C", "./a–b", "./a b", "", "./a=b"}
	for _, pkg := range refused {
		require.False(t, safeGoPackage(pkg), pkg)
	}
	// Through the runner: refused before anything runs (no command, no exit code).
	dir := t.TempDir()
	runner := NewLocalRegisteredCheckRunner(dir)
	for _, packages := range []string{"-C /tmp ./...", "./... -toolexec ./x", "../x/...", "./a/../../b", `.\x`, "‐C ./..."} {
		evidence := runner.Run(context.Background(), "go_test", "abc123", map[string]string{"packages": packages})
		require.False(t, evidence.Passed, packages)
		require.Contains(t, evidence.Details, "unsupported", packages)
		require.Empty(t, evidence.Command, packages)
		require.Nil(t, evidence.ExitCode, packages)
	}
}

// Mutation: drop quietGitEnv from quietGit: a commit or push in a fixture starts git's detached background maintenance, which
// races with the TempDir cleanup of the repository.
func TestFixtureGitStartsNoBackgroundMaintenance(t *testing.T) {
	dir, _ := gitFixture(t)
	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	quietBare(t, remoteDir)
	trace := filepath.Join(t.TempDir(), "trace.txt")
	run := func(args ...string) {
		cmd := quietGit(args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Env, "GIT_TRACE="+trace)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, strings.Join(args, " ")+"\n"+string(out))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "more.txt"), []byte("more"), 0600))
	run("add", "more.txt")
	run("commit", "-qm", "more")
	run("remote", "add", "origin", remoteDir)
	run("push", "-q", "origin", "HEAD:refs/heads/main")
	log, err := os.ReadFile(trace)
	require.NoError(t, err)
	require.NotContains(t, string(log), "maintenance run", "git started its background maintenance")
	require.NotContains(t, string(log), "gc --auto", "git started its background gc")
}
