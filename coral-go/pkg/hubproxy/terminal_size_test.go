package hubproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/tunnel"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

// #3422: a machine reports its terminal pane's size to every viewer (terminal_size), and a viewer
// that may not set it (View mode) renders at that size. Through the hub, in View mode, set either
// by the hub (the stream policy) or by the machine (the connector's mode): the size frame reaches
// the browser byte for byte, and the browser's resize never reaches the machine. This is the hub
// proxy as coral-hub pins it today: no hub change is needed for the size frames.
func TestTerminalSizeFramesPassAViewOnlyHub(t *testing.T) {
	const sizeFrame = `{"type":"terminal_size","cols":187,"rows":52}`
	for _, tc := range []struct {
		name      string
		hubView   bool
		machineVw bool
	}{
		{"hub limits to View", true, false},
		{"machine limits to View", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan string, 8)
			local := http.NewServeMux()
			local.HandleFunc("/ws/terminal/x", func(w http.ResponseWriter, r *http.Request) {
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"terminal_hello","input_acks":true}`))
				c.Write(r.Context(), websocket.MessageText, []byte(sizeFrame))
				for {
					_, b, err := c.Read(r.Context())
					if err != nil {
						return
					}
					got <- string(b)
				}
			})
			ls := httptest.NewServer(local)
			defer ls.Close()
			sessions := make(chan *tunnel.Session, 1)
			ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{})
				if err == nil {
					sessions <- s
				}
			}))
			defer ths.Close()
			machineMode := tunnel.TierInteract
			if tc.machineVw {
				machineMode = tunnel.TierView
			}
			cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
				Handler: &tunnel.LocalHandler{BaseURL: ls.URL, AllowPath: func(string, string) bool { return true },
					MaxMode: func() tunnel.Mode { return machineMode }},
				AllowInsecure: true,
			})
			require.NoError(t, err)
			defer cs.Close()
			sess := <-sessions
			defer sess.Close()

			h := NewWithOptions(tunnelResolver{sess}, Options{AllowedOrigins: []string{"hub.test"}})
			r := chi.NewRouter()
			r.Handle("/api/remote/{server}/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				pol := StreamPolicy{}
				if tc.hubView {
					pol.AllowClientFrame = func() bool { return false }
				}
				h.ServeHTTP(w, req.WithContext(WithStreamPolicy(req.Context(), pol)))
			}))
			hub := httptest.NewServer(r)
			defer hub.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hub.URL, "http")+"/api/remote/srv/ws/terminal/x", &websocket.DialOptions{
				HTTPHeader: http.Header{"Origin": {"http://hub.test"}},
			})
			require.NoError(t, err)
			defer c.CloseNow()

			// The machine's frames arrive unchanged, in order.
			_, hello, err := c.Read(ctx)
			require.NoError(t, err)
			assert.JSONEq(t, `{"type":"terminal_hello","input_acks":true}`, string(hello))
			typ, size, err := c.Read(ctx)
			require.NoError(t, err)
			assert.Equal(t, websocket.MessageText, typ)
			assert.Equal(t, sizeFrame, string(size), "terminal_size passes the hub byte for byte")

			// A phone's resize in View mode never reaches the machine (the pane keeps its size).
			require.NoError(t, c.Write(ctx, websocket.MessageText, []byte(`{"type":"terminal_resize","cols":44,"rows":30}`)))
			select {
			case v := <-got:
				t.Fatalf("a View-only resize reached the machine: %q", v)
			case <-time.After(400 * time.Millisecond):
			}
		})
	}
}
