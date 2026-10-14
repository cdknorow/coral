package background

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
)

// fgRuntimes report a foreground command per session name, like a terminal whose
// agent CLI may have exited back to its shell (#3219).
type fgState struct {
	mu sync.Mutex
	fg map[string]string
}

func (f *fgState) set(name, cmd string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fg == nil {
		f.fg = map[string]string{}
	}
	f.fg[name] = cmd
}

// AgentShellIdle: a shell name set for the session means its shell is idle (the CLI
// exited); anything else means the CLI is running.
func (f *fgState) AgentShellIdle(_ context.Context, name, _, _ string) (bool, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.fg[name]
	if !ok || c == "" {
		return false, "", false
	}
	return tmux.IsShellCommand(c), c, true
}

type fgMockRuntime struct {
	mockRuntime
	fgState
}

type fgReconRuntime struct {
	reconMockRuntime
	fgState
}

func (m *mockRuntime) sentTo(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.sent {
		if c.session == name {
			n++
		}
	}
	return n
}

// ── Reconciler: an agent whose CLI exited is marked sleeping ─────────────

func newExitedFixture(t *testing.T) (*store.SessionStore, *fgReconRuntime, *SessionReconciler) {
	t.Helper()
	ss, _, _ := newReconcilerFixture(t)
	rt := &fgReconRuntime{reconMockRuntime: reconMockRuntime{alive: map[string]bool{}}}
	r := NewSessionReconciler(ss, rt, time.Hour)
	// Past the launch grace.
	r.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	return ss, rt, r
}

func TestReconciler_ExitedAgentIsMarkedSleeping(t *testing.T) {
	ss, rt, r := newExitedFixture(t)
	ctx := context.Background()
	registerAwake(t, ss, "claude", testUUID)
	rt.listed = []AgentInfo{{AgentType: "claude", SessionID: testUUID}}
	rt.set(FormatSessionName("claude", testUUID), "zsh")

	r.reconcileOnce(ctx)
	assert.False(t, isSleeping(t, ss, testUUID), "one pass is not enough")
	r.reconcileOnce(ctx)
	assert.True(t, isSleeping(t, ss, testUUID), "marked sleeping after reconcileMissThreshold passes")
	assert.Empty(t, rt.killed, "the shell is left as it is (Wake resumes in it)")
}

func TestReconciler_RunningAgentIsNotMarked(t *testing.T) {
	ss, rt, r := newExitedFixture(t)
	ctx := context.Background()
	registerAwake(t, ss, "claude", testUUID)
	rt.listed = []AgentInfo{{AgentType: "claude", SessionID: testUUID}}
	rt.set(FormatSessionName("claude", testUUID), "node")
	for i := 0; i < 3; i++ {
		r.reconcileOnce(ctx)
	}
	assert.False(t, isSleeping(t, ss, testUUID))
}

func TestReconciler_ShellBlipDoesNotMark(t *testing.T) {
	ss, rt, r := newExitedFixture(t)
	ctx := context.Background()
	registerAwake(t, ss, "claude", testUUID)
	rt.listed = []AgentInfo{{AgentType: "claude", SessionID: testUUID}}
	name := FormatSessionName("claude", testUUID)
	rt.set(name, "zsh")
	r.reconcileOnce(ctx)
	rt.set(name, "node") // the CLI is back (e.g. the user resumed it by hand)
	r.reconcileOnce(ctx)
	rt.set(name, "zsh")
	r.reconcileOnce(ctx)
	assert.False(t, isSleeping(t, ss, testUUID), "the count resets when the CLI is seen again")
}

func TestReconciler_ExitedWithinLaunchGraceIsNotMarked(t *testing.T) {
	ss, rt, r := newExitedFixture(t)
	r.now = time.Now // the agent was just launched
	ctx := context.Background()
	registerAwake(t, ss, "claude", testUUID)
	rt.listed = []AgentInfo{{AgentType: "claude", SessionID: testUUID}}
	rt.set(FormatSessionName("claude", testUUID), "zsh")
	for i := 0; i < 3; i++ {
		r.reconcileOnce(ctx)
	}
	assert.False(t, isSleeping(t, ss, testUUID), "a fresh launch's shell is not an exited agent")
}

// Orchestrator's must: a 'terminal' agent's shell IS the agent.
func TestReconciler_TerminalAgentIsNeverMarked(t *testing.T) {
	ss, rt, r := newExitedFixture(t)
	ctx := context.Background()
	registerAwake(t, ss, "terminal", testUUID)
	rt.listed = []AgentInfo{{AgentType: "terminal", SessionID: testUUID}}
	rt.set(FormatSessionName("terminal", testUUID), "zsh")
	for i := 0; i < 3; i++ {
		r.reconcileOnce(ctx)
	}
	assert.False(t, isSleeping(t, ss, testUUID))
}

// ── Notifier: no nudge typed into a bare shell ──────────────────────────

func nudgeFixture(t *testing.T, agentType string) (*fgMockRuntime, *BoardNotifier, string) {
	t.Helper()
	bs := testBoardStore(t)
	rt := &fgMockRuntime{}
	ctx := context.Background()
	n := NewBoardNotifier(bs, rt, time.Second)
	n.SetIsPausedFn(func(string) bool { return false })
	name := FormatSessionName(agentType, testUUID)
	_, err := bs.Subscribe(ctx, "proj", "Worker", "Worker", name, nil, nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj", "Poster", "Poster", "claude-poster", nil, nil, "all")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "proj", "Poster", "@Worker hello", nil)
	require.NoError(t, err)
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentType: agentType, SessionID: testUUID, DisplayName: "Worker"}}, nil
	})
	return rt, n, name
}

func TestBoardNotifier_NoNudgeIntoABareShell(t *testing.T) {
	rt, n, name := nudgeFixture(t, "claude")
	ctx := context.Background()
	rt.set(name, "zsh")
	require.NoError(t, n.RunOnce(ctx))
	assert.Equal(t, 0, rt.sentTo(name), "nothing typed into the shell the exited CLI left")

	rt.set(name, "node") // the agent runs again
	require.NoError(t, n.RunOnce(ctx))
	assert.Equal(t, 1, rt.sentTo(name), "a running agent is nudged as before")
}

// The notifier skips 'terminal' sessions anyway (they don't read the board); the guard
// itself never blocks one, since a terminal agent's shell is the agent.
func TestSkipIfAgentExitedNeverBlocksATerminalAgent(t *testing.T) {
	rt := &fgMockRuntime{}
	name := FormatSessionName("terminal", testUUID)
	rt.set(name, "zsh")
	assert.False(t, skipIfAgentExited(context.Background(), rt, nil, name, "test"))
	claude := FormatSessionName("claude", testUUID)
	rt.set(claude, "zsh")
	assert.True(t, skipIfAgentExited(context.Background(), rt, nil, claude, "test"))
}

// ── Launcher: the first prompt waits for the CLI ─────────────────────────

func TestLauncherPromptWaitsForTheCLI(t *testing.T) {
	old := promptStartWait
	promptStartWait = 2 * time.Second
	t.Cleanup(func() { promptStartWait = old })
	rt := &fgMockRuntime{}
	l := NewAgentLauncher(rt, nil, 0)
	name := FormatSessionName("claude", testUUID)

	rt.set(name, "zsh")
	go func() {
		time.Sleep(4 * time.Second) // sleep-ok: the CLI takes the foreground 1 s after the 3 s initial delay
		rt.set(name, "node")
	}()
	require.NoError(t, l.SendPrompt(context.Background(), testUUID, name, "claude", "do the task"))
	assert.Equal(t, 1, rt.sentTo(name), "sent once the CLI is in the foreground")
}

func TestLauncherPromptNeverTypedIntoAShell(t *testing.T) {
	old := promptStartWait
	promptStartWait = 500 * time.Millisecond
	t.Cleanup(func() { promptStartWait = old })
	rt := &fgMockRuntime{}
	l := NewAgentLauncher(rt, nil, 0)
	name := FormatSessionName("claude", testUUID)
	rt.set(name, "zsh") // the CLI never starts
	require.NoError(t, l.SendPrompt(context.Background(), testUUID, name, "claude", "do the task"))
	assert.Equal(t, 0, rt.sentTo(name), "the prompt is not typed into the shell")
}
