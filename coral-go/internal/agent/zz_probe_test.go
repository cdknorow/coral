package agent

import "testing"

func TestProbe5247(t *testing.T) {
	for _, p := range []LaunchParams{
		{Capabilities: &Capabilities{Allow: []string{"file_read"}}, Flags: []string{"--sandbox", "workspace-write", "-a", "never"}},
		{Capabilities: &Capabilities{Allow: []string{"file_read"}}, Flags: []string{"--sandbox", "danger-full-access"}},
		{Capabilities: &Capabilities{Allow: []string{"file_read"}}, Flags: []string{"--sandbox=danger-full-access"}},
		{Capabilities: &Capabilities{Allow: []string{"file_read"}}, Flags: []string{"-s", "danger-full-access"}},
		{PermissionMode: "plan", Flags: []string{"--sandbox", "workspace-write"}},
		{Flags: []string{"--sandbox", "read-only", "--dangerously-bypass-approvals-and-sandbox"}},
	} {
		p.SessionID, p.CLIPath = "11111111-2222-3333-4444-555555555555", "codex"
		cmd, _ := (&CodexAgent{}).LaunchCommand(p)
		t.Logf("caps=%v mode=%q flags=%v\n   => %s", p.Capabilities, p.PermissionMode, p.Flags, cmd)
	}
}
