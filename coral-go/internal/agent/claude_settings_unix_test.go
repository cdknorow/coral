//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3591: a symlink or a FIFO where the global settings file goes is ignored, and never
// blocks the launch.
func TestReadSettingsFileRefusesLinksAndFIFOs(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	require.NoError(t, os.WriteFile(good, []byte(`{"env":{"A":"1"}}`), 0o600))
	link := filepath.Join(dir, "link.json")
	require.NoError(t, os.Symlink(good, link))
	assert.Empty(t, readSettingsFile(link), "a symlink")

	fifo := filepath.Join(dir, "fifo.json")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	done := make(chan map[string]interface{}, 1)
	go func() { done <- readSettingsFile(fifo) }()
	select {
	case got := <-done:
		assert.Empty(t, got, "a FIFO")
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked the launch")
	}
}
