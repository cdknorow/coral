package background

import (
	"context"

	"github.com/cdknorow/coral/internal/board"
)

// Test-only hooks, kept out of the shipped binary (#3824, Go cleanup 2: audit #3780 section B).

// SetPostMessageFn provides an isolated delivery seam for retry tests.
func (m *BoardHealthMonitor) SetPostMessageFn(fn func(context.Context, string, string, string, *string) (*board.Message, error)) {
	if fn != nil {
		m.postMessage = fn
	}
}

// SetDiscoverFn sets a custom agent discovery function (for dependency injection).
func (p *GitPoller) SetDiscoverFn(fn func(ctx context.Context) ([]AgentInfo, error)) {
	p.discoverFn = fn
}
