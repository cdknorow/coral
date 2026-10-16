package tunnel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sign in with Coral Hub (#3966, coral-hub PR #529): a browser signs in to this Coral through
// the owner's Coral Hub account. The Coral side talks to the hub over connector-opened streams
// (inbound.go has the wire), one request per stream, on four paths:
//
//	origins  the exact list of return origins this Coral serves its callback on
//	begin    start a flow (PKCE challenge, state, return origin): the hub's flow id and URL
//	redeem   trade the one-time code and the verifier for a grant
//	check    whether a grant still holds (every 60 s while the session lives)
//
// Bodies are JSON objects of at most SigninMaxBody bytes each way; their fields are the
// caller's (internal/cloud). The hub advertises "hub-signin" only behind its own switch, and
// its OnSignin hook takes the machine and account from the tunnel's identity, never the body.

// CapabilitySignin is the capability a hub advertises when it serves Hub sign-in.
const CapabilitySignin = "hub-signin"

// KindSignin is the stream kind of a connector-opened sign-in request.
const KindSignin Kind = "signin"

// The sign-in operations.
const (
	SigninOrigins = "origins"
	SigninBegin   = "begin"
	SigninRedeem  = "redeem"
	SigninCheck   = "check"
)

// SigninMaxBody caps every sign-in request and reply body.
const SigninMaxBody = 2 << 10

const signinPathPrefix = "/signin/v1/"

// signinTimeout bounds one sign-in request.
const signinTimeout = 10 * time.Second

func signinCap(path string) int {
	if signinOp(path) == "" {
		return 0
	}
	return SigninMaxBody
}

// signinOp is the operation a sign-in path names, or "" if it isn't one.
func signinOp(path string) string {
	op, ok := strings.CutPrefix(path, signinPathPrefix)
	if !ok {
		return ""
	}
	switch op {
	case SigninOrigins, SigninBegin, SigninRedeem, SigninCheck:
		return op
	}
	return ""
}

var signinKind = &inboundKind{
	kind: KindSignin, capability: CapabilitySignin, pathCap: signinCap, maxReply: SigninMaxBody, timeout: signinTimeout,
	busy: []byte(`{"ok":false}`), failed: []byte(`{"ok":false}`),
}

// ErrSigninUnsupported: the hub didn't advertise Hub sign-in for this tunnel.
var ErrSigninUnsupported = errors.New("tunnel: Coral Hub doesn't offer sign-in for this server")

// SigninRequest is what the hub's OnSignin hook gets: the operation and its body, already
// within SigninMaxBody. The machine is the Session's identity, never anything in the body.
type SigninRequest struct {
	Op   string
	Body []byte
}

// SigninResponse is the hub's answer. RetryAfter (seconds, > 0) is sent as Retry-After.
type SigninResponse struct {
	Status     int
	Body       []byte
	RetryAfter int
}

// SigninFunc handles one sign-in request on the hub. The capability is fixed at the
// handshake, so it is no permission: OnSignin must re-check the link, the owner and its own
// switch on EVERY call.
type SigninFunc func(ctx context.Context, s *Session, req SigninRequest) SigninResponse

// CanSignin reports whether the hub advertised Hub sign-in for this tunnel.
func (c *ClientSession) CanSignin() bool { return c.advertised(CapabilitySignin) }

// Signin sends one sign-in request (op is SigninOrigins, SigninBegin, SigninRedeem or
// SigninCheck) and returns the hub's answer. It fails with ErrSigninUnsupported when the hub
// didn't advertise sign-in or closed the stream unanswered.
func (c *ClientSession) Signin(ctx context.Context, op string, body []byte) (SigninResponse, error) {
	if signinOp(signinPathPrefix+op) == "" {
		return SigninResponse{}, fmt.Errorf("tunnel: %q is not a sign-in operation", op)
	}
	r, err := c.callInbound(ctx, signinKind, signinPathPrefix+op, body, ErrSigninUnsupported)
	return SigninResponse(r), err
}
