package routes

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
)

func repoOn(t *testing.T, branch string, commit bool) (string, func(args ...string) string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	git("init", "-q", "-b", branch)
	if commit {
		git("commit", "-q", "--allow-empty", "-m", "init")
	}
	return repo, git
}

// #6701 (QA's #6692): a team launch with a worktree in a repository on master (or any branch but main) used to fail with "invalid reference: main" because the base branch defaulted to main.
func TestTeamLaunchWithAWorktreeInARepositoryNotOnMain(t *testing.T) {
	for _, base := range []string{"", "main"} { // nothing named, and the old default
		for _, branch := range []string{"master", "trunk"} {
			cfg := config.Load(t.TempDir())
			server, _, _, ss := setupSessionsTestServerWithConfig(t, cfg)
			stubCLIPaths(t, ss)
			require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", stubCLI(t)))
			repo, git := repoOn(t, branch, true)
			body := map[string]any{"board_name": "wt-" + branch, "working_dir": repo, "worktree": true, "agent_type": "claude", "agents": []map[string]any{{"name": "A", "prompt": "x"}}}
			if base != "" {
				body["base_branch"] = base
			}
			status, out := postTeam(t, server.URL, body)
			require.Equal(t, http.StatusOK, status, "base %q on %s: %v", base, branch, out)
			id := out["board_id"].(string)
			assert.Contains(t, git("worktree", "list"), filepath.Join("worktrees", id), "the worktree is made")
			assert.Contains(t, git("branch", "--list", "coral-team/"+id), "coral-team/"+id, "on the team's branch, started from "+branch)
		}
	}
}

func TestTeamLaunchWithAWorktreeInARepositoryWithNoCommitsSaysSo(t *testing.T) {
	server, _, _, ss := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	stubCLIPaths(t, ss)
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", stubCLI(t)))
	repo, _ := repoOn(t, "master", false)
	status, out := postTeam(t, server.URL, map[string]any{"board_name": "wt-empty", "working_dir": repo, "worktree": true, "agent_type": "claude", "agents": []map[string]any{{"name": "A", "prompt": "x"}}})
	assert.GreaterOrEqual(t, status, 400, "%v", out)
	assert.Contains(t, strings.ToLower(toJSON(out)), "no commits yet", "one clear reason, not git's invalid reference")
	assert.NotContains(t, toJSON(out), "invalid reference")
}

func toJSON(m map[string]any) string {
	var b strings.Builder
	for k, v := range m {
		b.WriteString(k)
		b.WriteString("=")
		if s, ok := v.(string); ok {
			b.WriteString(s)
		}
		b.WriteString(";")
	}
	return b.String()
}
