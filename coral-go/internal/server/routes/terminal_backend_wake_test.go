package routes

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// #3634, Security's tmux-to-pty case: an agent from a tmux run still runs in Coral's private
// tmux server when Coral starts on pty. Waking it is refused (no second copy starts); once the
// agents left there are ended, the wake starts it the pty way (the row says "tmux", which used
// to send the wake down the tmux path, where a pty Coral fails).
func TestPTYWakeOfAnAgentStillInTheOldTmuxServer(t *testing.T) {
	bin, ok := tmux.IsAvailable()
	if !ok {
		t.Skip("tmux not installed")
	}
	dataDir, err := os.MkdirTemp("/tmp", "cw") // a short path: tmux sockets have a length limit
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	socket := tmux.PrivateSocketPath(dataDir)
	t.Cleanup(func() { exec.Command(bin, "-S", socket, "kill-server").Run() })

	const sid = "0b2f6c1e-3d4a-4b5c-8d6e-7f8091a2b3c4"
	name := naming.SessionName("claude", sid)
	start := exec.Command(bin, "-S", socket, "new-session", "-d", "-s", name, "sleep 600")
	start.Env = append(os.Environ(), "TMUX=")
	out, err := start.CombinedOutput()
	require.NoError(t, err, string(out))

	cfg := config.Load(dataDir)
	cfg.LogDir = filepath.Join(dataDir, "logs")
	require.NoError(t, os.MkdirAll(cfg.LogDir, 0o700))
	db, err := storetest.Open(t, filepath.Join(dataDir, "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	tmuxRecorded := "tmux"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID: sid, AgentName: "qa", AgentType: "claude", WorkingDir: dataDir, Backend: &tmuxRecorded,
	}))
	require.NoError(t, ss.SetSessionSleeping(context.Background(), sid, true))
	// The CLI the wake types is a harmless program: never the real Claude CLI in a test.
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", stubCLI(t)))
	ls, err := ss.GetLiveSession(context.Background(), sid)
	require.NoError(t, err)

	pty := ptymanager.NewPTYBackend()
	t.Cleanup(func() { pty.Close() })
	h := NewSessionsHandler(db, cfg, pty, ptymanager.NewPTYSessionTerminal(pty), nil)

	err = h.wakeExistingSession(context.Background(), ls)
	assert.True(t, errors.Is(err, ErrStillInOldTmux), "refused while it still runs in the old tmux server: %v", err)
	assert.False(t, pty.IsRunning(name), "no second copy started")

	assert.Equal(t, []string{name}, tmux.EndPrivateAgentSessions(context.Background(), socket))
	err = h.wakeExistingSession(context.Background(), ls)
	require.NoError(t, err, "after End them the wake goes ahead")
	assert.True(t, pty.IsRunning(name), "woken the pty way, though the row says tmux")
}
