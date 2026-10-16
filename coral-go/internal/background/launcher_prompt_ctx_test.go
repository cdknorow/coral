package background

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #3999 (finding 6): a scheduled run's prompt delivery runs on the run's context, so it ends when
// the run does instead of waiting on its own background context.
func TestPromptDeliveryEndsWithTheRunContext(t *testing.T) {
	l := &AgentLauncher{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	done := l.startPromptDelivery(ctx, "0123456789abcdef", "claude-0123", "claude", "do the thing")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the delivery ignored the cancelled run context (it waits its 3 s start delay on Background)")
	}
	assert.Less(t, time.Since(start), 2*time.Second)
}
