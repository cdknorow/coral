package hubproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// pacedUpload exercises real request-body reads and the production 30 s bound.
// Close interrupts a pending read when the proxy rejects an upload early.
type pacedUpload struct {
	ctx       context.Context
	cancel    context.CancelFunc
	remaining int64
	interval  time.Duration
}

func (b *pacedUpload) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	timer := time.NewTimer(b.interval)
	defer timer.Stop()
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-timer.C:
	}
	n := min(len(p), 32*1024, int(b.remaining))
	clear(p[:n])
	b.remaining -= int64(n)
	return n, nil
}
func (b *pacedUpload) Close() error { b.cancel(); return nil }

func TestProxyHeaderTimeoutSlowUpload(t *testing.T) {
	t.Parallel()
	const size = 5 * 1024 * 1024
	received := make(chan int64, 1)
	_, hub, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			return
		}
		received <- n
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	bodyCtx, closeBody := context.WithCancel(ctx)
	body := &pacedUpload{ctx: bodyCtx, cancel: closeBody, remaining: size, interval: 45 * time.Second / (size / (32 * 1024))}
	defer body.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hub.URL+"/api/remote/a/api/upload", body)
	require.NoError(t, err)
	req.ContentLength = size
	started := time.Now()
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	reply, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(reply))
	require.Equal(t, int64(size), <-received)
	require.GreaterOrEqual(t, time.Since(started), 45*time.Second)
	require.JSONEq(t, `{"ok":true}`, string(reply))
}

func TestProxyHeaderTimeoutAfterBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		body bool
		sse  bool
	}{
		{name: "chunked upload", body: true},
		{name: "no body"},
		{name: "event stream", sse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bodyEnded := make(chan time.Time, 1)
			remoteDone := make(chan struct{})
			_, hub, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(remoteDone)
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					return
				}
				bodyEnded <- time.Now()
				<-r.Context().Done() // The machine never sends response headers.
			}), nil)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			var body io.ReadCloser
			if tc.body {
				bodyCtx, closeBody := context.WithCancel(ctx)
				body = &pacedUpload{ctx: bodyCtx, cancel: closeBody, remaining: 4 * 32 * 1024, interval: 500 * time.Millisecond}
				defer body.Close()
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, hub.URL+"/api/remote/a/api/upload", body)
			require.NoError(t, err)
			if tc.sse {
				req.Header.Set("Accept", "text/event-stream")
			}
			resp, err := http.DefaultClient.Do(req)
			answered := time.Now()
			require.NoError(t, err)
			defer resp.Body.Close()
			reply, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadGateway, resp.StatusCode, string(reply))
			elapsed := answered.Sub(<-bodyEnded)
			require.GreaterOrEqual(t, elapsed, 29*time.Second, "30 s starts after the body, not at request start")
			require.Less(t, elapsed, 35*time.Second)
			select {
			case <-remoteDone:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream context was not canceled")
			}
		})
	}
}

// A browser that starts an upload then stops must release both sides rather
// than hold a proxy request and a machine's stream slot indefinitely.
func TestProxyUploadStallTimeout(t *testing.T) {
	t.Parallel()
	remoteDone := make(chan struct{})
	_, hub, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(remoteDone)
		_, _ = io.Copy(io.Discard, r.Body)
	}), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	go func() {
		_, _ = writer.Write([]byte("upload started"))
		pause := time.NewTimer(31 * time.Second)
		defer pause.Stop()
		select {
		case <-ctx.Done():
		case <-pause.C:
			_, _ = writer.Write([]byte("too late"))
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hub.URL+"/api/remote/a/api/upload", reader)
	require.NoError(t, err)
	started := time.Now()
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	require.GreaterOrEqual(t, time.Since(started), 29*time.Second)
	require.Less(t, time.Since(started), 35*time.Second)
	select {
	case <-remoteDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled upstream body reader was not released")
	}
}

type uploadTimeoutTarget struct {
	HTTPTarget
	roundTrip func(*http.Request) (*http.Response, error)
}

func (t uploadTimeoutTarget) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.roundTrip(r)
}

// Fake time exercises the actual ten-minute production cap without adding a
// ten-minute CI delay. The target reads continuously until context cancellation.
func TestProxyUploadOverallTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		body := &pacedUpload{remaining: 1 << 60, interval: time.Second, cancel: func() {}}
		var received int64
		target := uploadTimeoutTarget{roundTrip: func(r *http.Request) (*http.Response, error) {
			body.ctx = r.Context()
			var err error
			received, err = io.Copy(io.Discard, r.Body)
			return nil, err
		}}
		req := httptest.NewRequest(http.MethodPost, "/api/remote/a/api/upload", body)
		req.ContentLength = -1
		recorder := httptest.NewRecorder()
		started := time.Now()
		(&Handler{}).proxyHTTP(recorder, req, "a", target, "/api/upload", "/api/upload")
		require.Equal(t, http.StatusBadGateway, recorder.Code)
		require.Equal(t, 10*time.Minute, time.Since(started), "moving bytes reset the idle limit, never the total cap")
		require.Greater(t, received, int64(0))
		require.Greater(t, body.remaining, int64(0), "the trickle never reached EOF")
	})
}
