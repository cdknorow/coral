package background

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/tmux"
)

// fgRuntimes report a foreground command per session name, like a terminal whose
// agent CLI may have exited back to its shell (#3219).
type fgState struct {
	mu    sync.Mutex
	fg    map[string]string
	idles int // how many times AgentShellIdle was asked
}

func (f *fgState) idleCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.idles
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
	f.idles++
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
	ls, err := ss.GetLiveSession(ctx, testUUID)
	require.NoError(t, err)
	require.NotNil(t, ls.SleepReason, "it says why: the CLI ended (#6058)")
	assert.Equal(t, store.SleepReasonAgentExited, *ls.SleepReason)
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
	_, err := bs.Subscribe(ctx, "proj", "Worker", "Worker", name, nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj", "Poster", "Poster", "claude-poster", nil, "all")
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

// #3376 (reverses #3219's exemption): a terminal agent's terminal is a shell, where
// $( ) and backticks in automated text would run, so the guard always skips it.
func TestSkipIfAgentExitedAlwaysSkipsATerminalAgent(t *testing.T) {
	rt := &fgMockRuntime{}
	name := FormatSessionName("terminal", testUUID)
	rt.set(name, "zsh")
	assert.True(t, skipIfAgentExited(context.Background(), rt, nil, name, "test"))
	claude := FormatSessionName("claude", testUUID)
	rt.set(claude, "zsh")
	assert.True(t, skipIfAgentExited(context.Background(), rt, nil, claude, "test"))
}

// ── Launcher: the first prompt waits for the CLI ─────────────────────────

// fastPromptTimings shrinks SendPrompt's pauses (3 s and 250 ms in production) for a test (#4982).
func fastPromptTimings(t *testing.T, startWait time.Duration) {
	t.Helper()
	oldWait, oldInit, oldPoll := promptStartWait, promptInitialDelay, promptPollEvery
	promptStartWait, promptInitialDelay, promptPollEvery = startWait, 5*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { promptStartWait, promptInitialDelay, promptPollEvery = oldWait, oldInit, oldPoll })
}

func TestLauncherPromptWaitsForTheCLI(t *testing.T) {
	fastPromptTimings(t, 30*time.Second)
	rt := &fgMockRuntime{}
	l := NewAgentLauncher(rt, nil, 0)
	name := FormatSessionName("claude", testUUID)

	rt.set(name, "zsh")
	done := make(chan error, 1)
	go func() { done <- l.SendPrompt(context.Background(), testUUID, name, "claude", "do the task") }()
	// SendPrompt has looked at the terminal several times and found the shell in the foreground: nothing is sent yet.
	require.Eventually(t, func() bool { return rt.idleCalls() >= 5 }, 10*time.Second, time.Millisecond, "SendPrompt polls the terminal while the CLI has not started")
	assert.Zero(t, rt.sentTo(name), "nothing is typed while the shell is in the foreground")
	rt.set(name, "node") // the CLI takes the foreground
	require.NoError(t, <-done)
	assert.Equal(t, 1, rt.sentTo(name), "sent once the CLI is in the foreground")
}

func TestLauncherPromptNeverTypedIntoAShell(t *testing.T) {
	fastPromptTimings(t, 100*time.Millisecond)
	rt := &fgMockRuntime{}
	l := NewAgentLauncher(rt, nil, 0)
	name := FormatSessionName("claude", testUUID)
	rt.set(name, "zsh") // the CLI never starts
	require.NoError(t, l.SendPrompt(context.Background(), testUUID, name, "claude", "do the task"))
	assert.Equal(t, 0, rt.sentTo(name), "the prompt is not typed into the shell")
	assert.Greater(t, rt.idleCalls(), 1, "it looked until its wait ran out")
}

// #3268: Restart and Resume replace an old session's row with a new one, so the new
// agent gets its own launch grace (its created_at is the replacement's time), however
// old the agent it replaces was.
func TestReconciler_ReplacedSessionGetsItsOwnLaunchGrace(t *testing.T) {
	ss, rt, r := newExitedFixture(t)
	r.now = time.Now
	ctx := context.Background()
	const newID = "7d1c2b3a-4e5f-4a6b-8c9d-0e1f2a3b4c5d"
	registerAwake(t, ss, "claude", testUUID)
	require.NoError(t, ss.ReplaceLiveSession(ctx, testUUID, &store.LiveSession{
		SessionID: newID, AgentType: "claude", AgentName: "proj", WorkingDir: t.TempDir(),
	}))
	ls, err := ss.GetLiveSession(ctx, newID)
	require.NoError(t, err)
	created, err := time.Parse(store.ISOFormat, ls.CreatedAt)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), created, 10*time.Second, "the replacement's grace starts when it is created")

	rt.listed = []AgentInfo{{AgentType: "claude", SessionID: newID}}
	rt.set(FormatSessionName("claude", newID), "zsh") // its terminal shows a shell while the CLI starts
	for i := 0; i < 3; i++ {
		r.reconcileOnce(ctx)
	}
	assert.False(t, isSleeping(t, ss, newID), "inside its own launch grace it is not an exited agent")
}
