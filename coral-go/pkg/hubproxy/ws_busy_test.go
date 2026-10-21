package hubproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/pkg/redact"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #3663: a terminal the machine refuses because too many are open through the hub reaches the
// browser as an accepted socket closed at once with StatusTooManyStreams and the machine's reason
// (a refused handshake's status is invisible to a page), so the terminal can say why.
func TestABusyMachinesTerminalClosesWithItsReason(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.Read(r.Context())
	}))
	defer local.Close()
	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{})
		if err == nil {
			sessions <- s
		}
	}))
	defer ths.Close()
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
		Handler: &tunnel.LocalHandler{Redactor: redact.Rules, BaseURL: local.URL, AllowPath: allowAll, HubLimits: tunnel.HubLimits{Long: 1}}, AllowInsecure: true,
	})
	require.NoError(t, err)
	defer cs.Close()
	sess := <-sessions
	defer sess.Close()
	hub := cloudHub(t, tunnelResolver{sess}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(hub.URL, "http") + "/api/remote/srv/ws/terminal/a"
	opts := &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {hub.URL}}}
	first, _, err := websocket.Dial(ctx, url, opts)
	require.NoError(t, err, "the one long slot")
	defer first.CloseNow()

	second, _, err := websocket.Dial(ctx, url, opts)
	require.NoError(t, err, "the refusal still opens the socket, so its reason can be read")
	defer second.CloseNow()
	_, _, err = second.Read(ctx)
	var ce websocket.CloseError
	require.True(t, errors.As(err, &ce), "closed with a status: %v", err)
	assert.Equal(t, StatusTooManyStreams, ce.Code)
	assert.Contains(t, ce.Reason, "too many terminals open through Coral Hub")
	assert.LessOrEqual(t, len(ce.Reason), 123)
}
