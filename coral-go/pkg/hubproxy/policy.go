package hubproxy

import (
	"context"

	"nhooyr.io/websocket"
)

// StatusAccessChanged closes a proxied WebSocket whose access was withdrawn (the server was
// revoked, or its access mode no longer allows the stream).
const StatusAccessChanged = websocket.StatusCode(4403)

// StreamPolicy lets the embedding hub constrain a proxied WebSocket for its whole life,
// beyond what was checked when it was opened.
type StreamPolicy struct {
	// AllowClientFrame is asked for every frame the browser sends. While it returns
	// false the frame is dropped (the terminal becomes read-only). Nil allows all.
	AllowClientFrame func() bool
	// Closed, when closed, ends the bridge with StatusAccessChanged.
	Closed <-chan struct{}
}

type policyKey struct{}

// WithStreamPolicy attaches a policy to the request context the Handler is served with.
func WithStreamPolicy(ctx context.Context, p StreamPolicy) context.Context {
	return context.WithValue(ctx, policyKey{}, p)
}

// PolicyFrom returns the policy on ctx, or the zero policy.
func PolicyFrom(ctx context.Context) StreamPolicy {
	p, _ := ctx.Value(policyKey{}).(StreamPolicy)
	return p
}
