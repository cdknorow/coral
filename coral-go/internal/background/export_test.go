package background

import (
	"context"

)

// Test-only hooks, kept out of the shipped binary (#3824, Go cleanup 2: audit #3780 section B).

// SetDiscoverFn sets a custom agent discovery function (for dependency injection).
func (p *GitPoller) SetDiscoverFn(fn func(ctx context.Context) ([]AgentInfo, error)) {
	p.discoverFn = fn
}
