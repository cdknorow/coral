package routes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #3636 on a real pty backend: a coral_stopped agent is woken by the pass, resuming its
// conversation (--resume <id>); an agent the Operator put to sleep stays asleep.
func TestAutoWakeOnARealPTYResumesTheConversation(t *testing.T) {
	dataDir, err := os.MkdirTemp(shortTempBase(), "caw")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	cfg := config.Load(dataDir)
	cfg.LogDir = filepath.Join(dataDir, "logs")
	require.NoError(t, os.MkdirAll(cfg.LogDir, 0o700))
	db, err := storetest.Open(t, filepath.Join(dataDir, "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	ctx := context.Background()
	require.NoError(t, ss.SetSetting(ctx, "cli_path_claude", stubCLI(t))) // never a real agent in a test
	stopped := "5c1e0a7d-2b3c-4d5e-8f60-718293a4b5c6"
	slept := "6d2f1b8e-3c4d-4e6f-9071-8293a4b5c6d7"
	for id, reason := range map[string]string{stopped: store.SleepReasonCoralStopped, slept: ""} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentName: "a", AgentType: "claude", WorkingDir: dataDir}))
		require.NoError(t, ss.SetSessionSleepingFor(ctx, id, reason))
	}

	pty := ptymanager.NewPTYBackend()
	t.Cleanup(func() { pty.Close() })
	h := NewSessionsHandler(db, cfg, pty, ptymanager.NewPTYSessionTerminal(pty), nil)
	h.autoWake.sleep = func(c context.Context, d time.Duration) bool { return c.Err() == nil }
	h.AutoWakeAfterRestart(ctx)

	name := naming.SessionName("claude", stopped)
	require.True(t, pty.IsRunning(name), "woken on pty")
	assert.False(t, pty.IsRunning(naming.SessionName("claude", slept)), "the Operator's sleep stays")
	deadline := time.Now().Add(5 * time.Second)
	var replay string
	for time.Now().Before(deadline) {
		b, _ := pty.Replay(name)
		// Escape sequences out: PowerShell's PSReadLine colours the echoed line on Windows, between the words.
		if replay = statusActivityANSI.ReplaceAllString(string(b), ""); strings.Contains(replay, "--resume") {
			break
		}
		time.Sleep(100 * time.Millisecond) // sleep-ok: the shell echoes the typed launch line
	}
	assert.Contains(t, replay, "--resume "+stopped, "it resumes its own conversation")
	ls, _ := ss.GetLiveSession(ctx, stopped)
	assert.Equal(t, 0, ls.IsSleeping)
	assert.Nil(t, ls.SleepReason)
}
