package tmux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3728: with the default-socket fallback on, ListPanes reads tmux's default socket only when
// its socket file is there; with none, one tmux run instead of two.
func TestListPanesSkipsAnAbsentDefaultServer(t *testing.T) {
	skipWithoutUnixTmux(t)
	tmpdir := t.TempDir()
	t.Setenv("TMUX_TMPDIR", tmpdir)
	t.Setenv("TMUX", "")
	var runs atomic.Int64
	runObserver = func(args []string) {
		if len(args) > 0 && args[0] == "list-panes" {
			runs.Add(1)
		}
	}
	t.Cleanup(func() { runObserver = nil })
	c := &Client{TmuxBin: "tmux", SocketPath: filepath.Join(tmpdir, "coral.sock"), FallbackToDefault: true}

	assert.False(t, defaultServerMayRun())
	_, err := c.ListPanes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), runs.Load(), "no default server: only Coral's socket is listed")

	sock := filepath.Join(tmpdir, fmt.Sprintf("tmux-%d", os.Getuid()), "default")
	require.NoError(t, os.MkdirAll(filepath.Dir(sock), 0o700))
	require.NoError(t, os.WriteFile(sock, nil, 0o600)) // stands in for the socket: only its presence counts
	assert.True(t, defaultServerMayRun())
	runs.Store(0)
	_, err = c.ListPanes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), runs.Load(), "a default server may run: it is listed too")

	t.Setenv("TMUX", "/tmp/elsewhere,1,0")
	require.NoError(t, os.Remove(sock))
	assert.True(t, defaultServerMayRun(), "inside tmux the default server is $TMUX's: always listed")
}
