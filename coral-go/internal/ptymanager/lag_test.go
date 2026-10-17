package ptymanager

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4161: a subscriber whose buffer is full is cut off and recorded as lagged; the others still
// get every frame.
func TestFanOutCutsOffAFullSubscriber(t *testing.T) {
	fast, slow := make(chan []byte, 4), make(chan []byte, 1)
	subs := map[string]chan []byte{"fast": fast, "slow": slow}
	lagged := map[string]bool{}
	fanOut(subs, lagged, []byte("1"))
	drained := make(chan struct{})
	go func() {
		for range fast {
		}
		close(drained)
	}()
	fanOut(subs, lagged, []byte("2")) // slow's one slot is taken: it is cut off
	fanOut(subs, lagged, []byte("3"))
	assert.Equal(t, map[string]bool{"slow": true}, lagged)
	assert.NotContains(t, subs, "slow")
	assert.Contains(t, subs, "fast")
	got := <-slow
	assert.Equal(t, "1", string(got), "what it had queued is kept")
	_, open := <-slow
	assert.False(t, open, "then its channel is closed")
	close(fast) // the reader ends with the test (#4167's leak guard)
	<-drained
}

// A PTY viewer that reads nothing while the agent prints is cut off and reported lagged once;
// attaching again starts it afresh.
func TestPTYBackendLaggingSubscriberIsCutOff(t *testing.T) {
	canFork(t)
	m := NewPTYBackend()
	defer m.Close()
	require.NoError(t, m.Spawn("lagger", "test", t.TempDir(), "sid-lagger", "yes 0123456789abcdefghijklmnopqrstuvwxyz", 80, 24))
	ch, err := m.Attach("lagger", "viewer")
	require.NoError(t, err)

	// Read nothing until the buffer has filled, then drain: the channel must end closed.
	require.Eventually(t, func() bool { return len(ch) == cap(ch) }, 20*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond) // more output arrives: the full viewer is cut off
	drained := make(chan struct{})
	go func() {
		for range ch {
		}
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("a viewer that read nothing was never cut off")
	}
	assert.True(t, m.Lagged("lagger", "viewer"))
	assert.False(t, m.Lagged("lagger", "viewer"), "reported once")

	ch2, err := m.Attach("lagger", "viewer")
	require.NoError(t, err)
	select {
	case _, ok := <-ch2:
		assert.True(t, ok, "the new subscription gets output")
	case <-time.After(5 * time.Second):
		t.Fatal("no output after attaching again")
	}
	m.Unsubscribe("lagger", "viewer")
	assert.False(t, m.Lagged("lagger", "viewer"))
}
