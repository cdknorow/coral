package hubproxy

import (
	"context"
	"io"
	"net/http"

	"github.com/coder/websocket"
)

// Meter throttles the bytes a TunnelTarget moves, so one tenant cannot saturate a tunnel or
// the node. WaitIn is called for bytes going browser to connector, WaitOut for bytes going
// connector to browser. Waiting blocks (back-pressure through the stream window); it never
// kills a stream, and it returns early only when ctx ends.
type Meter interface {
	WaitIn(ctx context.Context, n int) error
	WaitOut(ctx context.Context, n int) error
}

type meteredBody struct {
	io.ReadCloser
	ctx  context.Context
	wait func(context.Context, int) error
}

func (m *meteredBody) Read(p []byte) (int, error) {
	n, err := m.ReadCloser.Read(p)
	if n > 0 {
		if werr := m.wait(m.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}

func meterRequest(req *http.Request, m Meter) *http.Request {
	if m == nil || req.Body == nil || req.Body == http.NoBody {
		return req
	}
	out := req.Clone(req.Context())
	out.Body = &meteredBody{ReadCloser: req.Body, ctx: req.Context(), wait: m.WaitIn}
	return out
}

func meterResponse(resp *http.Response, m Meter, ctx context.Context) {
	if m != nil && resp != nil && resp.Body != nil {
		resp.Body = &meteredBody{ReadCloser: resp.Body, ctx: ctx, wait: m.WaitOut}
	}
}

// meteredWS counts WebSocket frames: Read returns connector output, Write sends browser input.
type meteredWS struct {
	WSConn
	m Meter
}

func (w meteredWS) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	typ, data, err := w.WSConn.Read(ctx)
	if err == nil {
		if werr := w.m.WaitOut(ctx, len(data)); werr != nil {
			return 0, nil, werr
		}
	}
	return typ, data, err
}

func (w meteredWS) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	if err := w.m.WaitIn(ctx, len(p)); err != nil {
		return err
	}
	return w.WSConn.Write(ctx, typ, p)
}
