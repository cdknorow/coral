package routes

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/internal/tmux"
)

// #6146: a launch that names no backend gets the launch path of the terminal this Coral runs, and the answer says plainly which is which: launch_path is the path (the stored label; "pty" means through the
// TerminalBackend, a tmux Coral's included) and terminal is what the agent really runs on. It was "backend", which read as the terminal.

func TestLaunchPathByServerBackend(t *testing.T) {
	for _, c := range []struct {
		name      string
		backend   ptymanager.TerminalBackend
		requested string
		path      string
		terminal  string
	}{
		{"a Coral with no backend, none asked", nil, "", "tmux", "tmux"},
		{"a pty Coral, none asked", &ptymanager.PTYBackend{}, "", "pty", "pty"},
		{"a pty Coral, tmux asked: a pty Coral always launches the pty way", &ptymanager.PTYBackend{}, "tmux", "pty", "pty"},
		{"a tmux Coral, none asked (a dashboard launch): through the terminal backend, on tmux", &ptymanager.TmuxBackend{}, "", "pty", "tmux"},
		{"a tmux Coral, pty asked", &ptymanager.TmuxBackend{}, "pty", "pty", "tmux"},
		{"a tmux Coral, tmux asked: the legacy direct tmux path", &ptymanager.TmuxBackend{}, "tmux", "tmux", "tmux"},
	} {
		h := &SessionsHandler{backend: c.backend}
		assert.Equal(t, c.path, h.launchPath(c.requested), c.name)
		assert.Equal(t, c.terminal, terminalKind(c.backend), c.name)
	}
}

// The agents a relaunch restarts are decided by the terminal this Coral runs: a tmux Coral's agents all carry the launch path "pty" and survive a relaunch.
func TestRelaunchRestartsOnlyPtyCoralsAgents(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	ctx := context.Background()
	pty, direct, none := "pty", "tmux", ""
	for id, label := range map[string]*string{"aaaaaaaa-6146-4000-8000-000000000001": &pty, "aaaaaaaa-6146-4000-8000-000000000002": &direct, "aaaaaaaa-6146-4000-8000-000000000003": &none} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentName: "a", AgentType: "claude", WorkingDir: "/tmp", Backend: label}))
	}
	ids := func(backend string) []string {
		h := NewUpdateRelaunchHandler(ss, t.TempDir(), func() string { return backend }, nil)
		agents, err := h.Agents(ctx)
		require.NoError(t, err)
		var out []string
		for _, a := range agents {
			out = append(out, a.SessionID[len(a.SessionID)-1:])
		}
		return out
	}
	assert.Empty(t, ids("tmux"), "a tmux Coral: every agent survives, whatever launch path it carries")
	assert.ElementsMatch(t, []string{"1", "3"}, ids("pty"), "a pty Coral: its agents restart, except one started the legacy tmux way")
}

// A real tmux Coral, a launch that names no backend: the agent is on tmux (it survives a quit), and the answer, the stored label and the live session say the same thing.
func TestALaunchOnATmuxCoralIsOnTmuxAndSaysSo(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	dataDir, err := os.MkdirTemp(shortTempBase(), "clp")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	sock := filepath.Join(dataDir, "t.sock")
	smoke := sock + ".smoke"
	if out, err := exec.Command("tmux", "-S", smoke, "new-session", "-d", "-s", "coral-smoke", "sleep 1").CombinedOutput(); err != nil {
		exec.Command("tmux", "-S", smoke, "kill-server").Run()
		t.Skipf("tmux cannot create sessions here: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	exec.Command("tmux", "-S", smoke, "kill-server").Run()
	client := tmux.NewClient(dataDir)
	client.SocketPath = sock
	client.FallbackToDefault = false
	t.Cleanup(func() { exec.Command("tmux", "-S", sock, "kill-server").Run() })

	cfg := config.Load(dataDir)
	cfg.LogDir = filepath.Join(dataDir, "logs")
	require.NoError(t, os.MkdirAll(cfg.LogDir, 0o700))
	db, err := storetest.Open(t, filepath.Join(dataDir, "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	ctx := context.Background()
	require.NoError(t, ss.SetSetting(ctx, "cli_path_claude", stubCLI(t)))
	backend := ptymanager.NewTmuxBackend(client, cfg.LogDir)
	t.Cleanup(func() { backend.Close() })
	terminal := ptymanager.NewTmuxSessionTerminal(client)
	h := NewSessionsHandler(db, cfg, backend, terminal, nil)

	res, err := h.launchSession(ctx, launchSpec{WorkDir: dataDir, AgentType: "claude"}) // no backend named, as a dashboard launch may
	require.NoError(t, err)
	assert.Equal(t, "tmux", res["terminal"], "the agent runs on tmux")
	assert.Equal(t, "pty", res["launch_path"], "through the terminal backend, which here is tmux")
	assert.NotContains(t, res, "backend", "the old name is gone: it read as the terminal")
	id, _ := res["session_id"].(string)
	require.NotEmpty(t, id)
	assert.True(t, terminal.HasSession(ctx, naming.SessionName("claude", id)), "its session is really in tmux")
	ls, err := ss.GetLiveSession(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, ls.Backend)
	assert.Equal(t, res["launch_path"], *ls.Backend, "the answer and the stored label agree")
	b, _ := json.Marshal(ls)
	assert.Contains(t, string(b), `"launch_path":"pty"`)
	assert.NotContains(t, string(b), `"backend"`, "a live session's JSON says launch_path too")
}
