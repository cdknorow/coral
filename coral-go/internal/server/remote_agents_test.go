package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Outside hub mode there is no remote agent hub. RunRemoteAgents must still
// block until ctx ends; returning early made the background supervisor log
// "returned unexpectedly" and restart it every 5s forever.
func TestRunRemoteAgents_NonHubBlocksUntilCancel(t *testing.T) {
	env := newE2EEnv(t)
	h := newE2EHub(t, env, "", false)
	assert.False(t, h.srv.HasRemoteAgents())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); h.srv.RunRemoteAgents(ctx) }()

	select {
	case <-done:
		t.Fatal("RunRemoteAgents returned before ctx was cancelled")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunRemoteAgents did not return after ctx was cancelled")
	}
}

func TestHasRemoteAgents_HubMode(t *testing.T) {
	env := newE2EEnv(t)
	h := newE2EHub(t, env, "", true)
	require.True(t, h.srv.HasRemoteAgents())
}
