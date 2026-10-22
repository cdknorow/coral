package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// The connector tells the hub when this computer's own maximum access mode changes (#5918, coral-hub's "follow the machine"). It is a connector-opened stream of its own kind (inbound.go has
// the wire), one request each, on one path:
//
//	POST /control/mode   {"local_mode":"view"|"interact"|"launch"}   ->  204
//
// The body is exactly that and nothing else. The hub advertises "local_mode/1" in Welcome.Capabilities only when it serves it (SessionConfig.OnLocalMode); until it does, and for any answer that is
// not 204 (a refusal, 429, a timeout), the connector keeps what it did before: it reconnects, and the next Hello carries the mode (client.go). At most about one message per second per tunnel: the
// hub answers a faster one with 429 and Retry-After, and the connector coalesces (internal/cloud).

// CapabilityLocalMode is the capability a hub advertises when it takes the local mode change message.
const CapabilityLocalMode = "local_mode/1"

// KindControl is the stream kind of a connector-opened control request.
const KindControl Kind = "control"

// ControlModePath is the path of the local mode message.
const ControlModePath = "/control/mode"

// ControlMaxBody caps the request and reply bodies of a control request.
const ControlMaxBody = 128

// controlTimeout bounds one control request, on the connector and in the hub's hook.
const controlTimeout = 5 * time.Second

// LocalModeMinGap is the least time between two local mode messages the hub takes from one tunnel (SessionConfig.MinLocalModeGap overrides it). The connector holds its messages to a little more.
const LocalModeMinGap = time.Second

func controlCap(path string) int {
	if path != ControlModePath {
		return 0
	}
	return ControlMaxBody
}

var controlKind = &inboundKind{
	kind: KindControl, capability: CapabilityLocalMode, pathCap: controlCap, maxReply: ControlMaxBody, timeout: controlTimeout,
	busy: []byte(`{}`), failed: []byte(`{}`),
}

// ErrLocalModeUnsupported: the hub didn't advertise local_mode/1 for this tunnel, or closed the stream without answering.
var ErrLocalModeUnsupported = errors.New("tunnel: Coral Hub doesn't take local mode changes for this server")

// ErrLocalModeRefused: the hub answered a local mode message with something other than 204.
var ErrLocalModeRefused = errors.New("tunnel: Coral Hub refused the local mode message")

// LocalModeFunc handles a local mode message on the hub: the connector's tunnel and its new mode. It is told only of a well formed message that is not faster than one a second.
type LocalModeFunc func(ctx context.Context, s *Session, mode Mode)

// localModeBody is the body of a local mode message, exactly: one key, nothing else.
type localModeBody struct {
	LocalMode string `json:"local_mode"`
}

// parseLocalModeBody is the mode a body names, when it is exactly {"local_mode":"view|interact|launch"}.
func parseLocalModeBody(b []byte) (Mode, bool) {
	// Token by token, so that the object is exactly one key with one string value: an unknown key, a repeated one, a second value or trailing text is refused.
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return TierNever, false
	}
	if t, err := dec.Token(); err != nil || t != "local_mode" {
		return TierNever, false
	}
	t, err := dec.Token()
	name, isString := t.(string)
	if err != nil || !isString {
		return TierNever, false
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return TierNever, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return TierNever, false
	}
	if name != "view" && name != "interact" && name != "launch" { // the names exactly: no case folding, no spaces
		return TierNever, false
	}
	return ParseMode(name)
}

// localModeHook is the hub's hook for a control request: the mode is recorded on the Session (LocalMode) and OnLocalMode told, at most once a second; a faster one is 429 with Retry-After, a
// body that is not exactly the message is 400, and the answer to a good one is 204.
func (c SessionConfig) localModeHook() inboundHook {
	if c.OnLocalMode == nil {
		return nil
	}
	return func(ctx context.Context, s *Session, path string, body []byte) RelayResponse {
		mode, ok := parseLocalModeBody(body)
		if !ok {
			return RelayResponse{Status: http.StatusBadRequest, Body: []byte(`{}`)}
		}
		now := time.Now()
		s.controlMu.Lock()
		if !s.lastControl.IsZero() && now.Sub(s.lastControl) < c.minLocalModeGap() {
			s.controlMu.Unlock()
			return RelayResponse{Status: http.StatusTooManyRequests, Body: []byte(`{}`), RetryAfter: 1}
		}
		s.lastControl = now
		s.controlMu.Unlock()
		s.localMode.Store(int32(mode))
		c.OnLocalMode(ctx, s, mode)
		return RelayResponse{Status: http.StatusNoContent, Body: []byte(`{}`)}
	}
}

// CanSendLocalMode reports whether the hub advertised local_mode/1 for this tunnel.
func (c *ClientSession) CanSendLocalMode() bool { return c.advertised(CapabilityLocalMode) }

// SendLocalMode tells the hub this computer's maximum mode is now mode. nil only for the hub's 204; ErrLocalModeUnsupported when it didn't advertise the capability or closed the stream
// unanswered; ErrLocalModeRefused (with the status) for any other answer, 429 included; the context's error for a timeout. The caller falls back to a reconnect on any error.
func (c *ClientSession) SendLocalMode(ctx context.Context, mode Mode) error {
	if mode < TierView || mode > TierLaunch {
		return fmt.Errorf("tunnel: %d is not a mode", int(mode))
	}
	body, _ := json.Marshal(localModeBody{LocalMode: mode.String()})
	r, err := c.callInbound(ctx, controlKind, ControlModePath, body, ErrLocalModeUnsupported)
	if err != nil {
		return err
	}
	if r.Status != http.StatusNoContent {
		return fmt.Errorf("%w: status %d", ErrLocalModeRefused, r.Status)
	}
	return nil
}

func (c SessionConfig) minLocalModeGap() time.Duration {
	if c.MinLocalModeGap > 0 {
		return c.MinLocalModeGap
	}
	return LocalModeMinGap
}
