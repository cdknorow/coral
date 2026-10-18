package background

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// runOnTicker (#4599 second ranking, item 9: the loop six background services each wrote out) runs fn on every
// tick and returns the context's error once it is done.
func TestRunOnTicker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan struct{}, 10)
	done := make(chan error, 1)
	go func() { done <- runOnTicker(ctx, time.Millisecond, func() { ticks <- struct{}{} }) }()
	for i := 0; i < 3; i++ {
		select {
		case <-ticks:
		case <-time.After(5 * time.Second):
			t.Fatal("no tick")
		}
	}
	cancel()
	select {
	case err := <-done:
		require.True(t, errors.Is(err, context.Canceled), "%v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("did not return once cancelled")
	}
}
