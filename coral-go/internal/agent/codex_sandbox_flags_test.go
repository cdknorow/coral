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

// #5247: a capabilities preset is the authority: a flag stored from an earlier launch (or typed by hand) never widens it. Without a preset the later flag still wins.
func TestCodexLaunchPresetWinsOverStoredSandboxFlags(t *testing.T) {
	build := func(p LaunchParams) string {
		p.SessionID, p.CLIPath = "11111111-2222-3333-4444-555555555555", "codex"
		cmd, err := (&CodexAgent{}).LaunchCommand(p)
		assert.NoError(t, err)
		return cmd
	}
	readOnly := &Capabilities{Allow: []string{"file_read"}}
	cmd := build(LaunchParams{Capabilities: readOnly, Flags: []string{"--sandbox", "danger-full-access", "-a", "never"}})
	assert.Contains(t, cmd, "--sandbox read-only")
	assert.NotContains(t, cmd, "danger-full-access")
	assert.NotContains(t, cmd, " -a never")
	assert.Equal(t, 1, strings.Count(cmd, " --sandbox "), cmd)
	cmd = build(LaunchParams{Flags: []string{"--sandbox", "read-only", "--sandbox", "workspace-write"}})
	assert.Contains(t, cmd, "--sandbox workspace-write")
	assert.NotContains(t, cmd, "read-only")
}

// #5251: whatever the spelling, Codex gets one sandbox setting and one approval setting (it refuses the second: "cannot be used multiple times"), the preset's when there is a
// preset, the last given otherwise.
func TestCodexLaunchCollapsesEverySpellingOfTheSandboxAndApproval(t *testing.T) {
	build := func(p LaunchParams) string {
		p.SessionID, p.CLIPath = "11111111-2222-3333-4444-555555555555", "codex"
		cmd, err := (&CodexAgent{}).LaunchCommand(p)
		assert.NoError(t, err)
		return cmd
	}
	sandboxes := func(cmd string) int {
		n := 0
		for _, f := range []string{" --sandbox ", " --sandbox=", " -s ", " -s="} {
			n += strings.Count(cmd, f)
		}
		return n
	}
	approvals := func(cmd string) int {
		n := 0
		for _, f := range []string{" -a ", " -a=", " --ask-for-approval ", " --ask-for-approval=", " --approval-mode ", " --approval-mode="} {
			n += strings.Count(cmd, f)
		}
		return n
	}
	preset := &Capabilities{Allow: []string{"file_read"}} // read-only, on-request
	for name, flags := range map[string][]string{
		"-s X":                 {"-s", "danger-full-access"},
		"--sandbox=X":          {"--sandbox=danger-full-access"},
		"-s=X":                 {"-s=danger-full-access"},
		"--ask-for-approval X": {"--ask-for-approval", "never"},
		"-a=X":                 {"-a=never"},
		"--ask-for-approval=X": {"--ask-for-approval=never"},
		"all of them, mixed":   {"--sandbox=danger-full-access", "-s", "workspace-write", "-a=never", "--ask-for-approval", "never"},
	} {
		cmd := build(LaunchParams{Capabilities: preset, Flags: flags})
		assert.Equal(t, 1, sandboxes(cmd), "%s: one sandbox setting", name)
		assert.Equal(t, 1, approvals(cmd), "%s: one approval setting", name)
		assert.Contains(t, cmd, "--sandbox read-only -a on-request", "%s: the preset's pair wins", name)
		assert.NotContains(t, cmd, "danger-full-access", name)
	}
	// With no preset the later one wins, in whatever spelling.
	cmd := build(LaunchParams{Flags: []string{"--sandbox", "read-only", "-s", "workspace-write", "-a", "on-request", "--ask-for-approval=never"}})
	assert.Equal(t, 1, sandboxes(cmd), cmd)
	assert.Equal(t, 1, approvals(cmd), cmd)
	assert.Contains(t, cmd, "-s workspace-write")
	assert.Contains(t, cmd, "--ask-for-approval=never")
	assert.NotContains(t, cmd, "read-only")
	// A flag with no value is passed as it is, and the prompt is never read as flags.
	cmd = build(LaunchParams{Flags: []string{"-s", "-a=never"}, Prompt: "-s danger-full-access"})
	assert.Contains(t, cmd, "-a=never")
}
