package routes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// launchRecorder is the real terminal, with a note of every launch command it was given.
type launchRecorder struct {
	ptymanager.SessionTerminal
	mu   sync.Mutex
	sent map[string][]string
}

func (l *launchRecorder) SendToTarget(ctx context.Context, target, command string) error {
	l.mu.Lock()
	if l.sent == nil {
		l.sent = map[string][]string{}
	}
	l.sent[target] = append(l.sent[target], command)
	l.mu.Unlock()
	return l.SessionTerminal.SendToTarget(ctx, target, command)
}

// sentTo is every launch command sent to a target that names the session (a target may carry a
// pane or window suffix).
func (l *launchRecorder) sentTo(name string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for target, cmds := range l.sent {
		if strings.HasPrefix(target, name) {
			out = append(out, cmds...)
		}
	}
	return out
}

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
	launches := &launchRecorder{SessionTerminal: ptymanager.NewPTYSessionTerminal(pty)}
	h := NewSessionsHandler(db, cfg, pty, launches, nil)
	h.autoWake.sleep = func(c context.Context, d time.Duration) bool { return c.Err() == nil }
	h.AutoWakeAfterRestart(ctx)

	name := naming.SessionName("claude", stopped)
	require.True(t, pty.IsRunning(name), "woken on pty")
	assert.False(t, pty.IsRunning(naming.SessionName("claude", slept)), "the Operator's sleep stays")
	// The launch line goes to the terminal as it is, or (a long one, every OS) through a private
	// handoff script whose path is all the screen shows; so check the command the wake sent.
	sent := launches.sentTo(name)
	require.NotEmpty(t, sent, "the woken agent's terminal was given its launch command")
	assert.Contains(t, sent[len(sent)-1], "--resume "+stopped, "it resumes its own conversation")
	assert.Empty(t, launches.sentTo(naming.SessionName("claude", slept)), "nothing is launched for the Operator's sleep")
	ls, _ := ss.GetLiveSession(ctx, stopped)
	assert.Equal(t, 0, ls.IsSleeping)
	assert.Nil(t, ls.SleepReason)
}
