//go:build !windows

package ptymanager

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/tmux"
)

// #3602 (Lead's NIT): naming.LogFile gives no path for an unsafe agent type or session id, and
// then no session is made at all: the tmux backend doesn't create a session piped to an empty path, and
// the PTY backend doesn't start a process with no log.
func TestSpawnRefusesASessionWithNoLogPath(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s")
	client := tmux.NewClient(t.TempDir())
	client.SocketPath = sock
	client.FallbackToDefault = false
	t.Cleanup(func() { exec.Command("tmux", "-S", sock, "kill-server").Run() })
	b := NewTmuxBackend(client, t.TempDir())
	t.Cleanup(func() { b.Close() })

	const sid = "5c1e0a7d-2b3c-4d5e-8f60-718293a4b5c6"
	err := b.Spawn("bad", "../x", t.TempDir(), sid, "echo hi", 80, 24)
	require.ErrorIs(t, err, errNoLogPath)
	assert.Empty(t, b.ListSessions(), "nothing tracked")
	if exec.Command("tmux", "-V").Run() == nil {
		assert.Error(t, exec.Command("tmux", "-S", sock, "has-session").Run(), "no tmux session was made")
	}

	s, err := newSession("bad", "../x", t.TempDir(), sid, "echo hi", 80, 24)
	if s != nil {
		s.kill()
	}
	assert.ErrorIs(t, err, errNoLogPath)
}
