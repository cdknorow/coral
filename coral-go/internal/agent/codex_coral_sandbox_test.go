package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexCoralSandboxDefault(t *testing.T) {
	p := LaunchParams{SessionID: "11111111-2222-4333-8444-555555555555", CLIPath: "codex", PermissionMode: DefaultLaunchPermissionMode("codex")}
	cmd, err := (&CodexAgent{}).LaunchCommand(p)
	require.NoError(t, err)
	assert.Contains(t, cmd, "--sandbox workspace-write -a never")
	assert.Contains(t, cmd, "sandbox_workspace_write.network_access=true")
	assert.NotContains(t, cmd, "dangerously-bypass")
	assert.NotContains(t, cmd, "writable_roots", "Coral's token and board-state files only need read access")
	assert.True(t, SkipsPermissionPrompts("codex", PermissionFlagsForAgent("codex", p.PermissionMode)), "Hub classification remains skipping")
	for _, mode := range []string{"default", "plan", "acceptEdits", "bypassPermissions"} {
		p.PermissionMode = mode
		cmd, err = (&CodexAgent{}).LaunchCommand(p)
		require.NoError(t, err)
		assert.NotContains(t, cmd, "sandbox_workspace_write.network_access=true", mode)
		if mode == "bypassPermissions" {
			assert.Contains(t, cmd, "--dangerously-bypass-approvals-and-sandbox")
		}
	}
}

func TestCodexCoralSandboxExplicitPolicyWins(t *testing.T) {
	for _, flags := range [][]string{
		{"-c", "sandbox_workspace_write.network_access=false"},
		{"--config=sandbox_workspace_write.network_access=false"},
		{"-c=sandbox_workspace_write.network_access=false"},
	} {
		p := LaunchParams{PermissionMode: "auto", Flags: flags, CLIPath: "codex"}
		cmd, err := (&CodexAgent{}).LaunchCommand(p)
		require.NoError(t, err)
		assert.Less(t, strings.Index(cmd, "network_access=true"), strings.Index(cmd, "network_access=false"), cmd)
	}
	for _, p := range []LaunchParams{
		{PermissionMode: "auto", Flags: []string{"--sandbox", "read-only", "-a", "never"}},
		{PermissionMode: "auto", Flags: []string{"--sandbox", "workspace-write", "-a", "on-request"}},
		{PermissionMode: "auto", Capabilities: &Capabilities{Allow: []string{CapFileRead}}},
	} {
		p.CLIPath = "codex"
		cmd, err := (&CodexAgent{}).LaunchCommand(p)
		require.NoError(t, err)
		assert.NotContains(t, cmd, "sandbox_workspace_write.network_access=true")
	}
}

// The shell-only capability preset also yields workspace-write/never. It must
// not inherit the default's networking even though that final pair matches.
func TestCodexCoralSandboxCapabilityPresetKeepsItsNetworkPolicy(t *testing.T) {
	p := LaunchParams{CLIPath: "codex", PermissionMode: "auto", Capabilities: &Capabilities{Allow: []string{CapShell}}}
	cmd, err := (&CodexAgent{}).LaunchCommand(p)
	require.NoError(t, err)
	assert.Contains(t, cmd, "--sandbox workspace-write -a never")
	assert.NotContains(t, cmd, "sandbox_workspace_write.network_access", "a non-empty preset keeps its own network policy")
}
