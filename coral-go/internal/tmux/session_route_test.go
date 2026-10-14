package tmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An exact-match target ("=name", as the terminal watchdog's has-session uses)
// must route to the socket the session was found on, like a plain "name". With
// the "=" kept, the cache missed: the command ran on the primary socket first,
// failed and logged an exec error, then retried on the fallback, once a second
// per open terminal of a session on the default socket (#3243 review).
func TestRunRoutesExactMatchTargetsToTheSessionSocket(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	stub := filepath.Join(dir, "tmux")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\necho \"$*\" >> '"+calls+"'\n"), 0o755))

	c := &Client{
		TmuxBin:           stub,
		SocketPath:        filepath.Join(dir, "primary.sock"),
		FallbackToDefault: true,
		sessionSockets:    map[string]string{"claude-abc": filepath.Join(dir, "other.sock")},
	}
	for _, target := range []string{"=claude-abc", "claude-abc", "=claude-abc:0.0"} {
		require.NoError(t, os.RemoveAll(calls))
		_, err := c.run(context.Background(), "has-session", "-t", target)
		require.NoError(t, err)
		data, err := os.ReadFile(calls)
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		assert.Len(t, lines, 1, "target %q: one tmux call", target)
		assert.True(t, strings.HasPrefix(lines[0], "-S "+filepath.Join(dir, "other.sock")+" "), "target %q ran on %q", target, lines[0])
	}
}
