//go:build !windows

package main

import (
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A restarted instance waits for the one it replaces to exit, and never longer than
// the limit (#3460).
func TestWaitForExit(t *testing.T) {
	p := exec.Command("sleep", "0.4")
	require.NoError(t, p.Start())
	done := make(chan struct{})
	go func() { p.Wait(); close(done) }() // reap it, as the old instance's exit would
	start := time.Now()
	waitForExit(p.Process.Pid, 10*time.Second)
	<-done
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond, "it waited for the exit")
	assert.Less(t, time.Since(start), 5*time.Second)

	long := exec.Command("sleep", "30")
	require.NoError(t, long.Start())
	t.Cleanup(func() { long.Process.Kill(); long.Wait() })
	start = time.Now()
	waitForExit(long.Process.Pid, 500*time.Millisecond)
	assert.Less(t, time.Since(start), 3*time.Second, "gives up at the limit")

	start = time.Now()
	waitForExit(0, 10*time.Second)
	waitForExit(-1, 10*time.Second)
	assert.Less(t, time.Since(start), time.Second, "no process to wait for")
}
