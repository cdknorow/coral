package hubproxy

import (
	"context"
	"errors"
	"net/http"

	"github.com/cdknorow/coral/pkg/tunnel"
)

// TunnelTarget sends proxied requests through a Cloud Hub tunnel session to
// the connector, which executes them against the Coral server's local API.
// The connector owns credentials and SSRF protection (the local API is
// loopback only), so there is nothing to validate here.
type TunnelTarget struct{ Session *tunnel.Session }

// RoundTrip implements Target.
func (t TunnelTarget) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.Session.RoundTrip(req)
}

// DialWS implements Target. A local 401/403 on the websocket handshake maps to
// ErrRemoteRejected like it does for HTTPTarget.
func (t TunnelTarget) DialWS(ctx context.Context, path, rawQuery string, protocols []string) (WSConn, error) {
	c, err := t.Session.DialWS(ctx, path, rawQuery, protocols)
	if err != nil {
		var se *tunnel.StreamError
		if errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden) {
			return nil, ErrRemoteRejected
		}
		return nil, err
	}
	return c, nil
}
