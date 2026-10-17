package ptymanager

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4161, the tmux backend: a viewer that reads nothing while the agent prints is cut off and
// reported lagged once, and the tail keeps serving the others.
func TestTmuxBackendLaggingSubscriberIsCutOff(t *testing.T) {
	b := newTestTmuxBackend(t)
	require.NoError(t, b.Spawn("lagger", "claude", t.TempDir(), "sid-lag", "yes 0123456789abcdefghijklmnopqrstuvwxyz", 80, 24))
	defer b.Kill("lagger")
	time.Sleep(300 * time.Millisecond)

	stuck, err := b.Attach("lagger", "stuck")
	require.NoError(t, err)
	reader, err := b.Attach("lagger", "reader")
	require.NoError(t, err)
	got := make(chan int, 1)
	go func() {
		n := 0
		for d := range reader {
			n += len(d)
			if n > 1<<20 {
				select {
				case got <- n:
				default:
				}
			}
		}
	}()

	require.Eventually(t, func() bool { return len(stuck) == cap(stuck) }, 30*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	drained := make(chan struct{})
	go func() {
		for range stuck {
		}
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("a viewer that read nothing was never cut off")
	}
	assert.True(t, b.Lagged("lagger", "stuck"))
	assert.False(t, b.Lagged("lagger", "stuck"), "reported once")
	assert.False(t, b.Lagged("lagger", "reader"))
	select {
	case <-got:
	case <-time.After(20 * time.Second):
		t.Fatal("the reading viewer stopped getting output")
	}
	b.Unsubscribe("lagger", "stuck")
	b.Unsubscribe("lagger", "reader")
}
