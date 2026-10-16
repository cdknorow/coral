//go:build !windows

package tmux

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #3999 (finding 4): readProcTable holds procTableMu while ps runs, so a ps that never returns used
// to block every caller for good. It is now killed at psTimeout and the caller gets an error.
func TestReadProcTableGivesUpOnAHungPs(t *testing.T) {
	dir := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := psTimeout
	psTimeout = 300 * time.Millisecond
	t.Cleanup(func() { psTimeout = old })
	procTableMu.Lock()
	procTableLast = nil
	procTableMu.Unlock()

	start := time.Now()
	_, err := readProcTable()
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 10*time.Second, "returned at the deadline, not after 30 s")
	// The lock was released: a second call returns too (it would hang here if the first still held it).
	done := make(chan struct{})
	go func() { _, _ = readProcTable(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("procTableMu is still held")
	}
}
