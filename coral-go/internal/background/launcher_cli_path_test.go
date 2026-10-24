package background

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// spawnRecorder is a runtime that records the command each agent is spawned with.
type spawnRecorder struct {
	mockRuntime
	mu       sync.Mutex
	commands []string
}

func (s *spawnRecorder) SpawnAgent(_ context.Context, _, _, _, command string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, command)
	return nil
}

// Job and scheduled runs use Settings > CLI paths like a dashboard launch, not
// whatever binary is first on PATH (#3205).
func TestJobRunsUseTheConfiguredCLIPath(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	ss := store.NewSessionStore(db)
	rt := &spawnRecorder{}
	l := NewAgentLauncher(rt, ss, 8450)

	_, err := l.LaunchAgent(ctx, t.TempDir(), at.Claude, "job", "", nil, true, "p", "", "")
	require.NoError(t, err)

	name := "claude"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	custom := filepath.Join(t.TempDir(), "custom", name)
	require.NoError(t, os.MkdirAll(filepath.Dir(custom), 0o755))
	require.NoError(t, os.WriteFile(custom, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, ss.SetSetting(ctx, agent.CLIPathSettingKey(at.Claude), custom))
	_, err = l.LaunchAgent(ctx, t.TempDir(), at.Claude, "job", "", nil, true, "p", "", "")
	require.NoError(t, err)

	rt.mu.Lock()
	defer rt.mu.Unlock()
	require.Len(t, rt.commands, 2)
	assert.False(t, strings.Contains(rt.commands[0], "custom"), "no setting: the default lookup")
	assert.Contains(t, rt.commands[1], filepath.Base(custom), "the configured CLI path: %s", rt.commands[1])
}

// #3602 W13: LaunchAgent refuses an agent type it doesn't know before it touches anything.
func TestLaunchAgentValidatesAgentType(t *testing.T) {
	l := &AgentLauncher{}
	for _, bad := range []string{"../../x", "no-such-agent"} {
		if _, err := l.LaunchAgent(context.Background(), t.TempDir(), bad, "n", "", nil, true, "p", "", ""); err == nil {
			t.Errorf("LaunchAgent accepted agent type %q", bad)
		}
	}
}

// Security's #3602 question: an empty agent type (an older scheduled job, a workflow agent step)
// is Claude, defaulted before the log path is built, so the launch still gets its log under the
// log folder (naming.LogFile refuses an empty type).
func TestLaunchAgentWithNoTypeStillGetsItsLog(t *testing.T) {
	logs := t.TempDir()
	naming.SetDefaultLogDir(logs)
	t.Cleanup(func() { naming.SetDefaultLogDir("") })
	rt := &spawnRecorder{}
	l := NewAgentLauncher(rt, store.NewSessionStore(setupTestDB(t)), 8450)
	_, err := l.LaunchAgent(context.Background(), t.TempDir(), "", "job", "", nil, true, "p", "", "")
	require.NoError(t, err)
	matches, _ := filepath.Glob(filepath.Join(logs, "claude_coral_*.log"))
	assert.Len(t, matches, 1, "the log was made under the log folder, as Claude's")
	rt.mu.Lock()
	defer rt.mu.Unlock()
	require.Len(t, rt.commands, 1)
	assert.Contains(t, rt.commands[0], "claude", "launched as Claude")
}

// #6767 (Codex Test's #6766): a scheduled or one-shot agent is started with this server's address, data folder and its own session name, like a dashboard launch. Without them its hooks and coral-board dialed
// the default port (:8420), so a server on any other port stored none of its events (an open permission box showed as "working", "starting" never cleared).
func TestScheduledAgentsAreLaunchedWithThisServersEnvironment(t *testing.T) {
	ctx := context.Background()
	for _, agentType := range []string{at.Claude, at.Codex} {
		rt := &spawnRecorder{}
		l := NewAgentLauncher(rt, store.NewSessionStore(setupTestDB(t)), 8450)
		dataDir := t.TempDir()
		l.SetServer("127.0.0.1", dataDir)
		res, err := l.LaunchAgent(ctx, t.TempDir(), agentType, "job", "", nil, true, "p", "", "")
		require.NoError(t, err, agentType)
		rt.mu.Lock()
		cmd := rt.commands[0]
		rt.mu.Unlock()
		for _, want := range []string{"CORAL_PORT", "8450", "CORAL_URL", "http://127.0.0.1:8450", "CORAL_SESSION_NAME", res.SessionName, "CORAL_DATA_DIR", dataDir} {
			assert.Contains(t, cmd, want, "%s: %s", agentType, want)
		}
	}
}

// taggingRecorder is a spawnRecorder on a runtime that tags its agents' runs, as the tmux one does.
type taggingRecorder struct{ spawnRecorder }

func (*taggingRecorder) TagsRuns() bool { return true }

// #6770 (#6767 point 4): a scheduled agent on tmux carries the run tag a dashboard launch does, and it is recorded on its live row, where a Kill finds it by session id; the built-in terminal tags its own
// processes, so nothing is exported there.
func TestScheduledAgentsCarryARunTagOnTmuxOnly(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	ss := store.NewSessionStore(db)

	tagged := &taggingRecorder{}
	res, err := NewAgentLauncher(tagged, ss, 8450).LaunchAgent(ctx, t.TempDir(), at.Claude, "job", "", nil, true, "p", "", "")
	require.NoError(t, err)
	tagged.mu.Lock()
	cmd := tagged.commands[0]
	tagged.mu.Unlock()
	tag := ss.RunTagOf(ctx, res.SessionID)
	require.Len(t, tag, 32, "a tag is recorded on the live row")
	assert.Contains(t, cmd, "CORAL_PTY_RUN", "exported to the agent and everything it starts")
	assert.Contains(t, cmd, tag, "the same tag the row holds")

	plain := &spawnRecorder{}
	res2, err := NewAgentLauncher(plain, ss, 8450).LaunchAgent(ctx, t.TempDir(), at.Claude, "job", "", nil, true, "p", "", "")
	require.NoError(t, err)
	plain.mu.Lock()
	cmd2 := plain.commands[0]
	plain.mu.Unlock()
	assert.NotContains(t, cmd2, "CORAL_PTY_RUN", "the built-in terminal tags its own")
	assert.Empty(t, ss.RunTagOf(ctx, res2.SessionID))
}

// stampRecorder is a runtime that records when each agent was spawned.
type stampRecorder struct {
	mockRuntime
	mu     sync.Mutex
	stamps []time.Time
}

func (s *stampRecorder) SpawnAgent(_ context.Context, _, _, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stamps = append(s.stamps, time.Now())
	return nil
}

// #6839 (Codex Test's #6828): two Codex runs fired in the same minute started at once against one Codex home's state database, and one exited during its state initialisation. Codex starts are now spaced
// apart (agent.SharedHomeStartGap); the other agents start at once as before.
func TestCodexRunsFiredTogetherAreStartedApart(t *testing.T) {
	ctx := context.Background()
	defer func(g time.Duration) { agent.SharedHomeStartGap = g; agent.SetStartStagger(false) }(agent.SharedHomeStartGap)
	agent.SetStartStagger(true)
	agent.SharedHomeStartGap = 400 * time.Millisecond
	launch := func(agentType string, n int) []time.Time {
		rt := &stampRecorder{}
		l := NewAgentLauncher(rt, store.NewSessionStore(setupTestDB(t)), 8450)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := l.LaunchAgent(ctx, t.TempDir(), agentType, "job", "", nil, true, "p", "", "")
				require.NoError(t, err)
			}()
		}
		wg.Wait()
		rt.mu.Lock()
		defer rt.mu.Unlock()
		out := append([]time.Time(nil), rt.stamps...)
		sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
		return out
	}
	codex := launch(at.Codex, 3)
	require.Len(t, codex, 3)
	for i := 1; i < len(codex); i++ {
		assert.GreaterOrEqual(t, codex[i].Sub(codex[i-1]), 350*time.Millisecond, "codex start %d follows the previous by at least the gap", i)
	}
	claude := launch(at.Claude, 3)
	require.Len(t, claude, 3)
	assert.Less(t, claude[2].Sub(claude[0]), 300*time.Millisecond, "other agents are not delayed")
}
