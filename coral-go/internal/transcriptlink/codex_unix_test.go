//go:build !windows

package transcriptlink

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexLinkFileIsARegularFile(t *testing.T) {
	home := codexHomeFor(t)
	links := filepath.Join(home, "coral-session-links")

	// A symlinked link file is not read.
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	b, _ := json.Marshal(CodexLink{ThreadID: thread})
	require.NoError(t, os.WriteFile(target, b, 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(links, sid+".json")))
	assert.Equal(t, CodexLink{}, Codex(sid))
	require.NoError(t, os.Remove(filepath.Join(links, sid+".json")))

	// A FIFO in its place doesn't block the reader.
	require.NoError(t, syscall.Mkfifo(filepath.Join(links, sid+".json"), 0o600))
	done := make(chan CodexLink, 1)
	go func() { done <- Codex(sid) }()
	select {
	case l := <-done:
		assert.Equal(t, CodexLink{}, l)
	case <-time.After(3 * time.Second):
		t.Fatal("Codex blocked on a FIFO link file")
	}
	require.NoError(t, os.Remove(filepath.Join(links, sid+".json")))

	// An oversized link file is refused.
	require.NoError(t, os.WriteFile(filepath.Join(links, sid+".json"), make([]byte, maxLinkBytes+1), 0o600))
	assert.Equal(t, CodexLink{}, Codex(sid))
}
