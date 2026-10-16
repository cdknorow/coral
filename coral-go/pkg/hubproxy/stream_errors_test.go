package hubproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/redact"
	"github.com/cdknorow/coral/pkg/tunnel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// connectCapped runs a tunnel to local with the given hub-side and
// connector-side stream caps.
func connectCapped(t *testing.T, local http.Handler, hubMax, connectorMax int) *tunnel.Session {
	t.Helper()
	ls := httptest.NewServer(local)
	t.Cleanup(ls.Close)
	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{MaxStreams: hubMax})
		if err == nil {
			sessions <- s
		}
	}))
	t.Cleanup(ths.Close)
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
		SessionConfig: tunnel.SessionConfig{MaxStreams: connectorMax},
		Handler:       &tunnel.LocalHandler{Redactor: redact.Rules, BaseURL: ls.URL, AllowPath: allowAll},
		AllowInsecure: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	s := <-sessions
	t.Cleanup(func() { s.Close() })
	return s
}

// holdingLocal serves /api/hold until release is closed (signalling entered
// first) and answers /api/ok at once.
func holdingLocal(entered chan<- struct{}, release <-chan struct{}) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/hold", func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.Write([]byte("held"))
	})
	mux.HandleFunc("/api/ok", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	return mux
}

// A connector at its stream cap is busy, not down: the hub answers 503 with
// Retry-After and leaves the server's status alone, whether the connector or
// the hub's own session refused the extra stream. Once the held stream ends the
// same request succeeds.
func TestStreamCapIs503AndDoesNotMarkUnreachable(t *testing.T) {
	for name, caps := range map[string][2]int{
		"connector cap": {256, 1},
		"hub cap":       {1, 256},
	} {
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}, 1), make(chan struct{})
			sess := connectCapped(t, holdingLocal(entered, release), caps[0], caps[1])
			res := &markingTunnelResolver{tunnelResolver: tunnelResolver{sess}}
			hub := cloudHub(t, res, nil)

			held := make(chan int, 1)
			go func() {
				resp, err := http.Get(hub.URL + "/api/remote/srv/api/hold")
				if err != nil {
					held <- 0
					return
				}
				resp.Body.Close()
				held <- resp.StatusCode
			}()
			<-entered // the only stream allowed is now open

			resp, err := http.Get(hub.URL + "/api/remote/srv/api/ok")
			require.NoError(t, err)
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
			assert.Equal(t, "1", resp.Header.Get("Retry-After"))
			assert.JSONEq(t, `{"error":"remote busy","code":"too_many_streams","server":"srv"}`, string(body))
			assert.Empty(t, res.recorded(), "a busy connector is not marked")

			close(release)
			assert.Equal(t, http.StatusOK, <-held)
			// The held stream's slot is released just after its response is
			// delivered (connector and hub sides), so wait for it rather than race it.
			require.Eventually(t, func() bool {
				resp, err := http.Get(hub.URL + "/api/remote/srv/api/ok")
				if err != nil {
					return false
				}
				resp.Body.Close()
				return resp.StatusCode == http.StatusOK
			}, 2*time.Second, 10*time.Millisecond, "the stream slot is free again")
		})
	}
}

// A request the connector cannot accept as sent (here a method outside its
// allowlist) is the request's fault: 400 bad_request, server not marked.
func TestConnectorBadRequestIs400AndDoesNotMarkUnreachable(t *testing.T) {
	local := http.NewServeMux()
	local.HandleFunc("/api/ok", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	res := &markingTunnelResolver{tunnelResolver: tunnelResolver{connectLocal(t, local, nil)}}
	hub := cloudHub(t, res, nil)

	req, err := http.NewRequest(http.MethodOptions, hub.URL+"/api/remote/srv/api/ok", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.JSONEq(t, `{"error":"bad request","code":"bad_request","server":"srv"}`, string(body))
	assert.Empty(t, res.recorded(), "a bad request does not mark the server")
}
