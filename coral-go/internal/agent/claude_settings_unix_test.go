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

// #3591: the global settings file may be a symlink to a regular file (dotfiles setups), but
// a FIFO where it goes is ignored and never blocks the launch.
func TestReadSettingsFileFollowsLinksButRefusesFIFOs(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	require.NoError(t, os.WriteFile(good, []byte(`{"env":{"A":"1"}}`), 0o600))
	link := filepath.Join(dir, "link.json")
	require.NoError(t, os.Symlink(good, link))
	assert.Equal(t, map[string]interface{}{"A": "1"}, readSettingsFile(link)["env"], "a symlink to a regular file is read")
	fifoLink := filepath.Join(dir, "fifo-link.json")

	fifo := filepath.Join(dir, "fifo.json")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	require.NoError(t, os.Symlink(fifo, fifoLink))
	done := make(chan map[string]interface{}, 1)
	go func() { done <- readSettingsFile(fifoLink) }() // a link to a FIFO: still refused
	select {
	case got := <-done:
		assert.Empty(t, got, "a FIFO")
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked the launch")
	}
}
