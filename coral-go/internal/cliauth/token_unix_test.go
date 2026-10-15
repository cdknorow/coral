//go:build !windows

package cliauth

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3601 (weak site W22): the token is checked and read through one handle; a FIFO at its name is
// refused without blocking the command line.
func TestReadTokenNeverBlocksOnAFIFO(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, TokenFile), 0o600))
	done := make(chan error, 1)
	go func() {
		_, err := ReadToken(dir)
		done <- err
	}()
	select {
	case err := <-done:
		assert.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("ReadToken blocked on a FIFO")
	}
}
