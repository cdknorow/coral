package tunnel

import (
	"context"
	"errors"
	"time"
)

// Relay (#3489, coral-hub PR #260): a Coral may ask Coral Hub to deliver a cross-team
// message to another Coral of the same account, and to list the targets its owner linked
// it to. It is a connector-opened stream (inbound.go has the wire). The hub advertises the
// "relay" capability in Welcome only for a tunnel it will take relays from (its account has
// the relay on and the server has a link); a connector never opens one otherwise, and a hub
// without an OnRelay hook closes it at once.

// CapabilityRelay is the capability a hub advertises (Welcome.Capabilities) when it takes
// relay streams from this tunnel.
const CapabilityRelay = "relay"

// KindRelay is the stream kind of a connector-opened relay request.
const KindRelay Kind = "relay"

// The only relay paths, and their request body caps.
const (
	RelayMessagePath = "/relay/v1/message"
	RelayTargetsPath = "/relay/v1/targets"

	RelayMaxMessage  = 64 << 10 // the whole message request
	RelayMaxTargets  = 1 << 10  // {"from_board": "..."}
	RelayMaxResponse = 64 << 10 // a response body (a target list is at most 50 entries)
)

// relayCap returns the request body cap for a relay path, or 0 if the path isn't one.
func relayCap(path string) int {
	switch path {
	case RelayMessagePath:
		return RelayMaxMessage
	case RelayTargetsPath:
		return RelayMaxTargets
	}
	return 0
}

// relayTimeout bounds a whole relay request on the connector (the hub waits up to 20 s
// for the target Coral).
const relayTimeout = 30 * time.Second

var relayKind = &inboundKind{
	kind: KindRelay, capability: CapabilityRelay, pathCap: relayCap, maxReply: RelayMaxResponse, timeout: relayTimeout,
	busy:   []byte(`{"ok":false,"error":"rate_limited"}`),
	failed: []byte(`{"ok":false,"error":"target_error"}`),
}

// ErrRelayUnsupported: the hub didn't advertise the relay for this tunnel (an older hub,
// the relay off for the account, or no link for this server).
var ErrRelayUnsupported = errors.New("tunnel: Coral Hub doesn't relay messages for this server")

// RelayRequest is what the hub's OnRelay hook gets: the path (one of the two relay paths)
// and the body, already within its cap. The source server is the Session's identity,
// never anything in the request.
type RelayRequest struct {
	Path string
	Body []byte
}

// RelayResponse is the hub's answer. RetryAfter (seconds, > 0) is sent as Retry-After.
type RelayResponse struct {
	Status     int
	Body       []byte
	RetryAfter int
}

// RelayFunc handles one relay request on the hub. s is the tunnel it came from (its
// Identity is the source server). The "relay" capability is fixed at the handshake, so
// it is no permission: OnRelay must re-check the account, the link and the target's mode
// on EVERY call (a link revoked or a mode lowered since the tunnel connected applies at
// once).
type RelayFunc func(ctx context.Context, s *Session, req RelayRequest) RelayResponse

// CanRelay reports whether the hub advertised the relay for this tunnel.
func (c *ClientSession) CanRelay() bool { return c.advertised(CapabilityRelay) }

// Relay sends one relay request (RelayMessagePath or RelayTargetsPath) through the tunnel
// and returns the hub's answer: its status, its JSON body and Retry-After (seconds, 0 if
// none). It fails with ErrRelayUnsupported when the hub didn't advertise the relay.
func (c *ClientSession) Relay(ctx context.Context, path string, body []byte) (RelayResponse, error) {
	return c.callInbound(ctx, relayKind, path, body, ErrRelayUnsupported)
}
