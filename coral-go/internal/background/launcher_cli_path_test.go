package background

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

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
