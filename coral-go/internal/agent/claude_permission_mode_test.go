package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #5894: an interactive Claude launch always carries a permission mode. With none, Claude Code 2.1.283 and later starts in Auto, so where Coral means "ask" it passes
// --permission-mode default; a mode or a bypass in the person's own flags wins; a print-mode run starts in Manual without it and is left as it was.
func TestClaudeInteractiveLaunchAlwaysCarriesAPermissionMode(t *testing.T) {
	build := func(p LaunchParams) string {
		p.SessionID, p.CLIPath = "11111111-2222-3333-4444-555555555555", "claude"
		cmd, err := (&ClaudeAgent{}).LaunchCommand(p)
		assert.NoError(t, err)
		return cmd
	}
	count := func(cmd string) int { return strings.Count(cmd, "--permission-mode") }
	for _, mode := range []string{"", "default"} {
		cmd := build(LaunchParams{PermissionMode: mode})
		assert.Contains(t, cmd, "--permission-mode default", "mode %q", mode)
		assert.Equal(t, 1, count(cmd), "mode %q", mode)
	}
	// a resumed or woken agent restoring a stored default
	cmd := build(LaunchParams{ResumeSessionID: "99999999-2222-3333-4444-555555555555", PermissionMode: "default"})
	assert.Contains(t, cmd, "--resume 99999999-2222-3333-4444-555555555555")
	assert.Contains(t, cmd, "--permission-mode default")
	// Auto and the others as chosen
	assert.Contains(t, build(LaunchParams{PermissionMode: "auto"}), "--permission-mode auto")
	assert.Contains(t, build(LaunchParams{PermissionMode: "acceptEdits"}), "--permission-mode acceptEdits")
	// the person's own flags win, and nothing is said twice
	for _, flags := range [][]string{{"--permission-mode", "plan"}, {"--permission-mode=acceptEdits"}, {"--dangerously-skip-permissions"}} {
		cmd := build(LaunchParams{Flags: flags})
		assert.NotContains(t, cmd, "--permission-mode default", strings.Join(flags, " "))
		assert.LessOrEqual(t, count(cmd), 1, strings.Join(flags, " "))
	}
	// a print-mode run: as it was
	args, _, err := (&ClaudeAgent{}).PrintArgs(LaunchParams{SessionID: "11111111-2222-3333-4444-555555555555", PermissionMode: ""})
	assert.NoError(t, err)
	assert.NotContains(t, args, "--permission-mode")
	args, _, err = (&ClaudeAgent{}).PrintArgs(LaunchParams{SessionID: "11111111-2222-3333-4444-555555555555", PermissionMode: "auto"})
	assert.NoError(t, err)
	assert.Contains(t, args, "auto")
}
