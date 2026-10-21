package routes

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptymanager"
)

// The pieces of a launch (#4599 item 3: split out of LaunchTeam and launchSession, behaviour unchanged).

func TestTeamMemberTypes(t *testing.T) {
	types, skipped := teamMemberTypes(launchTeamRequest{AgentType: "claude", Agents: []launchTeamAgent{
		{Name: "A"}, {Name: "B", AgentType: "codex"}, {Name: ""}, {Name: "C", AgentType: ""},
	}})
	assert.Equal(t, []string{"claude", "codex", "claude"}, types)
	assert.Equal(t, 1, skipped)
}

func TestCheckTeamRequest(t *testing.T) {
	ok := launchTeamRequest{BoardName: "b", WorkingDir: "/tmp", Agents: []launchTeamAgent{{Name: "A"}}}
	assert.Empty(t, checkTeamRequest(ok))
	for name, mod := range map[string]func(r *launchTeamRequest){
		"no board":           func(r *launchTeamRequest) { r.BoardName = "" },
		"no folder":          func(r *launchTeamRequest) { r.WorkingDir = "" },
		"no members":         func(r *launchTeamRequest) { r.Agents = nil },
		"a bad team type":    func(r *launchTeamRequest) { r.AgentType = "nope" },
		"a bad member type":  func(r *launchTeamRequest) { r.Agents = []launchTeamAgent{{Name: "A", AgentType: "nope"}} },
		"a bad member model": func(r *launchTeamRequest) { r.Agents = []launchTeamAgent{{Name: "A", Model: "-rf"}} },
	} {
		r := ok
		mod(&r)
		assert.NotEmpty(t, checkTeamRequest(r), name)
	}
	r := ok
	r.Agents = []launchTeamAgent{{Name: "A", Model: "-rf"}}
	assert.Equal(t, `agent "A": `+errInvalidModel, checkTeamRequest(r))
}

func TestLaunchPath(t *testing.T) {
	h := &SessionsHandler{}
	assert.Equal(t, "tmux", h.launchPath(""), "no backend: tmux")
	assert.Equal(t, "pty", h.launchPath("pty"), "as asked")
	h.backend = &ptymanager.PTYBackend{}
	assert.Equal(t, "pty", h.launchPath("tmux"), "a pty Coral always launches the pty way")
}

func TestResolveLaunchCLI(t *testing.T) {
	got, err := resolveLaunchCLI("terminal", "")
	require.NoError(t, err)
	assert.Empty(t, got, "a plain terminal has no CLI")

	dir := t.TempDir()
	bin := stubScript(t, filepath.Join(dir, "my-claude"), "#!/bin/sh\n")
	got, err = resolveLaunchCLI("claude", bin)
	require.NoError(t, err)
	assert.Equal(t, bin, got, "a configured path that exists is kept")

	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir()) // no common install places
	_, err = resolveLaunchCLI("claude", filepath.Join(dir, "missing-claude"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CLI not found. Install it:")
}
