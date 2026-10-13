package background

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agent"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/store"
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

// Job and scheduled runs use Settings > CLI Paths like a dashboard launch, not
// whatever binary is first on PATH (#3205).
func TestJobRunsUseTheConfiguredCLIPath(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	ss := store.NewSessionStore(db)
	rt := &spawnRecorder{}
	l := NewAgentLauncher(rt, ss, 8450)

	_, err := l.LaunchAgent(ctx, t.TempDir(), at.Claude, "job", "", nil, true, "p", "", "")
	require.NoError(t, err)

	require.NoError(t, ss.SetSetting(ctx, agent.CLIPathSettingKey(at.Claude), "/opt/custom/bin/claude"))
	_, err = l.LaunchAgent(ctx, t.TempDir(), at.Claude, "job", "", nil, true, "p", "", "")
	require.NoError(t, err)

	rt.mu.Lock()
	defer rt.mu.Unlock()
	require.Len(t, rt.commands, 2)
	assert.False(t, strings.Contains(rt.commands[0], "/opt/custom/bin/claude"), "no setting: the default lookup")
	assert.Contains(t, rt.commands[1], "/opt/custom/bin/claude", "the configured CLI path: %s", rt.commands[1])
}
