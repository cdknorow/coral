package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #5222: a Codex agent woken after an earlier restart typed --sandbox twice (the restart stored the expanded flags, the wake added the mode's again) and Codex refused to start. The builder
// never gives Codex either flag twice. (PermissionModeFromFlags deliberately does not read these flags as a mode: TestPermissionModeFromFlags_TableTest_NeverWidens.)
func TestCodexLaunchNeverRepeatsSandboxOrApproval(t *testing.T) {
	count := func(cmd, flag string) int { return strings.Count(cmd, " "+flag+" ") }
	build := func(p LaunchParams) string {
		p.SessionID, p.CLIPath = "11111111-2222-3333-4444-555555555555", "codex"
		cmd, err := (&CodexAgent{}).LaunchCommand(p)
		assert.NoError(t, err)
		return cmd
	}
	// The same pair twice (stored by a restart, added again by a wake): once.
	cmd := build(LaunchParams{Flags: []string{"--sandbox", "workspace-write", "-a", "never", "--sandbox", "workspace-write", "-a", "never"}})
	assert.Equal(t, 1, count(cmd, "--sandbox"), cmd)
	assert.Equal(t, 1, count(cmd, "-a"), cmd)
	// The newer wins: a later value replaces an earlier one.
	cmd = build(LaunchParams{Flags: []string{"--sandbox", "read-only", "-a", "on-request", "--sandbox", "workspace-write", "-a", "never"}})
	assert.Contains(t, cmd, "--sandbox workspace-write -a never")
	assert.NotContains(t, cmd, "read-only")
	// Capabilities and a flag for the same thing: one.
	cmd = build(LaunchParams{Capabilities: &Capabilities{Allow: []string{"file_read"}}, Flags: []string{"--sandbox", "workspace-write"}})
	assert.Equal(t, 1, count(cmd, "--sandbox"), cmd)
	// Nothing to collapse: unchanged, and the prompt is never touched.
	cmd = build(LaunchParams{Flags: []string{"--sandbox", "read-only", "-a", "on-request"}, Prompt: "-a never --sandbox x"})
	assert.Equal(t, 1, count(cmd, "--sandbox"), cmd)
	assert.Contains(t, cmd, "read-only")
}
