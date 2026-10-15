//go:build !windows

package routes

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Hard links and FIFOs: Unix (syscall.Mkfifo doesn't exist on Windows, so this lives in a
// file of its own and Windows go vet of routes builds).
func TestAutoAttachFileKinds(t *testing.T) {
	rig := newAttachRig(t)
	src := rig.write("orig.png", pngBytes(t, 4, 4))
	require.NoError(t, os.Link(src, filepath.Join(rig.wd, "hard.png")))
	unchanged(t, rig, "![x](hard.png)", "a hard link")
	unchanged(t, rig, "![x](orig.png)", "the original of a hard link (two links)")
	require.NoError(t, syscall.Mkfifo(filepath.Join(rig.wd, "pipe.png"), 0o644))
	done := make(chan string, 1)
	go func() { done <- rig.post("![x](pipe.png)") }()
	select {
	case out := <-done:
		assert.Equal(t, "![x](pipe.png)", out, "a FIFO")
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO blocked the post")
	}
}
